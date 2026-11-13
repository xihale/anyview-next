package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 2026-09-15 真实上游抓包: 编译失败 result 是 HTML, gcc 行不带文件名且用 &nbsp; 缩进
const realCompileFailHTML = `编译失败<br><i class="el-icon-error"></i>7:5:&nbsp;error:&nbsp;expected&nbsp;unqualified-id&nbsp;before&nbsp;'this'<br>&nbsp;int&nbsp;this&nbsp;is&nbsp;broken&nbsp;syntax&nbsp;<<<<br>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;^<br>`

func TestHTMLToText(t *testing.T) {
	got := htmlToText(realCompileFailHTML)
	lines := strings.Split(got, "\n")
	if lines[0] != "编译失败" {
		t.Fatalf("首行应为「编译失败」, got %q", lines[0])
	}
	want := "7:5: error: expected unqualified-id before 'this'"
	if lines[1] != want {
		t.Fatalf("gcc 行清洗不符\n got: %q\nwant: %q", lines[1], want)
	}
	if strings.Contains(got, "&") || strings.Contains(got, "<br") {
		t.Fatalf("仍残留实体/标签: %q", got)
	}
}

func TestParseGCCDiags_NoFilename(t *testing.T) {
	ds := parseGCCDiags(htmlToText(realCompileFailHTML))
	if len(ds) != 1 {
		t.Fatalf("应解析出 1 条诊断, got %d: %+v", len(ds), ds)
	}
	d := ds[0]
	if d.Line != 7 || d.Col != 5 || d.Sev != "error" {
		t.Fatalf("位置不符: %+v", d)
	}
	if d.Message != "expected unqualified-id before 'this'" {
		t.Fatalf("message 不符: %q", d.Message)
	}
}

func TestParseGCCDiags_ClassicAndColless(t *testing.T) {
	text := "Dx.cpp:12:5: error: expected ';' before '}'\n" +
		"foo.c:9: warning: implicit declaration of function 'bar'\n" +
		"Dx.cpp:10:1: note: something\n" +
		"   int this is a source line\n" +
		"     ^"
	ds := parseGCCDiags(text)
	if len(ds) != 2 {
		t.Fatalf("应解析出 2 条(error+warning, note/源行/剑头跳过), got %+v", ds)
	}
	if ds[0].Line != 12 || ds[0].Col != 5 || ds[0].Sev != "error" {
		t.Fatalf("经典三段式不符: %+v", ds[0])
	}
	if ds[1].Line != 9 || ds[1].Col != 0 || ds[1].Sev != "warning" {
		t.Fatalf("无列号 warning 不符: %+v", ds[1])
	}
}

func TestFirstLineFallback(t *testing.T) {
	if got := firstLine("\n  \n编译失败 \n", "x"); got != "编译失败" {
		t.Fatalf("got %q", got)
	}
	if got := firstLine("  \n\t\n", "编译失败"); got != "编译失败" {
		t.Fatalf("空文本应回退, got %q", got)
	}
}

// TestRunVerdictHonorsUpstreamPassed: 判定只认本次的 passed 字段(analysis/08 实测:
// runErrCount 是历史累计而非本次结果)。锁死 PE49 实报形态——passed=false + end +
// 历史零错不得兜底成「评测通过」。
func TestRunVerdictHonorsUpstreamPassed(t *testing.T) {
	cases := []struct {
		name       string
		runData    string
		wantPassed bool
	}{
		{"passed=true 即通过(历史有错不影响)", `{"runErrCount":2,"runRightCount":1,"needInput":false,"output":"o","end":true,"passed":true,"order":3}`, true},
		{"passed=false 不得被历史零错兜底(PE49)", `{"runErrCount":0,"runRightCount":0,"needInput":false,"output":"o","end":true,"passed":false,"order":4}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/api/gdb-service/code/compile", func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `{"code":200,"message":"","data":{"result":"编译成功"}}`)
			})
			mux.HandleFunc("/api/gdb-service/code/runGroup", func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"code":200,"message":"","data":%s}`, c.runData)
			})
			up := httptest.NewServer(mux)
			defer up.Close()
			s := newServer(up.URL, "", "1.2.3.4", "ua", filepath.Join(t.TempDir(), "state.json"), false, "", true, 0, 0)
			sess := s.sessions.newSession()
			sess.set("tok", "user", "1", "n", time.Now().Add(time.Hour))
			s.sessions.put(sess)

			rec := httptest.NewRecorder()
			body := `{"eid":7,"qfn":"第1章-DC01PE49","code":"int main(){}","language":1,"kind":4}`
			req := httptest.NewRequest(http.MethodPost, "/api/run", strings.NewReader(body))
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.sid})
			s.handleRun(rec, req)
			if rec.Code != 200 {
				t.Fatalf("http=%d body=%s", rec.Code, rec.Body.String())
			}
			var got struct {
				Passed bool `json:"passed"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("响应解析失败: %v body=%s", err, rec.Body.String())
			}
			if got.Passed != c.wantPassed {
				t.Fatalf("passed=%v, 期望 %v (body=%s)", got.Passed, c.wantPassed, rec.Body.String())
			}
		})
	}
}
