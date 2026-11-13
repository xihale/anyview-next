package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fastLimiter: 毫秒级窗口/间隔的限速器(线上是 60s 窗口, 测试不等真实时钟)。
func fastLimiter(perMin int, windowDur, spacing, cooldown time.Duration) *limiter {
	return &limiter{perMin: perMin, windowDur: windowDur, spacing: spacing, cooldown: cooldown}
}

func TestLimiterSpacing(t *testing.T) {
	l := fastLimiter(100, time.Second, 80*time.Millisecond, time.Minute)
	ctx := context.Background()
	if err := l.wait(ctx); err != nil {
		t.Fatalf("首个请求应放行: %v", err)
	}
	start := time.Now()
	if err := l.wait(ctx); err != nil {
		t.Fatalf("第二个请求应放行: %v", err)
	}
	if el := time.Since(start); el < 60*time.Millisecond {
		t.Fatalf("相邻放行应被 spacing 拉开, 实际间隔 %s", el)
	}
}

func TestLimiterWindowFull(t *testing.T) {
	l := fastLimiter(2, 150*time.Millisecond, 0, time.Minute)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := l.wait(ctx); err != nil {
			t.Fatalf("窗口内第 %d 个应放行: %v", i+1, err)
		}
		time.Sleep(40 * time.Millisecond) // 两条错开出窗时刻, 窗口滚动后各留痕迹
	}
	start := time.Now() // 此刻 t0+80ms, 窗口最老一条(t0)要等到 t0+150ms 才出窗
	if err := l.wait(ctx); err != nil {
		t.Fatalf("窗口满后应等到最老一条出窗再放行: %v", err)
	}
	if el := time.Since(start); el < 60*time.Millisecond {
		t.Fatalf("第三个请求应等窗口滚动(约 70ms, ≥60ms), 实际 %s", el)
	}
	if st := l.status(); st.WindowUsed != 2 {
		t.Fatalf("窗口占用应为 2(第二条+第三条), got %d", st.WindowUsed)
	}
}

func TestLimiterCooldown(t *testing.T) {
	l := fastLimiter(10, time.Second, 0, 40*time.Millisecond)
	l.observe(403, []byte(`<html>Client IP ... eventID ... reason:OnekeyCS</html>`))
	if _, ok := l.cooling(); !ok {
		t.Fatal("命中拦截页后应处于冷却")
	}
	err := l.wait(context.Background())
	if !errors.Is(err, errCooldown) {
		t.Fatalf("冷却期内应拒绝, got %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP 403") { // 错误里带上触发原因
		t.Fatalf("错误信息应含原因: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := l.wait(context.Background()); err != nil {
		t.Fatalf("冷却到期应恢复放行: %v", err)
	}
	if st := l.status(); st.Trips != 1 || st.CooldownRemainSec != 0 {
		t.Fatalf("状态不符: %+v", st)
	}
}

func TestWafHit(t *testing.T) {
	const block = `<html><body>Client IP: 1.2.3.4 ... eventID 123 reason:OnekeyCS ... WZWS-RAY</body></html>`
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"403+OnekeyCS", 403, block, true},
		{"403+eventID+ClientIP 无 OnekeyCS", 403, `Client IP: x eventID 1`, true},
		{"429 一律算", 429, `too many requests`, true},
		{"403 业务掉登录不算", 403, `{"message":"状态失效"}`, false},
		{"403 裸 body 不算", 403, `forbidden`, false},
		{"200 正常", 200, `{"code":200}`, false},
		{"404 坏机不算", 404, `nginx default`, false},
	}
	for _, c := range cases {
		if got := wafHit(c.status, []byte(c.body)); got != c.want {
			t.Fatalf("%s: wafHit=%v, 期望 %v", c.name, got, c.want)
		}
	}
}

func TestLimiterDisabled(t *testing.T) {
	l := newLimiter(0, time.Minute)
	l.observe(403, []byte("reason:OnekeyCS"))
	if err := l.wait(context.Background()); err != nil {
		t.Fatalf("不限速(-rpm 0/mock)时 observe 不得触发冷却拦截: %v", err)
	}
}

func TestLimiterCtxCancel(t *testing.T) {
	l := fastLimiter(1, 10*time.Second, 0, time.Minute) // 窗口 10s, 第 2 个必等
	ctx := context.Background()
	if err := l.wait(ctx); err != nil {
		t.Fatalf("首个应放行: %v", err)
	}
	ctx2, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := l.wait(ctx2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("等待中 ctx 取消应退出, got %v", err)
	}
}

func TestFailUpstreamMapping(t *testing.T) {
	rec := httptest.NewRecorder()
	failUpstream(rec, fmt.Errorf("外层: %w", errCooldown))
	if rec.Code != 429 {
		t.Fatalf("冷却错误应回 429, got %d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	failUpstream(rec, errors.New("dial tcp: refused"))
	if rec.Code != 502 {
		t.Fatalf("普通错误应回 502, got %d", rec.Code)
	}
}

// TestUpstreamFunnelCooldown: 端到端——上游回 403 wzws 拦截页, 漏斗(doUpstreamHdrAuth)
// 探测并进入冷却, 后续请求不再打上游(本地直接拒绝), 防续期封禁。
func TestUpstreamFunnelCooldown(t *testing.T) {
	var hits int
	mux := http.NewServeMux()
	mux.HandleFunc("/api/anything", func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(403)
		fmt.Fprint(w, `<html>Client IP: 9.9.9.9 eventID 777 reason:OnekeyCS</html>`)
	})
	up := httptest.NewServer(mux)
	defer up.Close()
	s := newServer(up.URL, "", "1.2.3.4", "ua", filepath.Join(t.TempDir(), "state.json"), false, "", false, 60, time.Minute)

	status, body, err := s.doUpstream(context.Background(), http.MethodGet, "/anything", nil, nil, "", "")
	if err != nil || status != 403 || !strings.Contains(string(body), "OnekeyCS") {
		t.Fatalf("首个请求应原样拿到拦截页: status=%d err=%v", status, err)
	}
	if hits != 1 {
		t.Fatalf("上游应被命中 1 次, got %d", hits)
	}
	if _, ok := s.lim.cooling(); !ok {
		t.Fatal("拦截页应触发全局冷却")
	}
	_, _, err = s.doUpstream(context.Background(), http.MethodGet, "/anything", nil, nil, "", "")
	if !errors.Is(err, errCooldown) {
		t.Fatalf("冷却期内应本地拒绝, got %v", err)
	}
	if hits != 1 {
		t.Fatalf("冷却期内不得再打上游(会续期封禁), hits=%d", hits)
	}
}

// TestProxyStaleDuringCooldown: 冷却期间题库(catalog 网络优先)走 stale 兜底,
// 前端仍有题可看, 且不再打上游。
func TestProxyStaleDuringCooldown(t *testing.T) {
	h := newCacheHandlerTest(t)
	h.s.lim = fastLimiter(60, time.Minute, time.Second, time.Minute) // 该 fixture 默认不限速, 这里换成启用

	rec := h.get(t, "/api/up/scheme-service/catalog/list?schemeId=1&classId=2")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"eid":1`) {
		t.Fatalf("首次拉取应成功: %d %s", rec.Code, rec.Body.String())
	}
	h.s.lim.observe(403, []byte(`Client IP x eventID 1 reason:OnekeyCS`)) // 模拟另一路触发拦截

	before := h.catalog.Load()
	rec = h.get(t, "/api/up/scheme-service/catalog/list?schemeId=1&classId=2")
	if rec.Code != 200 || rec.Header().Get("X-Relay-Stale") != "1" {
		t.Fatalf("冷却期间应回 stale 缓存: %d stale=%q body=%s",
			rec.Code, rec.Header().Get("X-Relay-Stale"), rec.Body.String())
	}
	if h.catalog.Load() != before {
		t.Fatalf("冷却期间不得再打上游: catalog hits %d → %d", before, h.catalog.Load())
	}
}
