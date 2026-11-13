// exam WS 计时管理器: relay 全权持有上游 wss 连接,
// 服务端 accumTime 只认 401 计时消息对(HTTP 打点零效果, 见 fuckView analysis/10):
//
//	{type:401, content:eID} 开始 / {type:401, content:null} 停止
//	{type:206} 连接后先问考试安排   {type:117} 心跳(40s)
//	type:119 = token 失效/被顶    statusType:403 = 停留过长 → 断开重连后重发 401
//
// 前端只调 POST /api/exam/tick {eid} / {eid:null}, 心跳重连全在 relay。
//
// 并发约定(审计后固定):
//   - e.conn 等全部字段只在 e.mu 下读写; wmu 只串行化单条 conn 上的并发写。
//   - 所有写带 10s 超时, 上游僵死时不会无限阻塞持有锁。
//   - reconnectLoop 唤醒后重查条件(Stop/Reset/切题/已建连都会改变状态),
//     不携带调度时刻的快照, 避免 stale eid 复活旧题计时。
//   - 拨号不持 e.mu; 读带 90s deadline, 心跳写失败会 Close 以唤醒 readLoop。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type examMgr struct {
	s          *server
	st         *session   // 所属浏览器会话: 计时帧/拨号/重连判定都用它的 token
	mu         sync.Mutex // 保护以下全部字段与连接生命周期
	wmu        sync.Mutex // 单条上游 conn 的写串行化
	conn       *websocket.Conn
	done       chan struct{} // 当前 conn 的心跳退出信号
	active     int64
	tickedAt   time.Time
	kicked     bool
	lastErr    string
	reconn     bool          // reconnectLoop 在途标志
	reconnStop chan struct{} // 取消在途 reconnectLoop 睡眠的信号
}

const (
	examWriteTimeout = 10 * time.Second
	examReadTimeout  = 90 * time.Second // 心跳 40s, 对端僵死时 Read 必须有上限
	examDialTimeout  = 15 * time.Second
)

func newExamMgr(s *server, st *session) *examMgr { return &examMgr{s: s, st: st} }

func (e *examMgr) frame(typeID int, content any) map[string]any {
	return map[string]any{
		"type":      typeID,
		"content":   content,
		"timestamp": fmt.Sprint(time.Now().UnixMilli()),
		"token":     e.st.Token(),
	}
}

// sendConn: 向指定 conn 写一帧。10s 写超时防上游 TCP 零窗口时永久阻塞。
// 可在持 e.mu 时调用(wmu 从不反向获取 e.mu, 无锁序倒置)。
func (e *examMgr) sendConn(c *websocket.Conn, typeID int, content any) error {
	b, _ := json.Marshal(e.frame(typeID, content))
	e.wmu.Lock()
	defer e.wmu.Unlock()
	_ = c.SetWriteDeadline(time.Now().Add(examWriteTimeout))
	return c.WriteMessage(websocket.TextMessage, b)
}

// dialExam: 拨号, 不持 e.mu (握手最长 examDialTimeout, 持锁会卡死 Tick/Stop/Status)。
func (e *examMgr) dialExam() (*websocket.Conn, error) {
	tok := e.st.Token()
	if tok == "" {
		return nil, fmt.Errorf("未登录")
	}
	u := *e.s.base
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = "/api/exam-service/websocket/" + tok
	hdr := http.Header{}
	hdr.Set("User-Agent", e.s.ua)
	hdr.Set("X-Forwarded-For", e.s.xff)
	hdr.Set("Origin", e.s.base.Scheme+"://"+e.s.base.Host)

	// 若配置了 -dial，WS 同样直连目标地址(SNI 与 Host 仍用上游域名)
	dialer := &websocket.Dialer{HandshakeTimeout: examDialTimeout}
	if e.s.dialAddr != "" {
		d := &net.Dialer{Timeout: examDialTimeout, KeepAlive: 30 * time.Second}
		dialer.NetDialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return d.DialContext(ctx, network, e.s.dialAddr)
		}
	}
	c, _, err := dialer.Dial(u.String(), hdr)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// attachLocked: 接管已拨号的 conn。调用方须持 e.mu, 且 e.conn 须为 nil。
func (e *examMgr) attachLocked(c *websocket.Conn) error {
	_ = c.SetReadDeadline(time.Now().Add(examReadTimeout))
	done := make(chan struct{})
	e.conn = c
	e.done = done
	e.lastErr = ""
	if err := e.sendConn(c, 206, nil); err != nil {
		e.teardownLocked()
		e.lastErr = "206 握手失败: " + err.Error()
		return err
	}
	go e.readLoop(c)
	go e.heartbeat(c, done)
	return nil
}

// teardownLocked: 关闭当前连接与心跳信号。调用方须持 e.mu; 重复调用安全。
func (e *examMgr) teardownLocked() {
	if e.conn != nil {
		_ = e.conn.Close()
		e.conn = nil
	}
	if e.done != nil {
		close(e.done)
		e.done = nil
	}
}

// scheduleReconnectLocked: 需要且尚未安排重连时置位并返回 stopCh 与 true。
// 调用方须持 e.mu, 并在解锁后启动 reconnectLoop(stopCh) goroutine。
func (e *examMgr) scheduleReconnectLocked() (chan struct{}, bool) {
	if e.reconn {
		return nil, false
	}
	if e.active <= 0 || e.kicked || e.st.Token() == "" {
		return nil, false
	}
	ch := make(chan struct{})
	e.reconn = true
	e.reconnStop = ch
	return ch, true
}

func (e *examMgr) cancelReconnectLocked() {
	if e.reconnStop != nil {
		close(e.reconnStop)
		e.reconnStop = nil
	}
	e.reconn = false
}

func (e *examMgr) readLoop(c *websocket.Conn) {
	for {
		_ = c.SetReadDeadline(time.Now().Add(examReadTimeout))
		_, data, err := c.ReadMessage()
		if err != nil {
			// 只清理属于自己的连接: 旧 conn 迟到的错误不能误杀新连接
			e.mu.Lock()
			if e.conn == c {
				e.conn = nil
				if e.done != nil {
					close(e.done)
					e.done = nil
				}
			}
			_ = c.Close() // 错误路径 conn 不会自动关, 不关则泄漏 fd
			stopCh, need := e.scheduleReconnectLocked()
			e.mu.Unlock()
			if need {
				go e.reconnectLoop(stopCh)
			}
			return
		}
		var m struct {
			Type       int `json:"type"`
			StatusType int `json:"statusType"`
		}
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		if m.Type == 119 {
			e.mu.Lock()
			e.kicked = true
			e.lastErr = "token 失效或账号在他处登录顶掉(119)"
			e.teardownLocked()
			e.cancelReconnectLocked()
			e.mu.Unlock()
			return
		}
		if m.StatusType == 403 {
			// 停留过长: 按前端做法断开重连后重发 401(仅计时中才重连)
			e.mu.Lock()
			e.teardownLocked()
			stopCh, need := e.scheduleReconnectLocked()
			e.mu.Unlock()
			if need {
				go e.reconnectLoop(stopCh)
			}
			return
		}
	}
}

// reconnectLoop: 断线后按指数退避(3s→60s 封顶)重连并恢复当前 active 题的计时对。
// 每次唤醒都重查活状态, 期间被 Stop/Reset/新 Tick 接管则自行退出。
func (e *examMgr) reconnectLoop(stopCh chan struct{}) {
	delay := 3 * time.Second
	clearReconnLocked := func() {
		if e.reconnStop == stopCh {
			e.reconn = false
			e.reconnStop = nil
		}
	}
	for {
		t := time.NewTimer(delay)
		select {
		case <-t.C:
		case <-stopCh:
			t.Stop()
			e.mu.Lock()
			clearReconnLocked()
			e.mu.Unlock()
			return
		}
		e.mu.Lock()
		if !e.reconn || e.reconnStop != stopCh || e.active == 0 || e.kicked || e.st.Token() == "" || e.conn != nil {
			clearReconnLocked()
			e.mu.Unlock()
			return
		}
		e.mu.Unlock()

		c, err := e.dialExam()
		e.mu.Lock()
		if err != nil {
			if !e.reconn || e.reconnStop != stopCh || e.active == 0 || e.kicked || e.st.Token() == "" || e.conn != nil {
				clearReconnLocked()
				e.mu.Unlock()
				return
			}
			e.lastErr = "拨号失败: " + err.Error()
			e.mu.Unlock()
			if delay < 60*time.Second {
				delay *= 2
			}
			continue
		}
		if !e.reconn || e.reconnStop != stopCh || e.active == 0 || e.kicked || e.st.Token() == "" || e.conn != nil {
			_ = c.Close()
			if e.conn != nil || !e.reconn || e.reconnStop != stopCh || e.active == 0 || e.kicked {
				clearReconnLocked()
				e.mu.Unlock()
				return
			}
			e.mu.Unlock()
			continue
		}
		if err := e.attachLocked(c); err != nil {
			e.mu.Unlock()
			if delay < 60*time.Second {
				delay *= 2
			}
			continue
		}
		err = e.sendConn(e.conn, 401, e.active)
		if err != nil {
			e.teardownLocked()
			e.lastErr = "重连后恢复计时失败: " + err.Error()
			e.mu.Unlock()
			if delay < 60*time.Second {
				delay *= 2
			}
			continue
		}
		clearReconnLocked()
		e.mu.Unlock()
		return
	}
}

func (e *examMgr) heartbeat(c *websocket.Conn, done chan struct{}) {
	t := time.NewTicker(40 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			// 只在自己的 conn 上发: 连接被替换后写失败自然退出, 不会污染新连接
			if err := e.sendConn(c, 117, nil); err != nil {
				_ = c.Close() // 解开 readLoop 的阻塞读, 走错误路径重连
				return
			}
		case <-done:
			return
		}
	}
}

// ===== REST 处理 =====

func (e *examMgr) Tick(eid int64) error {
	if e.s.mock {
		e.mu.Lock()
		e.active, e.tickedAt, e.kicked, e.lastErr = eid, time.Now(), false, ""
		e.mu.Unlock()
		return nil
	}

	e.mu.Lock()
	if e.kicked {
		err := fmt.Errorf("%s", e.lastErr)
		e.mu.Unlock()
		return err
	}
	if e.conn == nil {
		e.mu.Unlock()
		c, err := e.dialExam()
		e.mu.Lock()
		if err != nil {
			if e.kicked {
				err = fmt.Errorf("%s", e.lastErr)
			} else {
				e.lastErr = "拨号失败: " + err.Error()
			}
			e.mu.Unlock()
			return err
		}
		if e.kicked || e.st.Token() == "" {
			_ = c.Close()
			msg := e.lastErr
			if msg == "" {
				msg = "未登录"
			}
			e.mu.Unlock()
			return fmt.Errorf("%s", msg)
		}
		if e.conn == nil {
			if err := e.attachLocked(c); err != nil {
				e.mu.Unlock()
				return err
			}
		} else {
			_ = c.Close()
		}
	}
	if e.conn == nil {
		e.mu.Unlock()
		return fmt.Errorf("exam ws 未连接")
	}
	if e.kicked {
		err := fmt.Errorf("%s", e.lastErr)
		e.mu.Unlock()
		return err
	}
	if e.active == eid {
		e.tickedAt = time.Now()
		e.mu.Unlock()
		return nil
	}
	if e.active > 0 {
		_ = e.sendConn(e.conn, 401, nil) // 单活计时器: 先停上一题
		e.active = 0                     // 切题启动失败不得让 reconnect 复活旧题
	}
	if err := e.sendConn(e.conn, 401, eid); err != nil {
		e.teardownLocked()
		e.lastErr = "发送 401 失败: " + err.Error()
		e.mu.Unlock()
		return err
	}
	e.active, e.tickedAt = eid, time.Now()
	e.mu.Unlock()
	return nil
}

// Stop: 停当前计时并断开连接(心跳随之停止; 下次 Tick 会按需重连,
// 修复旧实现登出后心跳带空 token 每 40s 永久打上游的问题)。
func (e *examMgr) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.active = 0
	e.cancelReconnectLocked()
	if e.s.mock {
		return
	}
	if e.conn != nil {
		_ = e.sendConn(e.conn, 401, nil)
		e.teardownLocked()
	}
}

// Reset: 登录成功/登出时全量复位。修复被顶(119)后 kicked 永不复位、
// 重新登录也无法恢复计时的问题。在途 reconnectLoop 醒来后见信号立即退出。
func (e *examMgr) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.conn != nil && e.active > 0 && !e.s.mock {
		_ = e.sendConn(e.conn, 401, nil) // 登出/重登必须发停对, 否则上游 accumTime 继续跑
	}
	e.active = 0
	e.kicked = false
	e.lastErr = ""
	e.cancelReconnectLocked()
	e.teardownLocked()
}

type examStatus struct {
	Active   int64  `json:"active"`
	Running  bool   `json:"running"`
	TickedAt int64  `json:"tickedAt"`
	Kicked   bool   `json:"kicked"`
	Conn     bool   `json:"connected"`
	LastErr  string `json:"lastError,omitempty"`
}

func (e *examMgr) Status() examStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	return examStatus{
		Active:  e.active,
		Running: e.active > 0,
		TickedAt: func() int64 {
			if e.tickedAt.IsZero() {
				return 0
			}
			return e.tickedAt.UnixMilli()
		}(),
		Kicked:  e.kicked,
		Conn:    (e.s.mock && e.active > 0) || (!e.s.mock && e.conn != nil),
		LastErr: e.lastErr,
	}
}

func (s *server) handleExamTick(w http.ResponseWriter, r *http.Request) {
	sess := s.sessions.current(r)
	if sess == nil || !sess.loggedIn() {
		fail(w, 401, "未登录")
		return
	}
	var req struct {
		EID *int64 `json:"eid"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
		fail(w, 400, "需要 eid(数字, null=停)")
		return
	}
	if req.EID == nil || *req.EID == 0 {
		sess.exam.Stop()
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}
	if err := sess.exam.Tick(*req.EID); err != nil {
		fail(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "active": *req.EID})
}

func (s *server) handleExamStatus(w http.ResponseWriter, r *http.Request) {
	sess := s.sessions.current(r)
	if sess == nil {
		fail(w, 401, "未登录")
		return
	}
	writeJSON(w, 200, sess.exam.Status())
}
