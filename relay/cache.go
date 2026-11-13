// 缓存池: GET /api/up/scheme-service/* 透传加一层缓存。
// 三件事: 上游慢/挂时不白屏(题库断网可用), 切题零上游延迟, 上游请求量可控(WAF 友好)。
//
// 策略(前端只发这三个读端点, 其余路径不碰):
//
//	scheme/list/student/*  混合条目: 题面 body 剥离 stuCode 后跨账号共享(题目人人相同),
//	                       个人存档码按账号另存、命中时拼回; 每次访问都后台校验追新,
//	                       stuCode 靠「秒回 + X-Relay-Stale + 客户端补拉」追到秒级新鲜
//	getQuestionTemplate/*  公共条目: fresh 30 天(初始模板人人相同)
//	catalog/list           个人条目(按账号隔离): 网络优先——响应里的 pass/accumTime
//	                       随做题变化, 吃缓存陈值会把刚过的题显示回未通过(2026-09-15
//	                       实报); 缓存只在上游不可达/5xx 时兜底, 成功即刷新缓存
//
// 一致性:
//   - save/compile 成功即写穿该账号的存档码(上游存档此刻已被覆盖, 见 work.go 顶部⚠️),
//     不必等后台校验, 用户切回该题时缓存已是新值
//   - 公共/个人分储: 条目 owner 为空 = 公共(题面/模板, 人人相同, 换号照常命中);
//     owner 非空 = 个人(catalog 与存档码), 读侧严格过滤, 会话之间互不可见;
//     登录成功顺手清掉「已无在册会话」账号的个人条目与存档码(磁盘卫生, 在场账号不动)
//   - 后台校验凭触发时会话的 (token, owner) 原子配对, 内容归属结构上不串号
//   - 校验失败 30s 退避, 期间继续回缓存, 不追着病上游打
//
// 落盘: relay-cache.json 防抖原子写(同 stateStore 手法), 重启/断网后题库照常可用;
// 超限按 fetchedAt LRU 淘汰。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	cacheFreshTTL        = 30 * 24 * time.Hour // 题面/样板/题库 fresh 期: 期内直接回缓存不碰上游
	cacheRevalidateFloor = 2 * time.Second     // 每次访问都校验类条目的最小校验间隔(客户端 1.5s 补拉不至于再触发一轮)
	cacheFailBackoff     = 30 * time.Second
	cacheMaxEntries      = 600
	cacheMaxBytes        = 64 << 20
	cacheFlushDebounce   = 2 * time.Second
)

type cachePolicy struct {
	fresh    time.Duration // fresh 期: 回缓存且不触发校验
	always   bool          // 每次访问都触发后台校验(作答代码追新)
	netFirst bool          // 网络优先: 永不拿缓存应付请求, 缓存只作上游不可达时的兜底
}

// cacheableGet: 哪些 GET /api/up/* 走缓存池。path 是去掉 /api/up 前缀、带首斜杠的形状
// (与 doUpstream 的入参一致)。
func cacheableGet(method, path string) (cachePolicy, bool) {
	if method != http.MethodGet {
		return cachePolicy{}, false
	}
	switch {
	case strings.HasPrefix(path, "/scheme-service/scheme/list/student/"):
		return cachePolicy{always: true}, true
	case strings.HasPrefix(path, "/scheme-service/scheme/getQuestionTemplate/"):
		return cachePolicy{fresh: cacheFreshTTL}, true
	case strings.HasPrefix(path, "/scheme-service/catalog/list"):
		return cachePolicy{netFirst: true}, true
	}
	return cachePolicy{}, false
}

func cacheKey(path string, query url.Values) string {
	if len(query) == 0 {
		return path
	}
	return path + "?" + query.Encode()
}

// 条目作用域: 题面/模板人人相同可共享, catalog 与作答码按账号隔离
type cacheScope int

const (
	scopePersonal cacheScope = iota // catalog: pass/accumTime 按账号
	scopeShared                     // template: 初始模板人人相同
	scopeMixed                      // scheme/list: 题面共享 + stuCode 个人
)

func cacheScopeOf(key string) cacheScope {
	switch {
	case strings.HasPrefix(key, "/scheme-service/catalog/list"):
		return scopePersonal
	case strings.HasPrefix(key, "/scheme-service/scheme/list/student/"):
		return scopeMixed
	default: // getQuestionTemplate 等其余可缓存端点
		return scopeShared
	}
}

type cacheEntry struct {
	body      []byte
	owner     string // 空=公共条目(题面/模板); 非空=个人条目, 仅该账号可见
	fetchedAt time.Time
	checkedAt time.Time
	failAt    time.Time
}

type cacheStore struct {
	mu       sync.Mutex
	file     string
	enabled  bool
	entries  map[string]*cacheEntry
	stu      map[string]map[string]string // 个人存档码: owner → key → stuCode(b64)
	inflight map[string]bool              // 正在后台校验的 key(单飞: 并发访问只放一个上游请求)
	bytes    int
	dirty    bool

	saveMu     sync.Mutex // 串行落盘, 不阻塞读路径(同 stateStore)
	flushTimer *time.Timer
}

type cacheStats struct {
	Enabled    bool `json:"enabled"`
	Entries    int  `json:"entries"`
	Bytes      int  `json:"bytes"`
	MaxEntries int  `json:"maxEntries"`
	MaxBytes   int  `json:"maxBytes"`
}

func (c *cacheStore) stats() cacheStats {
	if c == nil || !c.enabled {
		return cacheStats{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return cacheStats{
		Enabled:    true,
		Entries:    len(c.entries),
		Bytes:      c.bytes,
		MaxEntries: cacheMaxEntries,
		MaxBytes:   cacheMaxBytes,
	}
}

func newCacheStore(file string, enabled bool) *cacheStore {
	c := &cacheStore{
		file: file, enabled: enabled,
		entries:  map[string]*cacheEntry{},
		stu:      map[string]map[string]string{},
		inflight: map[string]bool{},
	}
	if enabled {
		c.load()
	}
	return c
}

// get: 可见性 = 已登录 && (公共条目 或 本人条目)。mixed(scheme/list)条目存的是
// 剥离 stuCode 的共享题面, 命中时把请求者自己的存档码拼回去(JSON 拼接在锁外完成)。
func (c *cacheStore) get(key, owner string) *cacheEntry {
	if owner == "" {
		return nil // 未登录读不到任何条目
	}
	scope := cacheScopeOf(key)
	c.mu.Lock()
	e := c.entries[key]
	if e == nil || (e.owner != "" && e.owner != owner) {
		c.mu.Unlock()
		return nil // 他人个人条目(catalog): 不得泄露
	}
	cp := *e
	var stu string
	if scope == scopeMixed {
		stu = c.stu[owner][key]
	}
	c.mu.Unlock()
	if scope == scopeMixed && stu != "" {
		cp.body = spliceStuCode(cp.body, stu)
	}
	return &cp
}

// beginRevalidate: 单飞占位。false = 已有同 key 校验在途, 调用方直接回缓存即可
func (c *cacheStore) beginRevalidate(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inflight[key] {
		return false
	}
	c.inflight[key] = true
	return true
}

func (c *cacheStore) endRevalidate(key string) {
	c.mu.Lock()
	delete(c.inflight, key)
	c.mu.Unlock()
}

// putFetch: 冷拉取或后台校验成功(调用方已确认 HTTP 200 + 信封成功码)。
// owner = 内容归属账号(触发方), 调用方须保证抓取用的会话就是这个账号的。
// mixed 条目剥离 stuCode 后以公共条目入池, 个人码记到 owner 名下(JSON 解析在锁外完成)。
func (c *cacheStore) putFetch(key string, body []byte, owner string) {
	if !c.enabled {
		return
	}
	scope := cacheScopeOf(key)
	if scope == scopePersonal && owner == "" {
		return // 个人条目无归属不入池——底线: catalog 绝不能变成公共条目
	}
	entryOwner, stored, stuCode := "", body, ""
	switch scope {
	case scopePersonal:
		entryOwner = owner // 个人条目整体按账号存
	case scopeMixed:
		stored, stuCode = splitStuCode(body) // 题面共享, 存档码个人另存(复用解析结果, 不重复解一次)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if scope == scopeMixed && owner != "" {
		c.setStuLocked(owner, key, stuCode)
	}
	if old := c.entries[key]; old != nil && old.owner == entryOwner {
		if bytes.Equal(old.body, stored) {
			old.checkedAt = now
			old.failAt = time.Time{}
			c.dirty = true
			c.scheduleFlushLocked()
			return
		}
		c.bytes -= len(old.body)
	}
	c.entries[key] = &cacheEntry{body: stored, owner: entryOwner, fetchedAt: now, checkedAt: now}
	c.bytes += len(stored)
	c.evictLocked()
	c.dirty = true
	c.scheduleFlushLocked()
}

func (c *cacheStore) markFail(key, owner string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// 公共条目(owner 空)人人可标退避; 个人条目仅本人
	if e := c.entries[key]; e != nil && (e.owner == "" || e.owner == owner) {
		e.failAt = time.Now()
		c.dirty = true
		c.scheduleFlushLocked()
	}
}

func (c *cacheStore) setStuLocked(owner, key, code string) {
	m := c.stu[owner]
	if m == nil {
		m = map[string]string{}
		c.stu[owner] = m
	}
	m[key] = code
}

// hasStu: 该账号在该题上是否已有存档码记录(码本身可为空=上游没有存档)。
// false = 该账号首见此题: 即使题面共享命中, 也要触发一轮校验拉回本人云端代码。
func (c *cacheStore) hasStu(owner, key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.stu[owner][key]
	return ok
}

// retainOwners: 登录成功后的磁盘卫生——个人条目(catalog)与存档码只保留
// 「仍有在册会话」的账号: 离场账号的清掉, 在场账号(其他浏览器还登着)的不动;
// 题面/模板是公共条目, 一律保留共享(题目人人相同, 换号不必冷加载)。返回清除条数。
func (c *cacheStore) retainOwners(live map[string]bool) int {
	if !c.enabled {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	purged := 0
	for k, e := range c.entries {
		if e.owner != "" && !live[e.owner] {
			c.bytes -= len(e.body)
			delete(c.entries, k)
			purged++
		}
	}
	for u := range c.stu {
		if !live[u] {
			delete(c.stu, u)
			purged++
		}
	}
	if purged > 0 {
		c.dirty = true
		c.scheduleFlushLocked()
	}
	return purged
}

// wipe: 整池清空(公共+个人)
func (c *cacheStore) wipe() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = map[string]*cacheEntry{}
	c.stu = map[string]map[string]string{}
	c.bytes = 0
	c.dirty = true
	c.scheduleFlushLocked()
}

// writeThroughCode: save/compile 成功后把新代码记到该账号名下(共享题面体不含码)。
// owner 须与当前登录用户一致(写穿的正是这个会话覆盖的存档)。
// 返回是否发生了内容变更(false = 代码没变)。
func (c *cacheStore) writeThroughCode(eid int64, codeB64, owner string) bool {
	if !c.enabled || owner == "" {
		return false
	}
	key := "/scheme-service/scheme/list/student/" + strconv.FormatInt(eid, 10)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stu[owner][key] == codeB64 {
		return false
	}
	c.setStuLocked(owner, key, codeB64)
	c.dirty = true
	c.scheduleFlushLocked()
	return true
}

// markPassed: 判题通过后把缓存题库里该题的 pass 补成 true。catalog 已是网络优先,
// 正常路径用不到它; 这是让「上游挂掉时的兜底副本」也别把刚过的题显示回未通过。
func (c *cacheStore) markPassed(eid int64, owner string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	anyChanged := false
	for key, e := range c.entries {
		if !strings.HasPrefix(key, "/scheme-service/catalog/list") || e.owner != owner {
			continue
		}
		var env map[string]any
		if json.Unmarshal(e.body, &env) != nil {
			continue
		}
		items, ok := env["data"].([]any)
		if !ok {
			continue
		}
		changed := false
		for _, it := range items {
			item, ok := it.(map[string]any)
			if !ok {
				continue
			}
			if f, ok := item["eid"].(float64); ok && int64(f) == eid && item["pass"] != true {
				item["pass"] = true
				changed = true
			}
		}
		if !changed {
			continue
		}
		b, err := jsonNoHTML(env)
		if err != nil {
			continue
		}
		c.bytes += len(b) - len(e.body)
		e.body = b
		anyChanged = true
	}
	if anyChanged {
		c.dirty = true
		c.scheduleFlushLocked()
	}
}

// evictLocked: 超限时按 fetchedAt 淘汰最旧, 条数与字节双上限
func (c *cacheStore) evictLocked() {
	if len(c.entries) <= cacheMaxEntries && c.bytes <= cacheMaxBytes {
		return
	}
	keys := make([]string, 0, len(c.entries))
	for k := range c.entries {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return c.entries[keys[i]].fetchedAt.Before(c.entries[keys[j]].fetchedAt)
	})
	for _, k := range keys {
		if len(c.entries) <= cacheMaxEntries && c.bytes <= cacheMaxBytes {
			break
		}
		c.bytes -= len(c.entries[k].body)
		delete(c.entries, k)
	}
}

// jsonNoHTML: 编码 JSON 但不转义 <>&——题面是 HTML, 默认转义会把 body 撑大
// 且与上游原文字节不一致(语义等价, 但没必要)
func jsonNoHTML(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// splitStuCode: 从 scheme/list 响应剥离个人存档码 → (可共享的题面体, 个人码)。
// 题面人人相同, 共享体里的 stuCode 置空串; 码由 putFetch 记到抓取账号名下。
func splitStuCode(body []byte) (shared []byte, stu string) {
	var env map[string]json.RawMessage
	if json.Unmarshal(body, &env) != nil {
		return body, ""
	}
	raw, ok := env["data"]
	if !ok {
		return body, ""
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(raw, &data) != nil {
		return body, ""
	}
	if sc, ok := data["stuCode"]; ok {
		_ = json.Unmarshal(sc, &stu)
	}
	data["stuCode"] = json.RawMessage(`""`)
	db, err := jsonNoHTML(data)
	if err != nil {
		return body, stu
	}
	env["data"] = db
	if out, err := jsonNoHTML(env); err == nil {
		return out, stu
	}
	return body, stu
}

// spliceStuCode: 把请求者自己的存档码拼回共享题面体(码为空则保持剥离态)
func spliceStuCode(shared []byte, stu string) []byte {
	if stu == "" {
		return shared
	}
	var env map[string]json.RawMessage
	if json.Unmarshal(shared, &env) != nil {
		return shared
	}
	raw, ok := env["data"]
	if !ok {
		return shared
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(raw, &data) != nil {
		return shared
	}
	b, err := json.Marshal(stu)
	if err != nil {
		return shared
	}
	data["stuCode"] = b
	db, err := jsonNoHTML(data)
	if err != nil {
		return shared
	}
	env["data"] = db
	if out, err := jsonNoHTML(env); err == nil {
		return out
	}
	return shared
}

// ===== 落盘 =====

type cacheEntryJSON struct {
	Body      string `json:"body"`
	Owner     string `json:"owner"` // 空=公共条目(题面/模板); 非空=个人条目(catalog)
	FetchedAt int64  `json:"fetchedAt"`
	CheckedAt int64  `json:"checkedAt"`
	FailAt    int64  `json:"failAt,omitempty"`
}

// v3: 公共/个人分储——entries 存共享题面(个人码已剥离)与个人 catalog,
// StuCodes 按账号存作答码。v1/v2 结构不同, 整文件弃用。
type cacheFileJSON struct {
	Version  int                          `json:"version"`
	SavedAt  int64                        `json:"savedAt"`
	Entries  map[string]cacheEntryJSON    `json:"entries"`
	StuCodes map[string]map[string]string `json:"stuCodes,omitempty"`
}

func (c *cacheStore) scheduleFlushLocked() {
	if !c.enabled || c.file == "" {
		return
	}
	if c.flushTimer != nil {
		return
	}
	c.flushTimer = time.AfterFunc(cacheFlushDebounce, func() {
		c.mu.Lock()
		c.flushTimer = nil
		c.mu.Unlock()
		c.flush()
	})
}

func (c *cacheStore) flush() {
	if !c.enabled || c.file == "" {
		return
	}
	c.saveMu.Lock()
	defer c.saveMu.Unlock()
	c.mu.Lock()
	if !c.dirty {
		c.mu.Unlock()
		return
	}
	file := c.file
	total := c.bytes
	c.dirty = false
	if total == 0 && len(c.stu) == 0 {
		c.mu.Unlock()
		_ = os.Remove(file)
		return
	}
	j := cacheFileJSON{
		Version:  3,
		SavedAt:  time.Now().Unix(),
		Entries:  make(map[string]cacheEntryJSON, len(c.entries)),
		StuCodes: make(map[string]map[string]string, len(c.stu)),
	}
	for k, e := range c.entries {
		je := cacheEntryJSON{
			Body:      string(e.body),
			Owner:     e.owner,
			FetchedAt: e.fetchedAt.Unix(),
			CheckedAt: e.checkedAt.Unix(),
		}
		if !e.failAt.IsZero() {
			je.FailAt = e.failAt.Unix()
		}
		j.Entries[k] = je
	}
	// 深拷贝 c.stu: 绝不能在释放 c.mu 后继续读共享 map 引用(防并发读写竞态)
	for u, m := range c.stu {
		if len(m) == 0 {
			continue
		}
		cp := make(map[string]string, len(m))
		for k, v := range m {
			cp[k] = v
		}
		j.StuCodes[u] = cp
	}
	c.mu.Unlock()

	b, err := jsonNoHTML(&j)
	if err != nil {
		log.Printf("[cache] 落盘序列化失败: %v", err)
		return
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		log.Printf("[cache] 落盘写入失败 (%s): %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, file); err != nil {
		_ = os.Remove(tmp)
		log.Printf("[cache] 落盘原子替换失败 (%s): %v", file, err)
	}
}

// close: 优雅停机时停掉防抖定时器并立即同步落盘。
func (c *cacheStore) close() {
	if c == nil || !c.enabled || c.file == "" {
		return
	}
	c.mu.Lock()
	if c.flushTimer != nil {
		c.flushTimer.Stop()
		c.flushTimer = nil
	}
	c.mu.Unlock()
	c.flush()
}

func (c *cacheStore) load() {
	b, err := os.ReadFile(c.file)
	if err != nil {
		return
	}
	var j cacheFileJSON
	if json.Unmarshal(b, &j) != nil || j.Version != 3 {
		log.Printf("[cache] %s 无法识别或版本不符, 忽略", c.file)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range j.Entries {
		if e.Body == "" || e.FetchedAt <= 0 {
			continue
		}
		entry := &cacheEntry{
			body:      []byte(e.Body),
			owner:     e.Owner,
			fetchedAt: time.Unix(e.FetchedAt, 0),
		}
		if e.CheckedAt > 0 {
			entry.checkedAt = time.Unix(e.CheckedAt, 0)
		}
		if e.FailAt > 0 {
			entry.failAt = time.Unix(e.FailAt, 0)
		}
		c.entries[k] = entry
		c.bytes += len(e.Body)
	}
	for u, m := range j.StuCodes {
		if u == "" || len(m) == 0 {
			continue
		}
		c.stu[u] = m
	}
	c.evictLocked()
}

// ===== server 集成 =====

// serveCacheable: 缓存命中直接回(顺带决定是否触发后台校验并贴 X-Relay-Stale)。
// miss 返回 false, 由 handleProxy 走冷路径。网络优先条目永不在此短路。
// 只回当前会话可见的条目(owner 过滤在 cacheStore.get)。
func (s *server) serveCacheable(w http.ResponseWriter, path string, query url.Values, p cachePolicy, sess *session) bool {
	if !s.cache.enabled || p.netFirst {
		return false
	}
	owner := sess.Username()
	key := cacheKey(path, query)
	e := s.cache.get(key, owner)
	if e == nil {
		return false
	}
	stale := false
	due := p.always || time.Since(e.checkedAt) >= p.fresh
	if due && time.Since(e.checkedAt) >= cacheRevalidateFloor && time.Since(e.failAt) >= cacheFailBackoff {
		// 该校验而未完(本次触发或已在途)都贴标记: 客户端紧跟着的立即校正请求
		// 多半撞上在途校验, 不贴标记它就不会补拉, 外部更新会漏掉
		if s.cache.beginRevalidate(key) {
			go s.revalidateEntry(key, path, query, sess)
		}
		stale = true
	}
	if !stale && cacheScopeOf(key) == scopeMixed && !s.cache.hasStu(owner, key) {
		// 本人首见此题(无本人存档码记录): 共享题面照样秒回, 同时拉一轮本人云端代码
		if s.cache.beginRevalidate(key) {
			go s.revalidateEntry(key, path, query, sess)
		}
		stale = true
	}
	if stale {
		w.Header().Set("X-Relay-Stale", "1")
	}
	if sess.expiredFlag() {
		w.Header().Set("X-Relay-Auth", "expired")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(e.body)
	return true
}

// serveStaleFallback: 网络优先条目的兜底——上游不可达/5xx 时回缓存, 保住「题库断网可用」;
// 没有缓存返回 false, 由调用方照常报错。4xx(尤其 401 掉登录)不走这里, 必须原样冒泡。
func (s *server) serveStaleFallback(w http.ResponseWriter, path string, query url.Values, sess *session) bool {
	if !s.cache.enabled {
		return false
	}
	e := s.cache.get(cacheKey(path, query), sess.Username())
	if e == nil {
		return false
	}
	w.Header().Set("X-Relay-Stale", "1")
	if sess.expiredFlag() {
		w.Header().Set("X-Relay-Auth", "expired")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(e.body)
	return true
}

// revalidateEntry: 后台校验。只认 HTTP 200 + 信封成功码, 其余按失败退避;
// 401/403 仍走 markInvalid(会话失效要冒泡到前端弹登录), 但不污染缓存。
// ctx 用 Background: 校验的使命是填缓存, 不随触发它的客户端断开而取消。
// 触发时刻从会话原子取 (token, owner): 凭据与归属永远配对——即使抓取途中
// 该会话重登录/换号, 入池也只是「把该账号凭它自己 token 拉回的内容记到它名下」,
// 结构上不存在串号(旧单会话实现的「完成时会话已换人则弃」守卫因此不再需要)。
func (s *server) revalidateEntry(key, path string, query url.Values, sess *session) {
	defer s.cache.endRevalidate(key)
	tok, owner := sess.cred()
	if tok == "" || owner == "" {
		return // 会话已登出/清空: 没有可用的归属身份, 不校验不入池
	}
	status, body, err := s.doUpstream(context.Background(), http.MethodGet, path, query, nil, "", tok)
	if err != nil {
		if errors.Is(err, errCooldown) {
			log.Printf("[cache] 校验 %s 暂停: %v", key, err)
		} else {
			log.Printf("[cache] 校验 %s 失败: %v", key, err)
		}
		s.cache.markFail(key, owner)
		return
	}
	if wafHit(status, body) {
		// 拿到的是 WAF 拦截页: 按失败退避(30s 后冷却中仍会被 wait 拒绝, 不会续期)
		s.cache.markFail(key, owner)
		return
	}
	sess.markInvalid(status, body)
	if status != 200 || !envelopeOK(body) {
		log.Printf("[cache] 校验 %s 未通过(http=%d), 退避 %s 内继续用缓存", key, status, cacheFailBackoff)
		s.cache.markFail(key, owner)
		return
	}
	s.cache.putFetch(key, body, owner)
}

// envelopeOK: 上游信封成功码全集才值得缓存——[200,0] + 废弃兼容 [31,41,70]
// (fuckView analysis/08; 生产 scheme/list/student 实测回的就是 31, 只认 200 会让
// 题面永远进不了缓存池)。信封错误(业务失败/未登录)缓存住会毒害前端。
// Code 用指针: 字段缺失(nil)与显式 0 不同——裸 JSON 不配进缓存池。
func envelopeOK(body []byte) bool {
	var env struct {
		Code *int `json:"code"`
	}
	if json.Unmarshal(body, &env) != nil || env.Code == nil {
		return false
	}
	switch *env.Code {
	case 200, 0, 31, 41, 70:
		return true
	}
	return false
}
