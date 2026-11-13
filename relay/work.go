// 编译/判题/喂输入/存码管线。
// 上游均为 gdb-service multipart 接口, stuCode 一律 base64(代码原文):
//
//	compile : /gdb-service/code/compile    {questionRes.questionFullName, eID, stuCode, isTeacher, isDebug}
//	runGroup: /gdb-service/code/runGroup   {questionFullName, eID, isTeacher, kind, language}
//	         每调判一组(order 递进), 须轮询打到 end=true; C/C++ 不带 stuCode
//	exeInput: /gdb-service/code/exeInput   {inputStr, isTeacher, language, kind, eID}  (needInput 时喂 stdin)
//	存码    : /gdb-service/exercise/saveStudentCode (JSON {eID, studentCode})
//
// ⚠️ compile/runGroup 会顺带覆盖平台侧 stuCode 存档(见 analysis)。
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type runRequest struct {
	EID      int64  `json:"eid"`
	QFN      string `json:"qfn"` // questionFullName
	Code     string `json:"code"`
	Language int    `json:"language"` // scheme.language, 默认 1
	Kind     int    `json:"kind"`     // catalog.kind, 默认 4
	Input    string `json:"input"`    // needInput 时喂入
}

type runResult struct {
	CompileOK  bool   `json:"compileOk"`
	CompileMsg string `json:"compileMsg"`
	Diags      []diag `json:"diagnostics,omitempty"`
	Passed     bool   `json:"passed,omitempty"`
	NeedInput  bool   `json:"needInput,omitempty"`
	Output     string `json:"output,omitempty"`
	Exception  string `json:"exception,omitempty"`
	Right      int    `json:"rightCount"`
	Wrong      int    `json:"wrongCount"`
	ElapsedMs  int64  `json:"elapsedMs"`
	Upstream   string `json:"upstream,omitempty"` // 非成功时的上游原文片段
}

type diag struct {
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	Sev     string `json:"severity"` // error | warning
	Message string `json:"message"`
}

var (
	brTagsRe   = regexp.MustCompile(`(?i)<br\s*/?>`)
	htmlTagsRe = regexp.MustCompile(`(?i)</?[a-z][^>]*>`)
	// &amp; 必须放最后: NewReplacer 单趟替换不回扫, "amp;lt;" 不会二次解码
	htmlEntRe = strings.NewReplacer(
		"&nbsp;", " ", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'", "&#39;", "'", "&amp;", "&",
	)
)

// htmlToText: 上游编译/判题结果常是 HTML(实测形如 "编译失败<br><i class=el-icon-*>
// 7:5:&nbsp;error: ..."), gcc 诊断解析与终端展示都依赖这一步清洗。
func htmlToText(s string) string {
	s = brTagsRe.ReplaceAllString(s, "\n")
	s = htmlTagsRe.ReplaceAllString(s, "")
	return strings.TrimRight(htmlEntRe.Replace(s), "\n")
}

// gcc 行可无文件名(上游编译固定 Dx.cpp, 失败输出形如 "7:5: error: ...")且可无列号
var gccRe = regexp.MustCompile(`(?:[^\s:]+\.(?:cpp|c|cc|cxx|h|hpp):)?(\d+)(?::(\d+))?:\s*(error|warning|note):\s*(.+)`)

func parseGCCDiags(text string) []diag {
	var out []diag
	for _, line := range strings.Split(text, "\n") {
		m := gccRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if m[3] == "note" {
			continue
		}
		l, _ := strconv.Atoi(m[1])
		c, _ := strconv.Atoi(m[2])
		out = append(out, diag{Line: l, Col: c, Sev: m[3], Message: m[4]})
	}
	return out
}

func firstLine(s, fallback string) string {
	for _, ln := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			return t
		}
	}
	return fallback
}

// resubmittable: 上游判题会话态丢失时的典型说辞; 空消息按未知瞬态也算可重试
func resubmittable(msg string) bool {
	if msg == "" {
		return true
	}
	return strings.Contains(msg, "重新提交") || strings.Contains(msg, "重试") || strings.Contains(msg, "再试")
}

// upstreamCompile: 打上游 compile(isDebug 由调用方给)。
// 返回 (编译成功?, 清洗后的结果文本, 会话失效?, 上游HTTP状态)。
func (s *server) upstreamCompile(ctx context.Context, sess *session, eid int64, qfn, codeB64 string, isDebug bool) (ok bool, msg string, authFail bool, status int, err error) {
	dbg := "false"
	if isDebug {
		dbg = "true"
	}
	ct, body, err := encodeMultipart([][2]string{
		{"questionRes.questionFullName", qfn},
		{"eID", strconv.FormatInt(eid, 10)},
		{"stuCode", codeB64},
		{"isTeacher", "false"},
		{"isDebug", dbg},
	})
	if err != nil {
		return false, "", false, 0, err
	}
	status, respBody, err := s.doUpstream(ctx, "POST", "/gdb-service/code/compile", nil, body, ct, sess.Token())
	if err != nil {
		return false, "", false, status, err
	}
	sess.markInvalid(status, respBody)
	var cResp struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Result string `json:"result"`
		} `json:"data"`
	}
	if json.Unmarshal(respBody, &cResp) != nil {
		return false, truncate(htmlToText(string(respBody)), 300), status == 401 || status == 403, status, nil
	}
	authFail = status == 401 || status == 403 || strings.Contains(cResp.Message, "状态失效")
	if cResp.Data.Result != "" {
		msg = htmlToText(cResp.Data.Result)
	} else if cResp.Message != "" {
		msg = htmlToText(cResp.Message)
	}
	return cResp.Code == 200 && strings.Contains(cResp.Data.Result, "编译成功"), msg, authFail, status, nil
}

func (s *server) handleRun(w http.ResponseWriter, r *http.Request) {
	var req runRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || req.EID == 0 {
		fail(w, 400, "需要 eid/code")
		return
	}
	if req.Language == 0 {
		req.Language = 1
	}
	if req.Kind == 0 {
		req.Kind = 4
	}
	codeB64 := base64.StdEncoding.EncodeToString([]byte(strings.TrimSpace(req.Code)))
	start := time.Now()

	if s.mock {
		t := time.NewTimer(400 * time.Millisecond)
		select {
		case <-r.Context().Done():
			t.Stop()
			return
		case <-t.C:
		}
		writeJSON(w, 200, mockRun(&req, start))
		return
	}

	// 会话按 cookie 定位: 每个浏览器只能跑自己账号的判题
	sess := s.sessions.current(r)
	if sess == nil || !sess.loggedIn() {
		fail(w, 401, "未登录")
		return
	}
	tok := sess.Token()

	// 1. 编译
	ok, cMsg, authFail, cStatus, err := s.upstreamCompile(r.Context(), sess, req.EID, req.QFN, codeB64, false)
	if err != nil {
		failUpstream(w, err)
		return
	}
	if cStatus == 403 || cStatus == 429 {
		if rem, cooling := s.lim.cooling(); cooling {
			failCooldown(w, rem) // 编译响应是 WAF 拦截页, 不能误报成编译失败/掉登录
			return
		}
	}
	// 会话失效/上游拒绝: 不能伪装成"编译未通过"——那会渲染出一条空诊断+「编译错误」
	// 提示, 用户根本想不到是掉登录了(2026-09-15 DC01PE65 实报)
	if authFail || cStatus == 401 || cStatus == 403 {
		fail(w, 401, "登录状态失效，请重新登录")
		return
	}
	if cStatus >= 400 {
		fail(w, 502, "编译服务错误(上游 "+strconv.Itoa(cStatus)+")")
		return
	}
	res := runResult{ElapsedMs: time.Since(start).Milliseconds()}
	res.CompileOK = ok
	res.CompileMsg = cMsg
	log.Printf("[run] eid=%d 编译 ok=%v 结果=%d字", req.EID, ok, len(cMsg))
	if !res.CompileOK {
		res.Diags = parseGCCDiags(cMsg)
		if len(res.Diags) == 0 {
			res.Diags = []diag{{Line: 1, Col: 1, Sev: "error", Message: firstLine(cMsg, "编译失败")}}
		}
		writeJSON(w, 200, res)
		return
	}

	// 编译成功 = 上游工作目录/存档已被这份代码覆盖, 缓存里的 stuCode 直接写穿对齐,
	// 用户切走再切回不会闪回旧代码(校验兜底外部改动, 不依赖这一步)
	s.cache.writeThroughCode(req.EID, codeB64, sess.Username())

	// 2. 成组判题: runGroup 每调判一组(order 递进), 须轮询打到 end=true——只调一次
	// 的话多组题 passed/runRightCount 恒空、输出只有第一组(fuckView solutions run.ts 同款)。
	// C/C++ 的 runGroup 原版不带 stuCode(判的是上次 compile 落盘的工作目录代码)。
	// 上游偶发返回非 200 让"重新提交"(判题会话态丢失), 自动重编译+重跑, 不惊动用户。
	rgContentType, rgBody, err := encodeMultipart([][2]string{
		{"questionFullName", req.QFN},
		{"eID", strconv.FormatInt(req.EID, 10)},
		{"isTeacher", "false"},
		{"kind", strconv.Itoa(req.Kind)},
		{"language", strconv.Itoa(req.Language)},
	})
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	var data runData
	var outs []string
	fed := false
	resubmits := 0
	for g := 0; g < 20; g++ {
		status, respBody, err := s.doUpstream(r.Context(), "POST", "/gdb-service/code/runGroup", nil, rgBody, rgContentType, tok)
		if err != nil {
			failUpstream(w, err) // 冷却/排队超时报 429; 窗口满时判题轮询会被限速排队拖慢而非打爆上游
			return
		}
		if wafHit(status, respBody) {
			rem, _ := s.lim.cooling()
			failCooldown(w, rem) // 本轮 runGroup 拿到的是 WAF 拦截页, 硬解析只会产出乱码错误
			return
		}
		sess.markInvalid(status, respBody)
		var rResp struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    runData
		}
		if json.Unmarshal(respBody, &rResp) != nil {
			fail(w, 502, "判题响应解析失败: "+truncate(string(respBody), 300))
			return
		}
		if status == 401 || status == 403 || rResp.Code == 401 || rResp.Code == 403 {
			fail(w, 401, "登录状态失效，请重新登录")
			return
		}
		if rResp.Code != 200 {
			msg := htmlToText(rResp.Message)
			log.Printf("[run] eid=%d 第%d轮 runGroup 失败 code=%d http=%d msg=%q",
				req.EID, g+1, rResp.Code, status, truncate(msg, 120))
			if resubmits < 2 && resubmittable(msg) {
				resubmits++
				log.Printf("[run] eid=%d 自动重提: 重编译后第 %d 次重跑判题", req.EID, resubmits)
				if ok2, _, authFail2, cStatus2, err2 := s.upstreamCompile(r.Context(), sess, req.EID, req.QFN, codeB64, false); err2 == nil && !authFail2 && cStatus2 < 400 && ok2 {
					continue
				}
				log.Printf("[run] eid=%d 重编译未成, 放弃重试", req.EID)
			}
			// 重试额度用尽/不可重试: 把上游人话放进 exception, 前端控制台可见
			res.Exception = firstLine(msg, "判题服务错误(上游 "+strconv.Itoa(rResp.Code)+")")
			res.Upstream = truncate(string(respBody), 600)
			break
		}
		data = rResp.Data
		if data.Output != "" {
			outs = append(outs, data.Output)
		}
		log.Printf("[run] eid=%d 第%d轮 code=%d passed=%v end=%v order=%d right=%d err=%d out=%d字",
			req.EID, g+1, rResp.Code, data.Passed, data.End, data.Order, data.RunRightCount, data.RunErrCount, len(data.Output))

		// 3. 中途等输入: 每轮最多喂一次, 没带输入就交回前端收集
		if data.NeedInput {
			if fed || req.Input == "" || data.End {
				break
			}
			fed = true
			ct, body, err := encodeMultipart([][2]string{
				{"inputStr", req.Input},
				{"isTeacher", "false"},
				{"language", strconv.Itoa(req.Language)},
				{"kind", strconv.Itoa(req.Kind)},
				{"eID", strconv.FormatInt(req.EID, 10)},
			})
			if err != nil {
				fail(w, 500, err.Error())
				return
			}
			status, respBody, err = s.doUpstream(r.Context(), "POST", "/gdb-service/code/exeInput", nil, body, ct, tok)
			if err != nil {
				failUpstream(w, err)
				return
			}
			sess.markInvalid(status, respBody)
			var iResp struct {
				Code int     `json:"code"`
				Data runData `json:"data"`
			}
			if json.Unmarshal(respBody, &iResp) == nil && iResp.Code == 200 {
				data = iResp.Data
				if data.Output != "" {
					outs = append(outs, data.Output)
				}
			}
		}
		if data.End {
			break
		}
		t := time.NewTimer(300 * time.Millisecond)
		select {
		case <-r.Context().Done():
			t.Stop()
			failUpstream(w, r.Context().Err())
			return
		case <-t.C:
		}
	}

	// passed 是本次提交的权威判定(analysis/08 实测: runErrCount/runRightCount
	// 是该题历史累计计数而非本次结果)。analysis/06 的「end+零错也算判过」是
	// noview 时代老口径, 拿它兜底会把 passed=false 的多组题判成通过——首次提交
	// 的题历史零错是常态(2026-09-15 DC01PE49 实报: 提示评测通过, 刷新后题库仍是未通过)
	res.Passed = data.Passed
	if res.Passed {
		// catalog 网络优先正常拿的就是上游新值, 这里只兜上游挂掉的场景:
		// 别让兜底副本把刚过的题显示回未通过
		s.cache.markPassed(req.EID, sess.Username())
	}
	res.NeedInput = data.NeedInput && !data.Passed
	res.Output = strings.Join(outs, "\n")
	if res.Exception == "" {
		res.Exception = data.Exception
	}
	res.Right, res.Wrong = data.RunRightCount, data.RunErrCount
	res.ElapsedMs = time.Since(start).Milliseconds()
	log.Printf("[run] eid=%d 判定: passed=%v needInput=%v right=%d/%d 组数=%d 重提=%d 耗时=%dms",
		req.EID, res.Passed, res.NeedInput, res.Right, res.Right+res.Wrong, len(outs), resubmits, res.ElapsedMs)
	writeJSON(w, 200, res)
}

// runData: gdb-service 判题结果( fuckView types/common.ts RunData 的子集 )
type runData struct {
	Exception     string         `json:"exception"`
	RunErrCount   int            `json:"runErrCount"`
	RunRightCount int            `json:"runRightCount"`
	NeedInput     bool           `json:"needInput"`
	ErrorOrder    map[string]any `json:"errorOrder"`
	Output        string         `json:"output"`
	End           bool           `json:"end"`
	Passed        bool           `json:"passed"`
	Order         int            `json:"order"`
}

func (s *server) handleSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EID  int64  `json:"eid"`
		Code string `json:"code"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || req.EID == 0 {
		fail(w, 400, "需要 eid/code")
		return
	}
	if s.mock {
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}
	sess := s.sessions.current(r)
	if sess == nil || !sess.loggedIn() {
		fail(w, 401, "未登录")
		return
	}
	codeB64 := base64.StdEncoding.EncodeToString([]byte(strings.TrimSpace(req.Code)))
	body, _ := json.Marshal(map[string]string{
		"eID":         strconv.FormatInt(req.EID, 10),
		"studentCode": codeB64,
	})
	status, respBody, err := s.doUpstream(r.Context(), "POST", "/gdb-service/exercise/saveStudentCode", nil, body, "application/json", sess.Token())
	if err != nil {
		failUpstream(w, err)
		return
	}
	if wafHit(status, respBody) {
		rem, _ := s.lim.cooling()
		failCooldown(w, rem)
		return
	}
	sess.markInvalid(status, respBody)
	// 失败必须以真实状态码冒泡: 曾返回 200+{ok:false} 而前端只看 HTTP 状态,
	// 掉登录时照样弹"已保存至云端"(2026-09-15 DC01PE65 实报)
	if status == 401 || status == 403 || strings.Contains(string(respBody), "状态失效") {
		fail(w, 401, "登录状态失效，保存失败，请重新登录")
		return
	}
	if status != 200 {
		fail(w, 502, "保存失败(上游 "+strconv.Itoa(status)+")")
		return
	}
	s.cache.writeThroughCode(req.EID, codeB64, sess.Username())
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ===== 通用透传: /api/up/<service/...> → 上游 /api/<service/...>, 自动带会话头 =====

func (s *server) handleProxy(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/up")
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	if s.mock {
		s.mockProxy(w, r, path)
		return
	}
	// 缓存池按会话归属: 命中先回缓存(必要时触发后台校验), miss 再走冷透传;
	// 网络优先条目(catalog)永不在此短路, 缓存只作上游挂掉时的兜底。
	// 未登录(无会话)直接 401: 不再匿名打上游(白耗限速额度还会让上游 401)
	sess := s.sessions.current(r)
	if sess == nil || !sess.loggedIn() {
		fail(w, 401, "未登录")
		return
	}
	tok := sess.Token()
	p, cacheable := cacheableGet(r.Method, path)
	if cacheable && s.serveCacheable(w, path, r.URL.Query(), p, sess) {
		return
	}
	status, respBody, err := s.doUpstream(r.Context(), r.Method, path, r.URL.Query(), body, ct, tok)
	if err != nil {
		// 限速冷却中也要有题可看: 网络优先条目(catalog)先试 stale 兜底, 没缓存才报 429
		if cacheable && p.netFirst && s.serveStaleFallback(w, path, r.URL.Query(), sess) {
			return
		}
		failUpstream(w, err)
		return
	}
	sess.markInvalid(status, respBody)
	// WAF 拦截页(403/429): 优先回缓存兜底, 没缓存才把冷却实情报给前端
	if wafHit(status, respBody) {
		if cacheable && p.netFirst && s.serveStaleFallback(w, path, r.URL.Query(), sess) {
			return
		}
		rem, _ := s.lim.cooling()
		failCooldown(w, rem)
		return
	}
	// 上游 5xx 同样回缓存兜底; 4xx(尤其 401 掉登录)必须原样冒泡, 不能拿旧题库装没事
	if cacheable && p.netFirst && status >= 500 && s.serveStaleFallback(w, path, r.URL.Query(), sess) {
		return
	}
	if cacheable && status == 200 && envelopeOK(respBody) {
		s.cache.putFetch(cacheKey(path, r.URL.Query()), respBody, sess.Username())
	}
	if sess.expiredFlag() {
		w.Header().Set("X-Relay-Auth", "expired")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(respBody)
}
