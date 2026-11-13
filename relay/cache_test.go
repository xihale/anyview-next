package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCacheableGet(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
		always       bool
		netFirst     bool
	}{
		{"GET", "/scheme-service/scheme/list/student/123", true, true, false},
		{"GET", "/scheme-service/scheme/getQuestionTemplate/student/9", true, false, false},
		{"GET", "/scheme-service/catalog/list", true, false, true},
		{"POST", "/scheme-service/scheme/list/student/123", false, false, false},
		{"GET", "/gdb-service/code/compile", false, false, false},
		{"GET", "/user-service/rsa", false, false, false},
	}
	for _, c := range cases {
		p, ok := cacheableGet(c.method, c.path)
		if ok != c.want || (ok && p.always != c.always) || (ok && p.netFirst != c.netFirst) {
			t.Fatalf("%s %s → ok=%v always=%v netFirst=%v, 期望 ok=%v always=%v netFirst=%v",
				c.method, c.path, ok, p.always, p.netFirst, c.want, c.always, c.netFirst)
		}
	}
}

func TestEnvelopeOK(t *testing.T) {
	// 成功码全集 [200,0] + 废弃兼容 [31,41,70](fuckView analysis/08)
	for _, code := range []string{"200", "0", "31", "41", "70"} {
		if !envelopeOK([]byte(`{"code":` + code + `,"data":{}}`)) {
			t.Fatalf("code:%s 应通过", code)
		}
	}
	for _, b := range []string{`{"code":401,"message":"未登录"}`, `{"code":1}`, `{"data":{}}`, `not json`, ``} {
		if envelopeOK([]byte(b)) {
			t.Fatalf("%q 不应通过", b)
		}
	}
}

// cacheHandlerTest: httptest 假上游 + 内存缓存池的 relay。
// 会话: user(name) 按用户名取/建带 cookie 的会话; get() 默认以 u1 发请求。
type cacheHandlerTest struct {
	s        *server
	upstream *httptest.Server
	users    map[string]*session
	scheme   atomic.Int64 // scheme/list 上游命中数
	tmpl     atomic.Int64
	catalog  atomic.Int64 // catalog/list 上游命中数
	stuSeq   atomic.Int64 // 每次上游响应给出不同的 stuCode, 模拟外部改动
}

func newCacheHandlerTest(t *testing.T) *cacheHandlerTest {
	t.Helper()
	h := &cacheHandlerTest{users: map[string]*session{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/scheme-service/catalog/list", func(w http.ResponseWriter, r *http.Request) {
		h.catalog.Add(1)
		fmt.Fprint(w, `{"code":200,"message":"","data":[{"eid":1,"pass":false},{"eid":2,"pass":true}]}`)
	})
	mux.HandleFunc("/api/scheme-service/scheme/list/student/", func(w http.ResponseWriter, r *http.Request) {
		h.scheme.Add(1)
		seq := h.stuSeq.Add(1)
		// 生产实测该端点成功回旧成功码 31(fuckView analysis/08), fixture 按真形状:
		// envelopeOK 只认 200 的话题面永远进不了缓存池(2026-09-16 线上实锤)
		fmt.Fprintf(w, `{"code":31,"message":"","data":{"doc":"<p>题面</p>","stuCode":"stu-sequence-%s"}}`,
			strconv.FormatInt(seq, 10))
	})
	mux.HandleFunc("/api/scheme-service/scheme/getQuestionTemplate/student/", func(w http.ResponseWriter, r *http.Request) {
		h.tmpl.Add(1)
		fmt.Fprint(w, `{"code":200,"message":"","data":"template-code"}`)
	})
	h.upstream = httptest.NewServer(mux)
	t.Cleanup(h.upstream.Close)
	h.s = newServer(h.upstream.URL, "", "1.2.3.4", "ua", filepath.Join(t.TempDir(), "state.json"), false, "", true, 0, 0)
	return h
}

// user: 按用户名取/建一个已登录会话(真实入册, cookie 流程与生产一致)
func (h *cacheHandlerTest) user(t *testing.T, name string) *session {
	t.Helper()
	if sess := h.users[name]; sess != nil {
		return sess
	}
	sess := h.s.sessions.newSession()
	sess.set("tok-"+name, name, "1", name, time.Now().Add(time.Hour))
	h.s.sessions.put(sess)
	h.users[name] = sess
	return sess
}

func (h *cacheHandlerTest) req(t *testing.T, sess *session, url string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, url, nil)
	if sess != nil {
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.sid})
	}
	h.s.handleProxy(rec, r)
	return rec
}

func (h *cacheHandlerTest) get(t *testing.T, url string) *httptest.ResponseRecorder {
	t.Helper()
	return h.req(t, h.user(t, "u1"), url)
}

// getAs: 以指定用户会话发请求(默认 get 是 u1)
func (h *cacheHandlerTest) getAs(t *testing.T, name, url string) *httptest.ResponseRecorder {
	t.Helper()
	return h.req(t, h.user(t, name), url)
}

// ageEntry: 直接把条目的 checkedAt 拨老, 绕过 2s 防重间隔(测试不等真实时钟)
func (h *cacheHandlerTest) ageEntry(t *testing.T, key string, d time.Duration) {
	t.Helper()
	h.s.cache.mu.Lock()
	if e := h.s.cache.entries[key]; e != nil {
		e.checkedAt = e.checkedAt.Add(-d)
	}
	h.s.cache.mu.Unlock()
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("2s 内未等到条件成立")
}

func TestProxyCacheColdHitRevalidate(t *testing.T) {
	h := newCacheHandlerTest(t)
	url := "/api/up/scheme-service/scheme/list/student/77"

	// 冷: 打上游, 无 stale 标记
	rec := h.get(t, url)
	if rec.Code != 200 || h.scheme.Load() != 1 {
		t.Fatalf("冷路径应打上游: code=%d hits=%d", rec.Code, h.scheme.Load())
	}
	if rec.Header().Get("X-Relay-Stale") != "" {
		t.Fatal("冷路径不应贴 stale 标记")
	}
	if !strings.Contains(rec.Body.String(), "stu-sequence-1") {
		t.Fatalf("冷响应内容不符: %s", rec.Body.String())
	}

	// 热命中: 秒回缓存不打上游; 防重间隔内不触发校验 → 无标记
	if rec = h.get(t, url); h.scheme.Load() != 1 {
		t.Fatalf("2s 防重间隔内的热命中不应再打上游, hits=%d", h.scheme.Load())
	}
	if rec.Header().Get("X-Relay-Stale") != "" {
		t.Fatal("未触发校验的热命中不应贴 stale 标记")
	}

	// 拨老后: 回缓存+贴标记+后台校验(上游内容已变化)
	h.ageEntry(t, "/scheme-service/scheme/list/student/77", 10*time.Second)
	rec = h.get(t, url)
	if rec.Header().Get("X-Relay-Stale") != "1" {
		t.Fatal("触发校验的热命中应贴 stale 标记")
	}
	if !strings.Contains(rec.Body.String(), "stu-sequence-1") {
		t.Fatal("本次仍应回旧缓存")
	}
	waitFor(t, func() bool { return h.scheme.Load() == 2 })

	// 校验完成后: 新值已进池, 再取拿新值且不再贴标记
	rec = h.get(t, url)
	if !strings.Contains(rec.Body.String(), "stu-sequence-2") {
		t.Fatalf("校验后应取到新值: %s", rec.Body.String())
	}
	if rec.Header().Get("X-Relay-Stale") != "" {
		t.Fatal("新值已到手不应再贴标记(客户端补拉会停)")
	}
}

func TestProxyCacheFreshTTLTemplate(t *testing.T) {
	h := newCacheHandlerTest(t)
	url := "/api/up/scheme-service/scheme/getQuestionTemplate/student/9"

	h.get(t, url)
	if h.tmpl.Load() != 1 {
		t.Fatal("冷路径应打上游")
	}
	if rec := h.get(t, url); h.tmpl.Load() != 1 || rec.Header().Get("X-Relay-Stale") != "" {
		t.Fatalf("fresh 期内命中缓存且不校验: hits=%d", h.tmpl.Load())
	}
	h.ageEntry(t, "/scheme-service/scheme/getQuestionTemplate/student/9", cacheFreshTTL+time.Hour)
	rec := h.get(t, url)
	if rec.Header().Get("X-Relay-Stale") != "1" {
		t.Fatal("过期后应先回缓存并触发校验")
	}
	waitFor(t, func() bool { return h.tmpl.Load() == 2 })
}

func TestProxyCacheUpstreamDownServesStale(t *testing.T) {
	h := newCacheHandlerTest(t)
	url := "/api/up/scheme-service/scheme/getQuestionTemplate/student/9"
	h.get(t, url)
	h.upstream.Close() // 上游挂了

	h.ageEntry(t, "/scheme-service/scheme/getQuestionTemplate/student/9", cacheFreshTTL+time.Hour)
	rec := h.get(t, url)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "template-code") {
		t.Fatalf("上游挂了应回过期缓存: code=%d body=%s", rec.Code, rec.Body.String())
	}
	// 校验失败进退避: 退避期内继续回缓存, 不追着死上游打
	if rec.Header().Get("X-Relay-Stale") == "" {
		t.Fatal("校验在途/触发过, 应贴标记")
	}
	// 等异步校验失败把 failAt 写进退避期, 再验证退避内不追打
	tkey := "/scheme-service/scheme/getQuestionTemplate/student/9"
	waitFor(t, func() bool {
		h.s.cache.mu.Lock()
		defer h.s.cache.mu.Unlock()
		return !h.s.cache.entries[tkey].failAt.IsZero()
	})
	h.ageEntry(t, tkey, cacheFreshTTL+time.Hour)
	rec = h.get(t, url)
	if rec.Header().Get("X-Relay-Stale") != "" {
		t.Fatal("退避期内不应再触发校验(死上游会被追打)")
	}
}

// TestProxyCatalogNetFirst: catalog 响应带 pass/accumTime 等随做题变化的字段,
// 网络优先——有缓存也打上游; 上游不可达/5xx 才回缓存兜底(题库断网可用)。
func TestProxyCatalogNetFirst(t *testing.T) {
	h := newCacheHandlerTest(t)
	q := url.Values{"schemeId": []string{"1"}, "classId": []string{"2"}}
	key := cacheKey("/scheme-service/catalog/list", q)
	u := "/api/up/scheme-service/catalog/list?schemeId=1&classId=2"

	// 缓存里有旧条目也必须打上游(不吃陈值)
	h.s.cache.putFetch(key, []byte(`{"code":200,"data":[{"eid":1,"pass":false}]}`), "u1")
	rec := h.get(t, u)
	if h.catalog.Load() != 1 {
		t.Fatalf("catalog 应网络优先打上游, hits=%d", h.catalog.Load())
	}
	if !strings.Contains(rec.Body.String(), `"pass":true`) {
		t.Fatalf("应回上游新值而非缓存旧值: %s", rec.Body.String())
	}
	// 成功响应顺带刷新缓存
	if got := string(h.s.cache.get(key, "u1").body); !strings.Contains(got, `"pass":true`) {
		t.Fatalf("成功后缓存应已刷新: %s", got)
	}

	// 上游挂掉: 回缓存兜底, 不白屏
	h.upstream.Close()
	rec = h.get(t, u)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"pass":true`) {
		t.Fatalf("上游挂了应回缓存兜底: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Relay-Stale") != "1" {
		t.Fatal("兜底响应应贴 stale 标记")
	}
}

// TestProxyCatalogQueryIsolation: 不同 scheme/class 的 catalog 是不同条目
func TestProxyCatalogQueryIsolation(t *testing.T) {
	h := newCacheHandlerTest(t)
	q := url.Values{"schemeId": []string{"1"}, "classId": []string{"2"}}
	h.s.cache.putFetch(cacheKey("/scheme-service/catalog/list", q), []byte(`{"code":200,"data":[{"eid":1}]}`), "u1")
	// 上游挂死, 只能吃 scheme=1/class=2 的兜底
	h.upstream.Close()
	rec := h.get(t, "/api/up/scheme-service/catalog/list?schemeId=1&classId=2")
	if !strings.Contains(rec.Body.String(), `"eid":1`) {
		t.Fatalf("同 query 应命中兜底缓存: %s", rec.Body.String())
	}
	// 不同 query 没有缓存条目: 上游又挂着, 只能报错, 绝不串缓存
	rec = h.get(t, "/api/up/scheme-service/catalog/list?schemeId=3&classId=4")
	if strings.Contains(rec.Body.String(), `"eid":1`) {
		t.Fatal("不同 query 不得串缓存")
	}
}

func TestCacheWriteThroughCode(t *testing.T) {
	h := newCacheHandlerTest(t)
	key := "/scheme-service/scheme/list/student/5"
	h.s.cache.putFetch(key, []byte(`{"code":200,"data":{"doc":"d","stuCode":"b2xk"}}`), "u1")

	// 共享体已剥离个人码, u1 命中拼回自己的
	if body := string(h.s.cache.get(key, "u1").body); !strings.Contains(body, `"stuCode":"b2xk"`) {
		t.Fatalf("u1 命中应拼回自己的存档码: %s", body)
	}

	if !h.s.cache.writeThroughCode(5, "bmV3", "u1") {
		t.Fatal("代码不同, 应写穿成功")
	}
	if body := string(h.s.cache.get(key, "u1").body); !strings.Contains(body, `"stuCode":"bmV3"`) || strings.Contains(body, "b2xk") {
		t.Fatalf("写穿后 u1 命中应为新代码: %s", body)
	}
	// 相同代码重复写穿 → no-op
	if h.s.cache.writeThroughCode(5, "bmV3", "u1") {
		t.Fatal("代码没变应返回 false")
	}
	// 另一账号写自己的, 互不影响
	h.s.cache.writeThroughCode(5, "dTJfY29kZQ==", "u2")
	if body := string(h.s.cache.get(key, "u2").body); !strings.Contains(body, "dTJfY29kZQ==") {
		t.Fatalf("u2 应读自己的代码: %s", body)
	}
	if body := string(h.s.cache.get(key, "u1").body); strings.Contains(body, "dTJfY29kZQ==") {
		t.Fatalf("u1 不得读到 u2 的代码: %s", body)
	}
}

func TestProxySaveWritesThrough(t *testing.T) {
	h := newCacheHandlerTest(t)
	h.user(t, "user")
	key := "/scheme-service/scheme/list/student/5"
	h.s.cache.putFetch(key, []byte(`{"code":200,"data":{"doc":"d","stuCode":"b2xk"}}`), "user")
	// handleSave 成功后的写穿(与 work.go 调用同一入口)
	h.s.cache.writeThroughCode(5, "c2F2ZWQ=", "user")
	rec := h.getAs(t, "user", "/api/up/scheme-service/scheme/list/student/5")
	if !strings.Contains(rec.Body.String(), "c2F2ZWQ=") {
		t.Fatalf("save 后切回该题, 缓存应已是新代码: %s", rec.Body.String())
	}
	if h.scheme.Load() != 0 {
		t.Fatalf("写穿不应打上游, hits=%d", h.scheme.Load())
	}
}

// 缓存公共/个人分储: 题面/模板人人相同跨账号共享, 两个在线账号都照常命中;
// 存档码个人另存——u2 命中共享题面但拼回的是 u2 自己的码, 绝不见到 u1 的;
// catalog 是个人条目, 按会话隔离, u1/u2 各拉各的
func TestCacheSharedAcrossAccounts(t *testing.T) {
	h := newCacheHandlerTest(t)
	h.user(t, "u1")
	sUrl := "/api/up/scheme-service/scheme/list/student/77"
	tUrl := "/api/up/scheme-service/scheme/getQuestionTemplate/student/9"

	h.getAs(t, "u1", sUrl)
	h.getAs(t, "u1", tUrl)
	if e := h.s.cache.get("/scheme-service/scheme/list/student/77", "u1"); e == nil {
		t.Fatal("u1 应命中题面条目")
	}

	// u2 也登录(并发会话): 共享条目原样命中, 不打上游
	h.user(t, "u2")
	before := h.scheme.Load()
	beforeT := h.tmpl.Load()
	rec := h.getAs(t, "u2", sUrl)
	if h.scheme.Load() != before {
		t.Fatalf("题面共享, u2 不应冷拉: hits=%d→%d", before, h.scheme.Load())
	}
	if !strings.Contains(rec.Body.String(), "<p>题面</p>") {
		t.Fatalf("u2 应命中共享题面: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "stu-sequence") {
		t.Fatalf("u1 的存档码不得出现在 u2 的响应里: %s", rec.Body.String())
	}
	rec = h.getAs(t, "u2", tUrl)
	if h.tmpl.Load() != beforeT {
		t.Fatal("模板共享, u2 不应冷拉")
	}
	if !strings.Contains(rec.Body.String(), "template-code") {
		t.Fatalf("u2 应命中共享模板: %s", rec.Body.String())
	}

	// u2 首见该题(无本人存档码记录): 秒回共享题面的同时触发后台校验,
	// 拉回 u2 自己的云端代码并记到 u2 名下
	waitFor(t, func() bool { return h.scheme.Load() == before+1 })
	seq := h.stuSeq.Load()
	rec = h.getAs(t, "u2", sUrl)
	if !strings.Contains(rec.Body.String(), fmt.Sprintf("stu-sequence-%d", seq)) {
		t.Fatalf("u2 命中应拼回自己的存档码: %s", rec.Body.String())
	}

	// catalog 是个人条目: u1 拉过后 u2 读不到 u1 的, 必须自己冷拉
	h.getAs(t, "u1", "/api/up/scheme-service/catalog/list?schemeId=1&classId=2")
	before = h.catalog.Load()
	rec = h.getAs(t, "u2", "/api/up/scheme-service/catalog/list?schemeId=1&classId=2")
	if h.catalog.Load() != before+1 {
		t.Fatalf("catalog 个人条目对其他会话不可见, u2 应冷拉: hits=%d→%d", before, h.catalog.Load())
	}
}

// TestRetainOwnersKeepsLive: 磁盘卫生只清「已无在册会话」账号的个人数据;
// 仍在线账号(其他浏览器)的个人条目/存档码与公共题面都保留
func TestRetainOwnersKeepsLive(t *testing.T) {
	c := newCacheStore("", true)
	c.putFetch("/scheme-service/scheme/list/student/1", []byte(`{"code":200,"data":{}}`), "u1") // 共享
	c.putFetch("/scheme-service/catalog/list", []byte(`{"code":200,"data":[]}`), "u1")          // 个人
	c.putFetch("/scheme-service/catalog/list?x=1", []byte(`{"code":200,"data":[]}`), "u2")      // 个人
	c.setStuLocked("u1", "/k", "YQ==")
	c.setStuLocked("u2", "/k", "Yg==")

	if n := c.retainOwners(map[string]bool{"u2": true}); n != 2 {
		t.Fatalf("应清掉 u1 的 1 条个人条目 + 1 份存档码, got %d", n)
	}
	if c.get("/scheme-service/scheme/list/student/1", "u2") == nil {
		t.Fatal("公共题面条目应保留共享")
	}
	if c.get("/scheme-service/catalog/list", "u2") != nil {
		t.Fatal("离场账号 u1 的个人条目应被清除")
	}
	if c.get("/scheme-service/catalog/list?x=1", "u2") == nil {
		t.Fatal("在线账号 u2 的个人条目应保留")
	}
	c.mu.Lock()
	_, hasU1 := c.stu["u1"]
	_, hasU2 := c.stu["u2"]
	c.mu.Unlock()
	if hasU1 || !hasU2 {
		t.Fatalf("存档码应只清离场账号: u1=%v u2=%v", hasU1, hasU2)
	}
}

// 后台校验凭触发时会话的 (token, owner) 原子配对: 内容归属结构上不串号。
// 会话凭据被清空(登出/未登录)则不校验不入池
func TestRevalidateUsesCredSnapshot(t *testing.T) {
	h := newCacheHandlerTest(t)
	u1 := h.user(t, "u1")
	key := "/scheme-service/scheme/list/student/77"
	h.s.revalidateEntry(key, "/scheme-service/scheme/list/student/77", url.Values{}, u1)
	if e := h.s.cache.get(key, "u1"); e == nil {
		t.Fatal("校验内容应记到触发会话的账号名下")
	}

	// 会话登出(凭据清空): 不打上游不入池
	h2 := newCacheHandlerTest(t)
	logout := h2.user(t, "u9")
	logout.set("", "", "", "", time.Time{})
	h2.s.revalidateEntry(key, "/scheme-service/scheme/list/student/77", url.Values{}, logout)
	if e := h2.s.cache.get(key, "u9"); e != nil {
		t.Fatal("登出会话不得触发校验或入池")
	}
}

// v1/v2 缓存文件结构与 v3 不同(无公共/个人分储), 整文件弃用
func TestCacheOldVersionFileRejected(t *testing.T) {
	for _, ver := range []string{"1", "2"} {
		file := filepath.Join(t.TempDir(), "cache.json")
		old := `{"version":` + ver + `,"savedAt":1,"entries":{"/x":{"body":"{\"code\":200}","fetchedAt":100}}}`
		if err := os.WriteFile(file, []byte(old), 0o600); err != nil {
			t.Fatal(err)
		}
		c := newCacheStore(file, true)
		if len(c.entries) != 0 {
			t.Fatalf("v%s 文件应整体弃用, got %d 条", ver, len(c.entries))
		}
	}
}

func TestCacheMarkPassed(t *testing.T) {
	c := newCacheStore("", true)
	k1 := "/scheme-service/catalog/list?schemeId=1&classId=2"
	k2 := "/scheme-service/catalog/list?schemeId=3&classId=4"
	c.putFetch(k1, []byte(`{"code":200,"message":"ok","data":[{"eid":1,"pass":true},{"eid":2,"pass":false},{"eid":3,"pname":"DC01PE03"}]}`), "u1")
	c.putFetch(k2, []byte(`{"code":200,"data":[{"eid":2,"pass":false}]}`), "u1")
	c.putFetch("/scheme-service/scheme/list/student/2", []byte(`{"code":200,"data":{"stuCode":"x"}}`), "u1")

	c.markPassed(2, "u1")

	for _, k := range []string{k1, k2} {
		var env struct {
			Data []struct {
				EID  int64 `json:"eid"`
				Pass bool  `json:"pass"`
			} `json:"data"`
		}
		if err := json.Unmarshal(c.get(k, "u1").body, &env); err != nil {
			t.Fatalf("%s 解析失败: %v", k, err)
		}
		for _, it := range env.Data {
			if it.EID == 2 && !it.Pass {
				t.Fatalf("%s 中 eid=2 应写穿为 pass=true: %s", k, c.get(k, "u1").body)
			}
		}
	}
	if e := c.get("/scheme-service/scheme/list/student/2", "u1"); string(e.body) != `{"code":200,"data":{"stuCode":"x"}}` {
		t.Fatalf("非 catalog 条目不应被改动: %s", e.body)
	}
}

func TestCacheStoreWipe(t *testing.T) {
	h := newCacheHandlerTest(t)
	h.s.cache.putFetch("/scheme-service/scheme/list/student/1", []byte(`{"code":200,"data":{}}`), "u1")
	h.s.cache.putFetch("/scheme-service/catalog/list", []byte(`{"code":200,"data":[]}`), "u1")
	h.s.cache.wipe()
	if h.s.cache.get("/scheme-service/scheme/list/student/1", "u1") != nil || h.s.cache.get("/scheme-service/catalog/list", "u1") != nil {
		t.Fatal("wipe 后应全空")
	}
}

func TestCachePersistRoundtrip(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "cache.json")
	c := newCacheStore(file, true)
	c.putFetch("/scheme-service/scheme/list/student/3", []byte(`{"code":200,"data":{"stuCode":"abc"}}`), "u1")
	c.putFetch("/scheme-service/scheme/getQuestionTemplate/student/3", []byte(`{"code":200,"data":"tpl"}`), "u1")
	c.markFail("/scheme-service/scheme/getQuestionTemplate/student/3", "u1")
	c.flush() // 防抖改为同步等待验证

	c2 := newCacheStore(file, true)
	// mixed 条目落盘为剥离态, 命中时拼回本人存档码
	e := c2.get("/scheme-service/scheme/list/student/3", "u1")
	if e == nil || !strings.Contains(string(e.body), `"stuCode":"abc"`) {
		t.Fatalf("scheme 条目回读+拼回不符: %+v", e)
	}
	c2.mu.Lock()
	stu := c2.stu["u1"]["/scheme-service/scheme/list/student/3"]
	c2.mu.Unlock()
	if stu != "abc" {
		t.Fatalf("个人存档码应随盘回读: %q", stu)
	}
	tpl := c2.get("/scheme-service/scheme/getQuestionTemplate/student/3", "u1")
	if tpl == nil || time.Since(tpl.failAt) > time.Minute {
		t.Fatalf("template 条目/失败时间回读不符: %+v", tpl)
	}

	// 清空落盘 → 文件删除
	c2.wipe()
	c2.flush()
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("清空后缓存文件应被删除")
	}
}

func TestCachePersistCorruptIgnored(t *testing.T) {
	file := filepath.Join(t.TempDir(), "cache.json")
	if err := os.WriteFile(file, []byte("不是 json"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := newCacheStore(file, true)
	if len(c.entries) != 0 {
		t.Fatal("损坏文件应被忽略")
	}
}

func TestCacheEvictionCap(t *testing.T) {
	c := newCacheStore("", true)
	for i := range cacheMaxEntries + 50 {
		key := fmt.Sprintf("/scheme-service/scheme/getQuestionTemplate/student/%d", i)
		c.putFetch(key, []byte(fmt.Sprintf(`{"code":200,"data":"tpl-%d"}`, i)), "u1")
	}
	c.mu.Lock()
	n := len(c.entries)
	oldestKept := c.entries["/scheme-service/scheme/getQuestionTemplate/student/0"] != nil
	newestKept := c.entries[fmt.Sprintf("/scheme-service/scheme/getQuestionTemplate/student/%d", cacheMaxEntries+49)] != nil
	c.mu.Unlock()
	if n != cacheMaxEntries {
		t.Fatalf("应淘汰到上限 %d, 实际 %d", cacheMaxEntries, n)
	}
	if oldestKept || !newestKept {
		t.Fatalf("应按 fetchedAt 淘汰最旧: oldestKept=%v newestKept=%v", oldestKept, newestKept)
	}
}

func TestCacheDisabledNoop(t *testing.T) {
	h := newCacheHandlerTest(t)
	h.s.cache.enabled = false
	url := "/api/up/scheme-service/scheme/getQuestionTemplate/student/9"
	h.get(t, url)
	h.get(t, url)
	if h.tmpl.Load() != 2 {
		t.Fatalf("关闭缓存应每次打上游, hits=%d", h.tmpl.Load())
	}
	if h.s.cache.get("/scheme-service/scheme/getQuestionTemplate/student/9", "u1") != nil {
		t.Fatal("关闭缓存不应落池")
	}
}

func TestCacheCloseFlushesDirtyImmediately(t *testing.T) {
	file := filepath.Join(t.TempDir(), "cache.json")
	c := newCacheStore(file, true)
	c.putFetch("/scheme-service/scheme/list/student/88", []byte(`{"code":200,"data":{"doc":"<p>hi</p>","stuCode":"Y29kZQ=="}}`), "u1")
	// 不等 2s 防抖，直接 close() 应同步落盘
	c.close()

	c2 := newCacheStore(file, true)
	e := c2.get("/scheme-service/scheme/list/student/88", "u1")
	if e == nil || !strings.Contains(string(e.body), `"stuCode":"Y29kZQ=="`) {
		t.Fatalf("close() 后重启应能回读完整缓存: %+v", e)
	}
}

func TestCacheConcurrentMutateAndFlush(t *testing.T) {
	file := filepath.Join(t.TempDir(), "cache.json")
	c := newCacheStore(file, true)
	done := make(chan struct{})
	for g := 0; g < 4; g++ {
		go func(id int) {
			user := fmt.Sprintf("u%d", id)
			for i := 0; i < 40; i++ {
				key := fmt.Sprintf("/scheme-service/scheme/list/student/%d", i%10)
				c.putFetch(key, []byte(fmt.Sprintf(`{"code":200,"data":{"doc":"<p>%d</p>","stuCode":"code-%d"}}`, i, id)), user)
				c.writeThroughCode(int64(i%10), fmt.Sprintf("wt-%d-%d", id, i), user)
				_ = c.get(key, user)
			}
			done <- struct{}{}
		}(g)
	}
	go func() {
		for i := 0; i < 20; i++ {
			c.flush()
			time.Sleep(time.Millisecond)
		}
		done <- struct{}{}
	}()
	for i := 0; i < 5; i++ {
		<-done
	}
	c.close()
}
