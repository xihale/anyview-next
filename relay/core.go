// 上游传输 + 登录/登出链路。会话状态见 sessions.go(每浏览器独立持有 token)。
// 网关把会话绑定在 XFF 客户端 IP 上: 登录与后续请求必须带同一个 X-Forwarded-For,
// 由 relay 统一注入, 前端永远不用关心。
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type server struct {
	base      *url.URL
	xff, ua   string
	mock      bool
	startedAt time.Time
	sessions  *sessionStore // 每浏览器一份上游 token 状态(an_sid cookie 键控)
	hc        *http.Client
	dialAddr  string
	cache     *cacheStore
	lim       *limiter

	// mock 假 gdb 会话(/api/gdb/* 离线调试), 挂实例上而非包级全局, 测试可并行起多台
	mockDbgMu sync.Mutex
	mockDbgs  map[int64]*mockDbg
}

func newUpstreamTransport(dialAddr string) *http.Transport {
	d := &net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	tr := &http.Transport{
		IdleConnTimeout:       45 * time.Second,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ReadBufferSize:        16 << 10,
		WriteBufferSize:       16 << 10,
	}
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if dialAddr != "" {
			addr = dialAddr
		}
		return d.DialContext(ctx, network, addr)
	}
	return tr
}

func newServer(upstream, dialAddr, xff, ua, stateFile string, mock bool, cacheFile string, cacheOn bool, rpm int, wafCooldown time.Duration) *server {
	u, err := url.Parse(upstream)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		panic(fmt.Sprintf("无效的 -upstream 地址 %q: 需要 http(s)://host[:port]", upstream))
	}
	s := &server{
		base: u, xff: xff, ua: ua, mock: mock, startedAt: time.Now(), dialAddr: dialAddr,
		mockDbgs: map[int64]*mockDbg{},
		hc: &http.Client{
			Timeout:   60 * time.Second,
			Transport: newUpstreamTransport(dialAddr),
			// 默认 Client 会带着 Token 头跟随跨域 302, 上游 WAF 跳到第三方即泄会话
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	// mock 会话仅内存: 永不读写真实 state 文件, 也不让真实 token 混进 mock
	s.sessions = newSessionStore(s, stateFile, !mock)
	s.cache = newCacheStore(cacheFile, cacheOn && !mock)
	if mock {
		rpm = 0 // mock 不碰真实上游, 无需限速
	}
	s.lim = newLimiter(rpm, wafCooldown)
	return s
}

func (s *server) shutdown() {
	if s.sessions != nil {
		s.sessions.shutdown()
	}
	if s.cache != nil {
		s.cache.close()
	}
	if s.hc != nil {
		s.hc.CloseIdleConnections()
	}
}

// doUpstream: 透传请求到上游 /api/<path>, 自动注入会话头。
// tok = 请求所属会话的上游 token(空 = 匿名); 各 handler 从自己的会话取, 不再有全局单份。
func (s *server) doUpstream(ctx context.Context, method, path string, query url.Values, body []byte, contentType, tok string) (int, []byte, error) {
	return s.doUpstreamHdr(ctx, method, path, query, body, contentType, nil, tok)
}

// doUpstreamAnon: 不带存量 Token 的匿名透传, 登录链路(取公钥/登录)专用。
// 网关对 /api/* 全量过 JWT 中间件, 带上残留的假/过期 token 会在登录前就被拒
// (mock 模式落盘的 "mock.xxx" 即触发 "JWT strings must contain exactly 2 period characters")。
func (s *server) doUpstreamAnon(ctx context.Context, method, path string, query url.Values, body []byte, contentType string) (int, []byte, error) {
	return s.doUpstreamHdrAuth(ctx, method, path, query, body, contentType, nil, "")
}

// 透传上限 8MB: 超限返回显式错误而非静默截断(截断的破损 JSON 会毒害前端)
const maxUpstreamBody = 8 << 20

// doUpstreamHdr: 同上, 可附加端点专属头(如 gdb-service 的 Eid)。
func (s *server) doUpstreamHdr(ctx context.Context, method, path string, query url.Values, body []byte, contentType string, extra map[string]string, tok string) (int, []byte, error) {
	return s.doUpstreamHdrAuth(ctx, method, path, query, body, contentType, extra, tok)
}

// doUpstreamHdrAuth: 一切上游 HTTP 请求的唯一漏斗——限速在这里排队(limiter.wait),
// WAF 拦截页在这里探测(limiter.observe)。ctx 传播等待中断: 前端断开即不再打出。
func (s *server) doUpstreamHdrAuth(ctx context.Context, method, path string, query url.Values, body []byte, contentType string, extra map[string]string, tok string) (int, []byte, error) {
	if err := s.lim.wait(ctx); err != nil {
		return 0, nil, err
	}
	u := *s.base
	u.Path = "/api/" + strings.TrimPrefix(path, "/")
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	s.injectAuth(req, tok)
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := s.hc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := readLimited(resp.Body, resp.ContentLength, maxUpstreamBody)
	s.lim.observe(resp.StatusCode, data)
	if err == nil && len(data) > maxUpstreamBody {
		return resp.StatusCode, nil, fmt.Errorf("上游响应超过 %dMB 上限", maxUpstreamBody>>20)
	}
	return resp.StatusCode, data, err
}

func readLimited(r io.Reader, contentLen int64, limit int) ([]byte, error) {
	lr := io.LimitReader(r, int64(limit)+1)
	if contentLen > 0 {
		capHint := int(contentLen)
		if capHint > limit {
			capHint = limit + 1
		}
		var buf bytes.Buffer
		buf.Grow(capHint)
		_, err := buf.ReadFrom(lr)
		return buf.Bytes(), err
	}
	return io.ReadAll(lr)
}

func (s *server) injectAuth(req *http.Request, tok string) {
	req.Header.Set("User-Agent", s.ua)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("X-Forwarded-For", s.xff)
	if tok != "" {
		req.Header.Set("Token", tok)
	}
}

// mockTokenPrefix: mock 会话 token 的哨兵前缀。mock 会话只存内存,
// 真实模式加载会话文件时仍拒载该前缀——兜底被旧二进制落盘毒化的文件
// (2026-09-16: 假 token 被 injectAuth 带进登录链路, 上游 JWT 校验拒绝, 永久登不上)。
const mockTokenPrefix = "mock."

func isMockToken(tok string) bool { return strings.HasPrefix(tok, mockTokenPrefix) }

// ===== RSA 登录(与 fuckView tools/origin/login2.ts 同款流程) =====

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil || req.Username == "" || req.Password == "" {
		fail(w, 400, "需要 username/password")
		return
	}

	if s.mock {
		// 哨兵前缀: 会话仅内存(persist=false)本就不落盘, 前缀仅防御旧文件/显式诊断
		sess := s.sessions.ensure(r)
		sess.set(mockTokenPrefix+fmt.Sprint(time.Now().Unix()), req.Username, "1", "Mock同学", time.Now().Add(12*time.Hour))
		sess.exam.Reset()
		bindSessionCookie(w, sess.sid)
		writeJSON(w, 200, map[string]any{"ok": true, "student": map[string]any{"id": "1", "name": "Mock同学"}})
		return
	}

	// 1. 取 RSA 公钥(base64 DER SPKI)—— 匿名端点, 不得携带存量 token
	rsaStatus, body, err := s.doUpstreamAnon(r.Context(), "GET", "/user-service/rsa", nil, nil, "")
	if err != nil {
		failUpstream(w, err)
		return
	}
	if wafHit(rsaStatus, body) {
		rem, _ := s.lim.cooling() // 漏斗里 observe 已同步置位, 这里只取剩余时长
		failCooldown(w, rem)      // rsa 响应是 WAF 拦截页, 不再硬解公钥
		return
	}
	var rsaResp struct {
		Code int    `json:"code"`
		Data string `json:"data"`
	}
	if json.Unmarshal(body, &rsaResp) != nil || rsaResp.Code != 200 || rsaResp.Data == "" {
		fail(w, 502, "取公钥失败: "+truncate(string(body), 200))
		return
	}
	der, err := base64.StdEncoding.DecodeString(rsaResp.Data)
	if err != nil {
		fail(w, 500, "公钥解码失败: "+err.Error())
		return
	}
	pubAny, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		fail(w, 500, "公钥解析失败: "+err.Error())
		return
	}
	pub, ok := pubAny.(*rsa.PublicKey)
	if !ok {
		fail(w, 500, "不是 RSA 公钥")
		return
	}
	// 2. 明文格式 `${password},${时间戳毫秒}` → PKCS1 → base64
	enc, err := rsa.EncryptPKCS1v15(rand.Reader, pub,
		[]byte(req.Password+","+fmt.Sprint(time.Now().UnixMilli())))
	if err != nil {
		fail(w, 500, "加密失败: "+err.Error())
		return
	}

	// 3. multipart 登录(roleId=3 学生, schoolId=63 广工)
	ct, bodyBytes, err := encodeMultipart([][2]string{
		{"username", req.Username},
		{"password", base64.StdEncoding.EncodeToString(enc)},
		{"roleId", "3"},
		{"schoolId", "63"},
	})
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	status, body, err := s.doUpstreamAnon(r.Context(), "POST", "/user-service/login", nil, bodyBytes, ct)
	if err != nil {
		failUpstream(w, err)
		return
	}
	if wafHit(status, body) {
		rem, _ := s.lim.cooling()
		failCooldown(w, rem)
		return
	}
	var loginResp struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Token   string `json:"token"`
			Student struct {
				ID   any    `json:"id"`
				Name string `json:"name"`
			} `json:"student"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &loginResp) != nil || loginResp.Code != 200 || loginResp.Data.Token == "" {
		// 上游业务失败常为 HTTP 200+信封错误码(如密码错误); 映射为 401,
		// 否则前端拿到 HTTP 200 会误判登录成功(旧实现在此处回 HTTP 200+code:200)
		httpStatus := status
		if httpStatus < 400 {
			httpStatus = 401
		}
		fail(w, httpStatus, "登录失败: "+truncate(string(body), 200))
		return
	}
	exp := jwtExp(loginResp.Data.Token)
	sid := fmt.Sprint(loginResp.Data.Student.ID)
	// 会话归属: 复用该浏览器已有会话(过期重登/换号), 没有才新建——
	// 其他浏览器的会话不受影响, 这是多账号并发的关键
	sess := s.sessions.ensure(r)
	sess.set(loginResp.Data.Token, req.Username, sid, loginResp.Data.Student.Name, exp)
	s.sessions.save() // 复用已有会话(过期重登/换号)时 ensure 不会落盘, 必须显式刷新状态文件
	sess.exam.Reset() // 清掉该浏览器上一身份的 kicked/计时残留
	// 磁盘卫生: 个人缓存(catalog/存档码)只保留「仍有在册会话」的账号——
	// 离场账号的清掉, 在场账号(别的浏览器还登着)的不动; 共享题面/模板不受影响
	if purged := s.cache.retainOwners(s.sessions.liveOwners()); purged > 0 {
		log.Printf("[cache] 登录 %s: 清除离场账号个人缓存 %d 条", req.Username, purged)
	}
	bindSessionCookie(w, sess.sid)
	writeJSON(w, 200, map[string]any{
		"ok":      true,
		"student": map[string]any{"id": sid, "name": loginResp.Data.Student.Name},
		"exp":     exp.Unix(),
	})
}

func (s *server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	if sess := s.sessions.current(r); sess != nil {
		writeJSON(w, 200, sess.snapshot())
		return
	}
	writeJSON(w, 200, anonymousSnapshot())
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// 只登出当前浏览器这一份会话: 停它的计时 + 断它的 exam WS + 摘出存储
	if sess := s.sessions.current(r); sess != nil {
		sess.exam.Reset()
		s.sessions.delete(sess)
	}
	clearSessionCookie(w)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// jwtExp: 解 JWT payload 的 exp(失败则默认 12h)
func jwtExp(tok string) time.Time {
	parts := strings.Split(tok, ".")
	if len(parts) >= 2 {
		if b, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
			var p struct {
				Exp int64 `json:"exp"`
			}
			if json.Unmarshal(b, &p) == nil && p.Exp > 0 {
				return time.Unix(p.Exp, 0)
			}
		}
	}
	return time.Now().Add(12 * time.Hour)
}

// ===== multipart 工具 =====

func encodeMultipart(fields [][2]string) (contentType string, body []byte, err error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, f := range fields {
		if err := w.WriteField(f[0], f[1]); err != nil {
			return "", nil, err
		}
	}
	if err := w.Close(); err != nil {
		return "", nil, err
	}
	return w.FormDataContentType(), buf.Bytes(), nil
}

// truncate: 字节上限截断, 回退到 UTF-8 字符边界(避免错误信息里出现撕裂的半个汉字)
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
