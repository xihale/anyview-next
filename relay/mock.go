// 离线假上游(-mock): 前端开发/截图不依赖真实 AnyView。
// 覆盖 catalog、题目详情、编译判题、计时状态、gdb 调试假会话; 其余透传路径一律回 code:200。
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type mockItem struct {
	Difficulty  int     `json:"difficulty"`
	EID         int64   `json:"eid"`
	Pass        bool    `json:"pass"`
	PName       string  `json:"pname"`
	Kind        int     `json:"kind"`
	PMemo       string  `json:"pmemo"`
	AccumTime   float64 `json:"accumTime"`
	ChapName    string  `json:"chapName"`
	QuestionTyp int     `json:"questionType"`
}

var mockCatalog = []mockItem{
	{1, 114001, true, "DC01PE01", 4, "", 613.0, "1", 1},
	{2, 114002, true, "DC01PE02", 4, "", 942.0, "1", 1},
	{3, 114003, false, "DC01PE03", 4, "", 1275.5, "1", 1},
	{1, 114010, true, "DC02PE01", 4, "", 480.0, "2", 1},
	{2, 114011, false, "DC02PE02", 4, "", 2360.0, "2", 1},
	{3, 114012, false, "DC02PE03", 4, "", 0, "2", 1},
	{4, 114020, false, "DC02ES10", 2, "实验题", 0, "2", 2},
	{2, 114030, true, "DC03PE01", 4, "", 731.0, "3", 1},
	{3, 114031, false, "DC03PE02", 4, "", 158.0, "3", 1},
	{3, 114032, false, "DC03PE03", 4, "", 0, "3", 1},
}

func mockDoc(eid int64, pname string) string {
	kind := "函数补全"
	if strings.Contains(pname, "ES") {
		kind = "实验题"
	}
	return fmt.Sprintf(`<div class="doc">
<h2>%s · %s</h2>
<p><b>题目描述</b>：给定一个含 n 个整数的高桥君序列，请按非降序排列后输出。（Mock 数据，eid=%d）</p>
<p><b>输入</b>：第一行一个整数 n（1 ≤ n ≤ 100）；第二行 n 个整数。</p>
<p><b>输出</b>：一行，按升序排列的 n 个整数，空格分隔。</p>
<p><b>提示</b>：补全 <code>sortList(int a[], int n)</code> 函数，不要修改 main。</p>
</div>`, pname, kind, eid)
}

func mockTemplate(pname string) string {
	if strings.Contains(pname, "ES") {
		return "#include <iostream>\nusing namespace std;\n\n// 实验题: 完整程序\nint main() {\n    // TODO\n    return 0;\n}\n"
	}
	return "#include <iostream>\n#include <algorithm>\nusing namespace std;\n\n// 补全该函数\nvoid sortList(int a[], int n) {\n    // TODO\n}\n\nint main() {\n    int n; cin >> n;\n    int a[128];\n    for (int i = 0; i < n; i++) cin >> a[i];\n    sortList(a, n);\n    for (int i = 0; i < n; i++) cout << a[i] << \" \";\n    cout << endl;\n    return 0;\n}\n"
}

func mockRun(req *runRequest, start time.Time) runResult {
	elapsed := time.Since(start).Milliseconds()
	if strings.Contains(req.Code, "MOCKFAIL") {
		return runResult{
			CompileOK:  false,
			CompileMsg: "main.cpp: In function 'void sortList(int*, int)':\nmain.cpp:7:5: error: 'x' was not declared in this scope\nmain.cpp:9:12: error: expected ';' before '}' token",
			Diags: []diag{
				{Line: 7, Col: 5, Sev: "error", Message: "'x' was not declared in this scope"},
				{Line: 9, Col: 12, Sev: "error", Message: "expected ';' before '}' token"},
			},
			ElapsedMs: elapsed,
		}
	}
	if strings.Contains(req.Code, "MOCKINPUT") {
		return runResult{
			CompileOK: true, CompileMsg: "编译成功", NeedInput: true,
			Output: "请输入 n: ", ElapsedMs: elapsed,
		}
	}
	return runResult{
		CompileOK: true, CompileMsg: "编译成功", Passed: true,
		Right: 5, Wrong: 0,
		Output: "1 2 3 4 5\n", ElapsedMs: elapsed,
	}
}

func (s *server) mockProxy(w http.ResponseWriter, r *http.Request, path string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	switch {
	case strings.HasPrefix(path, "/scheme-service/catalog/list"):
		writeJSON(w, 200, map[string]any{"code": 200, "message": "ok", "data": mockCatalog})
	case strings.HasPrefix(path, "/scheme-service/scheme/list/student/"):
		var eid int64
		fmt.Sscanf(path, "/scheme-service/scheme/list/student/%d", &eid)
		pname := "DC01PE01"
		for _, it := range mockCatalog {
			if it.EID == eid {
				pname = it.PName
			}
		}
		doc := mockDoc(eid, pname)
		writeJSON(w, 200, map[string]any{
			"code": 200, "message": "ok",
			"data": map[string]any{
				"questionFullName": qfnOf(pname),
				"questionId":       eid,
				"kind":             4,
				"doc":              doc,
				"language":         1,
				"completes":        []any{},
				"stuCode":          base64.StdEncoding.EncodeToString([]byte(mockTemplate(pname))),
			},
		})
	case strings.HasPrefix(path, "/scheme-service/scheme/getQuestionTemplate/student/"):
		// 真模板端点: 让前端"重置模板"在 mock 下也有真模板可拉(区别于上次保存的代码)
		writeJSON(w, 200, map[string]any{
			"code": 200, "message": "ok",
			"data": base64.StdEncoding.EncodeToString([]byte(mockTemplate("DC01PE01"))),
		})
	default:
		writeJSON(w, 200, map[string]any{"code": 200, "message": "mock", "data": nil})
	}
}

func qfnOf(pname string) string {
	for _, it := range mockCatalog {
		if it.PName == pname {
			return fmt.Sprintf("第%s章-%s", it.ChapName, pname)
		}
	}
	return pname
}

// ===== gdb 镜像假会话(/api/gdb/*, -mock 离线调试用) =====

type mockDbg struct {
	line int
	out  strings.Builder
}

func mockEnv(data any) map[string]any {
	return map[string]any{"code": 200, "message": "mock", "data": data}
}

func mockRunData(st *mockDbg, end bool) map[string]any {
	// variables 形状对齐真机: [{variate:{name,type,value,innerObj[]}}] 递归
	return map[string]any{
		"exception": "", "runErrCount": 0, "runRightCount": 5,
		"needInput": false, "errorOrder": map[string]any{},
		"backtrace": []string{"mock () Line " + strconv.Itoa(st.line), "main () Line 42", ""},
		"variables": []map[string]any{
			{"variate": map[string]any{
				"name": "n", "type": "int", "value": "5", "innerObj": []any{},
			}},
			{"variate": map[string]any{
				"name": "arr", "type": "int [5]", "value": "0x617030",
				"innerObj": []map[string]any{
					{"name": "arr[0]", "type": "int", "value": "1", "innerObj": []any{}},
					{"name": "arr[1]", "type": "int", "value": "2", "innerObj": []any{}},
				},
			}},
		},
		"watchPoint": map[string]any{}, "dataStruct": map[string]any{},
		"output": st.out.String(), "lineNum": strconv.Itoa(st.line),
		"end": end, "passed": true, "order": 1, "path": "",
	}
}

// mockGdb: 按端点形状回假 envelope, 前端离线开发调试 UI 不碰真实 AnyView。
func (s *server) mockGdb(w http.ResponseWriter, r *http.Request, path string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	// 收集载荷字段(body JSON 拍平 / GET query), 供 cmd/inputStr/eID 取值
	vals := map[string]string{}
	if r.Method == http.MethodGet {
		for k, vs := range r.URL.Query() {
			if len(vs) > 0 {
				vals[k] = vs[0]
			}
		}
	} else {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var payload map[string]any
		if dec.Decode(&payload) == nil {
			for _, kv := range flatten(payload, "") {
				vals[kv[0]] = kv[1]
			}
		}
	}
	eid := firstNonEmpty(vals["eID"], vals["labExerciseId"])
	var id int64
	fmt.Sscanf(eid, "%d", &id)

	get := func(key string) string {
		for k, v := range vals {
			if k == key || strings.HasPrefix(k, key+".") {
				return v
			}
		}
		return ""
	}

	s.mockDbgMu.Lock()
	defer s.mockDbgMu.Unlock()
	switch {
	case strings.HasPrefix(path, "debug/start"):
		st := &mockDbg{line: 5}
		if strings.Contains(get("stuCode")+get("codeInfos"), "MOCKFAIL") {
			writeJSON(w, 200, map[string]any{"code": 500, "message": "mock: 编译失败", "data": nil})
			return
		}
		s.mockDbgs[id] = st
		writeJSON(w, 200, mockEnv(mockRunData(st, false)))
	case strings.HasPrefix(path, "debug/nextStep"), strings.HasPrefix(path, "debug/skipOut"):
		st := s.mockDbgs[id]
		if st == nil {
			writeJSON(w, 200, mockEnv(mockRunData(&mockDbg{line: 1}, true)))
			return
		}
		if strings.Contains(vals["cmd"], "continue") {
			st.out.WriteString("sum=12\n")
			writeJSON(w, 200, mockEnv(mockRunData(st, true)))
			return
		}
		st.line += 1 + (st.line*7)%3
		writeJSON(w, 200, mockEnv(mockRunData(st, false)))
	case strings.HasPrefix(path, "debug/quit"):
		delete(s.mockDbgs, id)
		writeJSON(w, 200, mockEnv(map[string]any{"end": true}))
	case strings.HasPrefix(path, "debug/breakPoints"):
		if r.Method == http.MethodGet {
			writeJSON(w, 200, mockEnv([]any{}))
			return
		}
		writeJSON(w, 200, mockEnv(nil))
	case strings.HasPrefix(path, "debug/setWatchPoint"):
		writeJSON(w, 200, mockEnv(map[string]any{get("addPoints"): "3"}))
	case strings.HasPrefix(path, "code/exeInput"):
		st := s.mockDbgs[id]
		if st == nil {
			st = &mockDbg{line: 1}
		}
		st.out.WriteString(get("inputStr"))
		writeJSON(w, 200, mockEnv(mockRunData(st, false)))
	case strings.HasPrefix(path, "code/interruptInput"):
		writeJSON(w, 200, mockEnv(mockRunData(&mockDbg{line: 1}, true)))
	case strings.HasPrefix(path, "code/compile"):
		codeTxt := get("stuCode") + get("codeInfos")
		if strings.Contains(codeTxt, "MOCKFAIL") {
			writeJSON(w, 200, mockEnv(map[string]any{"result": "编译失败\nmain.cpp:7:5: error: 'x' was not declared in this scope"}))
			return
		}
		writeJSON(w, 200, mockEnv(map[string]any{"result": "编译成功", "cmpRightCount": 0, "cmpErrorCount": 0}))
	case strings.HasPrefix(path, "code/runGroup"), strings.HasPrefix(path, "code/run"):
		st := &mockDbg{line: 0}
		st.out.WriteString("1 2 3 4 5\n")
		writeJSON(w, 200, mockEnv(mockRunData(st, true)))
	case strings.HasPrefix(path, "exercise/saveStudentCode"), strings.HasPrefix(path, "exercise/saveCode"):
		writeJSON(w, 200, mockEnv(nil))
	case strings.HasPrefix(path, "exercise/pastCode"):
		writeJSON(w, 200, mockEnv("#include <iostream>\nint main() { return 0; }\n"))
	case strings.HasPrefix(path, "exercise/errorOrder"):
		writeJSON(w, 200, mockEnv([]any{}))
	default:
		writeJSON(w, 200, mockEnv(nil))
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
