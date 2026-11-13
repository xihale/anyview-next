package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mock 会话必须与真实 state 文件完全隔离。2026-09-16 事故: mock 登录把
// "mock.xxx" 假 token 落盘进 relay-state.json, 真实模式加载后 injectAuth
// 把它带进取公钥/登录请求, 上游 JWT 校验拒绝, 表现为永久登不上。
func TestMockLoginNeverTouchesStateFile(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")
	s := newServer("http://127.0.0.1:1", "", "1.2.3.4", "ua", stateFile, true, "", false, 20, time.Minute)

	rec := httptest.NewRecorder()
	s.handleLogin(rec, httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader(`{"username":"2026000001","password":"x"}`)))
	if rec.Code != 200 {
		t.Fatalf("mock 登录应 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(stateFile); !os.IsNotExist(err) {
		t.Fatalf("mock 登录不得创建 state 文件: err=%v", err)
	}
	// 登录响应须带 an_sid cookie, 且带着它查 status 是已登录(前端 auth/status 依赖)
	sid := sidFromSetCookie(t, rec)
	if sid == "" {
		t.Fatal("mock 登录应下发 an_sid cookie")
	}
	snap := s.authStatusWith(t, sid)
	if loggedIn, _ := snap["loggedIn"].(bool); !loggedIn {
		t.Fatalf("mock 会话应在内存可见: %v", snap)
	}
}

// 真实模式拒载 mock 假 token(整条弃用, 学生字段也是假的);
// 兜底被旧二进制落盘毒化的会话文件。
func TestRealModeRejectsMockTokenInSessionsFile(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")
	poison := `{"version":2,"sessions":{"sid-x":{"token":"mock.1788941756","username":"2026000001","studentId":"1","studentName":"Mock同学"}}}`
	if err := os.WriteFile(stateFile, []byte(poison), 0o600); err != nil {
		t.Fatal(err)
	}
	ss := newSessionStore(newServer("http://127.0.0.1:1", "", "1.2.3.4", "ua", stateFile, false, "", false, 20, time.Minute), stateFile, true)
	if ss.count() != 0 {
		t.Fatalf("mock 前缀 token 必须整条拒载: %d 条", ss.count())
	}

	real := `{"version":2,"sessions":{"sid-y":{"token":"eyJhbGci.eyJleHA6MTkxNjgzNTIwMH0.sig","username":"u","studentId":"2","studentName":"真同学","exp":1916835200}}}`
	if err := os.WriteFile(stateFile, []byte(real), 0o600); err != nil {
		t.Fatal(err)
	}
	ss = newSessionStore(newServer("http://127.0.0.1:1", "", "1.2.3.4", "ua", stateFile, false, "", false, 20, time.Minute), stateFile, true)
	if ss.count() != 1 {
		t.Fatalf("真实 token 应正常加载: %d 条", ss.count())
	}
	sess := ss.bySID["sid-y"]
	if sess == nil || sess.Token() == "" {
		t.Fatal("加载的会话应持有 token")
	}
}

// 旧版单用户 v1 state 文件无会话句柄, 整文件弃用(升级后各浏览器重新登录一次)
func TestLegacySingleUserStateFileRejected(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")
	legacy := `{"token":"eyJhbGci.eyJleHA6MTkxNjgzNTIwMH0.sig","username":"u","studentId":"2","studentName":"真同学","exp":1916835200}`
	if err := os.WriteFile(stateFile, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	ss := newSessionStore(newServer("http://127.0.0.1:1", "", "1.2.3.4", "ua", stateFile, false, "", false, 20, time.Minute), stateFile, true)
	if ss.count() != 0 {
		t.Fatalf("v1 文件应整体弃用, got %d 条", ss.count())
	}
}

// mock 假 gdb 会话挂在 server 实例上, 两台 server 互不串扰(原包级全局会串)
func TestMockDbgSessionsArePerServer(t *testing.T) {
	a := newServer("http://127.0.0.1:1", "", "1.2.3.4", "ua", filepath.Join(t.TempDir(), "s.json"), true, "", false, 20, time.Minute)
	b := newServer("http://127.0.0.1:1", "", "1.2.3.4", "ua", filepath.Join(t.TempDir(), "s.json"), true, "", false, 20, time.Minute)

	req := func(s *server, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.mockGdb(rec, httptest.NewRequest(http.MethodPost, "/api/gdb/oj/"+path,
			strings.NewReader(`{"eID":"114001","stuCode":"int main(){}"}`)), "debug/start")
		return rec
	}
	if rec := req(a, "debug/start"); rec.Code != 200 {
		t.Fatalf("start 应 200: %d %s", rec.Code, rec.Body.String())
	}
	if len(a.mockDbgs) != 1 || len(b.mockDbgs) != 0 {
		t.Fatalf("会话应只存在于实例 a: a=%d b=%d", len(a.mockDbgs), len(b.mockDbgs))
	}
}

// 回归确认: 真实模式登录会话仍正常落盘(隔离只针对 mock)
func TestRealModeStillPersistsSessions(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")
	s := newServer("http://127.0.0.1:1", "", "1.2.3.4", "ua", stateFile, false, "", false, 20, time.Minute)
	sess := s.sessions.newSession()
	sess.set("tok", "u", "2", "n", time.Now().Add(time.Hour))
	s.sessions.put(sess)
	if _, err := os.Stat(stateFile); err != nil {
		t.Fatalf("真实模式 put 应落盘: %v", err)
	}
	if sess.Token() != "tok" {
		t.Fatalf("token 应可见, got %q", sess.Token())
	}

	// 重启(重载文件)后同一 cookie 仍是同一会话
	s2 := newServer("http://127.0.0.1:1", "", "1.2.3.4", "ua", stateFile, false, "", false, 20, time.Minute)
	resumed := s2.sessions.current(reqWithSID(t, sess.sid))
	if resumed == nil || resumed.Token() != "tok" || resumed.Username() != "u" {
		t.Fatalf("重启后 cookie 应恢复会话: %+v", resumed)
	}
}

// ===== 测试工具 =====

func reqWithSID(t *testing.T, sid string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sid})
	return r
}

func (s *server) authStatusWith(t *testing.T, sid string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handleAuthStatus(rec, reqWithSID(t, sid))
	if rec.Code != 200 {
		t.Fatalf("auth/status 应 200, got %d", rec.Code)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("auth/status 响应解析失败: %v body=%s", err, rec.Body.String())
	}
	return m
}

func sidFromSetCookie(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == sessionCookieName {
			return ck.Value
		}
	}
	return ""
}
