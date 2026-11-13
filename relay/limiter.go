// 上游限速与 WAF 防封。实测口径(fuckView analysis/09, 2026-09-07):
//   - 云端 wzws WAF 按"单位时间请求量"拉黑, 几十个/分钟级别即触发(403 + body
//     reason:OnekeyCS + Client IP + eventID);
//   - 封禁期内继续请求会**续期**——5s 一锤永远解不了封, 触发后要完全静默 ≥30 分钟;
//   - 正常运行要压平瞬时突发(8s 间隔实测安全)。
//
// 据此三件事, 全部收敛在 doUpstream* 这一个漏斗上:
//
//  1. 滑动窗口限速: 滚动 60s 内最多放行 -rpm 个请求, 相邻放行自动按 60s/rpm
//     拉开间隔(登录对/判题轮询/后台校验/透传全走同一把尺);
//  2. WAF 拦截探测: 响应命中拦截页特征(403+OnekeyCS/eventID+Client IP, 或 429)
//     即进入全局静默冷却(-wafCooldown, 默认 30m), 期间一切上游请求本地直接拒绝,
//     绝不再打一枪——续期封禁比晚几分钟更贵;
//  3. 冷却期间缓存池的 stale 兜底照常工作: 题库/题面/作答代码仍可看可写(本地)。
//
// exam WS 不走这里: 已建连接的心跳是 40s/条(连接级而非请求级), 断线重连已有
// 3s→60s 指数退避。GET /api/health 可查看当前窗口占用与冷却状态。
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

var (
	// errCooldown: WAF 静默冷却期内上游请求被本地拒绝(封禁期内继续请求会续期)。
	errCooldown = errors.New("上游 WAF 冷却中")
	// errQueueTimeout: 排队等限速窗口超出单请求最长等待。
	errQueueTimeout = errors.New("上游限速排队超时")
)

// limiterMaxWait: 窗口占满时单个请求最长排队时间。须 > 60s(窗口时长)才能等到
// 最老一条出窗; 加上 http.Client 60s 超时仍在 server WriteTimeout 300s 之内。
const limiterMaxWait = 90 * time.Second

type limiter struct {
	mu        sync.Mutex
	perMin    int           // 滚动窗口放行上限; ≤0 = 不限速(mock/显式 -rpm 0)
	windowDur time.Duration // 滚动窗口时长(线上固定 1 分钟, 与 WAF 阈值同口径)
	spacing   time.Duration // 相邻放行最小间隔 = windowDur/perMin, 压平突发
	cooldown  time.Duration // 命中 WAF 特征后的静默时长
	window    []time.Time   // 窗口内放行时刻(单调递增)
	last      time.Time
	until     time.Time // 冷却截止; 零值 = 未冷却
	reason    string
	trips     int
	passed    int
	blocked   int
}

func newLimiter(perMin int, cooldown time.Duration) *limiter {
	l := &limiter{perMin: perMin, windowDur: time.Minute, cooldown: cooldown}
	if perMin > 0 {
		l.spacing = time.Minute / time.Duration(perMin)
	}
	return l
}

func (l *limiter) enabled() bool { return l != nil && l.perMin > 0 }

// wait: 阻塞到可放行(窗口未满且距上次放行 ≥ spacing)。冷却期/排队超时/ctx 取消
// 返回错误。等待按 500ms 分片: 别的请求触发冷却时, 排队者立即跟着退出。
func (l *limiter) wait(ctx context.Context) error {
	if !l.enabled() {
		return nil
	}
	deadline := time.Now().Add(limiterMaxWait)
	for {
		l.mu.Lock()
		now := time.Now()
		if now.Before(l.until) {
			rem, reason := l.until.Sub(now), l.reason
			l.blocked++
			l.mu.Unlock()
			return fmt.Errorf("%w: 剩余 %s(原因: %s), 封禁期内继续请求会续期, 本地已静默",
				errCooldown, rem.Truncate(time.Second), reason)
		}
		cutoff := now.Add(-l.windowDur)
		live := l.window[:0]
		for _, t := range l.window {
			if t.After(cutoff) {
				live = append(live, t)
			}
		}
		l.window = live
		var delay time.Duration
		if len(l.window) >= l.perMin {
			if d := l.window[0].Add(l.windowDur).Sub(now); d > delay {
				delay = d
			}
		}
		if d := l.last.Add(l.spacing).Sub(now); d > delay {
			delay = d
		}
		if delay <= 0 {
			l.window = append(l.window, now)
			l.last = now
			l.passed++
			l.mu.Unlock()
			return nil
		}
		l.mu.Unlock()
		if now.Add(delay).After(deadline) {
			return fmt.Errorf("%w: 60s 窗口已满(-rpm=%d), 稍后再试", errQueueTimeout, l.perMin)
		}
		if delay > 500*time.Millisecond {
			delay = 500 * time.Millisecond
		}
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// wafHit: 响应是否为 WAF 拦截页。必须与网关业务 403("状态失效"=掉登录)区分:
// wzws 拦截页 body 带 reason:OnekeyCS / Client IP + eventID(analysis/09)。
// 不认 WZWS-RAY 头——wzws 体系的正常响应也可能带它, 单凭头部会误判掉登录为封禁。
func wafHit(status int, body []byte) bool {
	if status == 429 {
		return true
	}
	if status != 403 {
		return false
	}
	return bytes.Contains(body, []byte("OnekeyCS")) ||
		(bytes.Contains(body, []byte("eventID")) && bytes.Contains(body, []byte("Client IP")))
}

// observe: 上游响应过一遍 WAF 探测, 命中即(重新)起算全局静默冷却——这条请求
// 确实打到了 WAF, 续期是事实。冷却中不会发新请求, 正常只有单发探测会再次命中。
func (l *limiter) observe(status int, body []byte) {
	if !l.enabled() || !wafHit(status, body) {
		return
	}
	l.mu.Lock()
	l.until = time.Now().Add(l.cooldown)
	l.reason = fmt.Sprintf("HTTP %d WAF 拦截页", status)
	l.trips++
	until := l.until
	l.mu.Unlock()
	log.Printf("[waf] 上游 %d 命中 WAF 拦截特征, 全局静默冷却至 %s——期间上游请求一律本地拒绝, 防续期封禁",
		status, until.Format(time.RFC3339))
}

// cooling: 当前是否处于静默冷却, 及剩余时长。上游 403/429 被 observe 判定后,
// 当前这条响应本身也是废的(HTML 拦截页), 调用方应直接报 429 冷却而非硬解析。
func (l *limiter) cooling() (time.Duration, bool) {
	if !l.enabled() {
		return 0, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	rem := time.Until(l.until)
	if rem <= 0 {
		return 0, false
	}
	return rem, true
}

type limiterStatus struct {
	RPM               int    `json:"rpm"`               // 0 = 不限速
	WindowUsed        int    `json:"windowUsed"`        // 滚动窗口内已用额度
	CooldownRemainSec int    `json:"cooldownRemainSec"` // 0 = 未冷却
	LastReason        string `json:"lastReason,omitempty"`
	Trips             int    `json:"trips"`
}

func (l *limiter) status() limiterStatus {
	if !l.enabled() {
		return limiterStatus{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := time.Now().Add(-l.windowDur)
	live := l.window[:0]
	for _, t := range l.window {
		if t.After(cutoff) {
			live = append(live, t)
		}
	}
	l.window = live
	st := limiterStatus{
		RPM: l.perMin, WindowUsed: len(l.window),
		LastReason: l.reason, Trips: l.trips,
	}
	if rem := time.Until(l.until); rem > 0 {
		st.CooldownRemainSec = int(rem.Truncate(time.Second).Seconds()) + 1
	}
	return st
}

// failUpstream: doUpstream 失败的统一出口——限速冷却/排队超时回 429(语义就是
// "稍后再试"), 其余按上游不可达 502。前端 useApi 按 message 原样展示。
func failUpstream(w http.ResponseWriter, err error) {
	if errors.Is(err, errCooldown) || errors.Is(err, errQueueTimeout) {
		fail(w, 429, err.Error())
		return
	}
	fail(w, 502, "上游不可达（请检查网络、-upstream 或 -dial 配置）: "+err.Error())
}

// failCooldown: 上游响应本身已是 WAF 拦截页时的直接回话(不硬解析 HTML)。
func failCooldown(w http.ResponseWriter, rem time.Duration) {
	fail(w, 429, fmt.Sprintf("上游 WAF 拦截, 已进入静默冷却(剩余 %s)——期间请求被本地拒绝以防封禁续期, 稍后再试",
		rem.Truncate(time.Second)))
}
