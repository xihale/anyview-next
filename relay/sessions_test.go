package main

// 多账号并发会话的行为回归: 每个浏览器一份登录态, 互不可见互不顶掉。
// 登录用 mock 上游(免真实凭据), 语义与真实链路同一 handlers。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newMockRelay(t *testing.T) *server {
	t.Helper()
	return newServer("http://127.0.0.1:1", "", "1.2.3.4", "ua", filepath.Join(t.TempDir(), "state.json"), true, "", false, 20, time.Minute)
}

// login: 走真实 handleLogin 流程(mock 上游), 返回下发的 an_sid。
// withSID 非空 = 该浏览器已有的会话 cookie(测换号复用)
func login(t *testing.T, s *server, username, withSID string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader(`{"username":"`+username+`","password":"x"}`))
	if withSID != "" {
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: withSID})
	}
	s.handleLogin(rec, r)
	if rec.Code != 200 {
		t.Fatalf("登录 %s 应 200, got %d body=%s", username, rec.Code, rec.Body.String())
	}
	sid := sidFromSetCookie(t, rec)
	if sid == "" {
		t.Fatalf("登录 %s 应下发会话 cookie", username)
	}
	return sid
}

func authStatus(t *testing.T, s *server, sid string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	if sid != "" {
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sid})
	}
	s.handleAuthStatus(rec, r)
	var m map[string]any
	if rec.Code != 200 {
		t.Fatalf("auth/status 应 200, got %d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("auth/status 解析失败: %v", err)
	}
	return m
}

func logout(t *testing.T, s *server, sid string) {
	t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sid})
	s.handleLogout(rec, r)
	if rec.Code != 200 {
		t.Fatalf("logout 应 200, got %d", rec.Code)
	}
}

// 两个浏览器先后登录不同账号: 各自 status 正确; 任一方登出, 另一方不受影响
func TestConcurrentBrowserSessionsIndependent(t *testing.T) {
	s := newMockRelay(t)
	sidA := login(t, s, "3125000001", "")
	sidB := login(t, s, "3125000002", "")

	for _, tc := range []struct{ sid, user string }{{sidA, "3125000001"}, {sidB, "3125000002"}} {
		m := authStatus(t, s, tc.sid)
		if loggedIn, _ := m["loggedIn"].(bool); !loggedIn {
			t.Fatalf("%s 应为已登录: %v", tc.user, m)
		}
		if u, _ := m["username"].(string); u != tc.user {
			t.Fatalf("username 应为 %s, got %v", tc.user, m["username"])
		}
	}

	logout(t, s, sidB)
	if loggedIn, _ := authStatus(t, s, sidA)["loggedIn"].(bool); !loggedIn {
		t.Fatal("B 登出不得影响 A 的会话")
	}
	if loggedIn, _ := authStatus(t, s, sidB)["loggedIn"].(bool); loggedIn {
		t.Fatal("B 登出后自己的会话应消失")
	}
	logout(t, s, sidA)
	if loggedIn, _ := authStatus(t, s, sidA)["loggedIn"].(bool); loggedIn {
		t.Fatal("A 登出后会话应消失")
	}
	if n := s.sessions.count(); n != 0 {
		t.Fatalf("全部登出后会话应清空, got %d", n)
	}
}

// 同一浏览器换账号登录: 复用同一会话, cookie 不变, 身份整体替换
func TestSameBrowserSwitchesAccount(t *testing.T) {
	s := newMockRelay(t)
	sid1 := login(t, s, "u1", "")
	sid2 := login(t, s, "u2", sid1) // 同一浏览器带着旧 cookie 换号
	if sid1 != sid2 {
		t.Fatal("同浏览器换号应复用同一会话句柄")
	}
	if u, _ := authStatus(t, s, sid2)["username"].(string); u != "u2" {
		t.Fatalf("换号后身份应为 u2, got %q", u)
	}
	if n := s.sessions.count(); n != 1 {
		t.Fatalf("换号不应残留旧会话, got %d", n)
	}
}

// 无会话(无/伪造 cookie)访问业务端点一律 401; auth/status 回匿名快照
func TestUnauthenticatedRejected(t *testing.T) {
	// 真实模式: 业务端点在 mock 短路之前就按会话拦截; 上游不可达不影响 401 判定
	s := newServer("http://127.0.0.1:1", "", "1.2.3.4", "ua", filepath.Join(t.TempDir(), "state.json"), false, "", false, 20, time.Minute)
	cases := []struct {
		name, method, path, body string
		h                        func(w http.ResponseWriter, r *http.Request)
	}{
		{"run", "POST", "/api/work/run", `{"eid":1,"code":"x"}`, s.handleRun},
		{"save", "POST", "/api/work/save", `{"eid":1,"code":"x"}`, s.handleSave},
		{"proxy", "GET", "/api/up/scheme-service/catalog/list", "", s.handleProxy},
		{"gdb", "POST", "/api/gdb/oj/debug/start", `{"eID":"1"}`, s.handleGdb},
		{"examTick", "POST", "/api/exam/tick", `{"eid":1}`, s.handleExamTick},
		{"examStatus", "GET", "/api/exam/status", "", s.handleExamStatus},
	}
	for _, c := range cases {
		for _, sid := range []string{"", "forged-sid"} {
			rec := httptest.NewRecorder()
			r := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
			if sid != "" {
				r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sid})
			}
			c.h(rec, r)
			if rec.Code != 401 {
				t.Fatalf("%s (cookie=%q) 应 401, got %d body=%s", c.name, sid, rec.Code, rec.Body.String())
			}
		}
	}
	m := authStatus(t, s, "")
	if loggedIn, _ := m["loggedIn"].(bool); loggedIn {
		t.Fatalf("无 cookie 的 status 应为未登录: %v", m)
	}
}

// exam 计时器按会话私有: 两个账号各刷各的题, 状态互不覆盖; 一方登出另一方照跑
func TestExamPerSession(t *testing.T) {
	s := newMockRelay(t)
	sidA := login(t, s, "uA", "")
	sidB := login(t, s, "uB", "")

	tick := func(sid string, eid int64) {
		t.Helper()
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/exam/tick", strings.NewReader(`{"eid":`+strconv.FormatInt(eid, 10)+`}`))
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sid})
		s.handleExamTick(rec, r)
		if rec.Code != 200 {
			t.Fatalf("tick 应 200, got %d body=%s", rec.Code, rec.Body.String())
		}
	}
	status := func(sid string) examStatus {
		t.Helper()
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/exam/status", nil)
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sid})
		s.handleExamStatus(rec, r)
		if rec.Code != 200 {
			t.Fatalf("exam/status 应 200, got %d", rec.Code)
		}
		var st examStatus
		if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
			t.Fatalf("exam/status 解析失败: %v", err)
		}
		return st
	}

	tick(sidA, 114001)
	tick(sidB, 114002)
	if st := status(sidA); st.Active != 114001 {
		t.Fatalf("A 的 active 应为 114001: %+v", st)
	}
	if st := status(sidB); st.Active != 114002 {
		t.Fatalf("B 的 active 应为 114002(B 不得被 A 覆盖): %+v", st)
	}
	logout(t, s, sidA)
	if st := status(sidB); st.Active != 114002 || !st.Running {
		t.Fatalf("A 登出不得影响 B 的计时: %+v", st)
	}
	// A 的会话已随登出摘除, 再查 status 应 401
	rec := httptest.NewRecorder()
	rA := httptest.NewRequest(http.MethodGet, "/api/exam/status", nil)
	rA.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sidA})
	s.handleExamStatus(rec, rA)
	if rec.Code != 401 {
		t.Fatalf("登出会话的 exam/status 应 401, got %d", rec.Code)
	}
	if n := s.sessions.count(); n != 1 {
		t.Fatalf("A 登出后应只剩 B 一个会话, got %d", n)
	}
}

// 会话文件随重启恢复: cookie 不变即免重登(mock 模式不落盘, 用真实模式直灌 store)
func TestSessionResumeAfterRestart(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")
	s1 := newServer("http://127.0.0.1:1", "", "1.2.3.4", "ua", stateFile, false, "", false, 20, time.Minute)
	sess := s1.sessions.newSession()
	sess.set("tok-x", "u1", "1", "同学一", time.Now().Add(time.Hour))
	s1.sessions.put(sess)

	s2 := newServer("http://127.0.0.1:1", "", "1.2.3.4", "ua", stateFile, false, "", false, 20, time.Minute)
	m := authStatus(t, s2, sess.sid)
	if loggedIn, _ := m["loggedIn"].(bool); !loggedIn {
		t.Fatalf("重启后同 cookie 应仍登录: %v", m)
	}
	if name, _ := m["studentName"].(string); name != "同学一" {
		t.Fatalf("重启后身份字段应恢复: %v", m)
	}

	// 重启后用原 cookie 登出: 必须从 bySID 和磁盘里彻底清除(曾因 load 未设 sess.sid 导致登出无效)
	logout(t, s2, sess.sid)
	if s2.sessions.count() != 0 {
		t.Fatalf("重启后登出应清空会话, got %d", s2.sessions.count())
	}
	if loggedIn, _ := authStatus(t, s2, sess.sid)["loggedIn"].(bool); loggedIn {
		t.Fatal("重启后登出再查 status 应为未登录")
	}
	s3 := newServer("http://127.0.0.1:1", "", "1.2.3.4", "ua", stateFile, false, "", false, 20, time.Minute)
	if s3.sessions.count() != 0 {
		t.Fatalf("登出后再次重启会话数应为 0, got %d", s3.sessions.count())
	}
}

func TestExamReconnectLoopStopsOnReset(t *testing.T) {
	s := newServer("http://127.0.0.1:1", "", "1.2.3.4", "ua", filepath.Join(t.TempDir(), "state.json"), false, "", false, 20, time.Minute)
	sess := s.sessions.newSession()
	sess.set("tok", "u1", "1", "n", time.Now().Add(time.Hour))
	e := sess.exam
	e.mu.Lock()
	e.active = 123
	stopCh, ok := e.scheduleReconnectLocked()
	e.mu.Unlock()
	if !ok {
		t.Fatal("应成功调度重连")
	}
	done := make(chan struct{})
	go func() {
		e.reconnectLoop(stopCh)
		close(done)
	}()
	// Reset 应立即唤醒并退出 3s~60s 退避睡眠，无需等待
	e.Reset()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Reset 后 reconnectLoop 未及时退出")
	}
}
