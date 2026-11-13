// gdb-service 通用镜像: /api/gdb/{oj|lab|cd}/<code|debug|exercise>/<...>
// 1:1 复刻原版前端 api/modules/xdb/{Oj,Lab}.js 的请求形状(直接抄, 不自造协议):
//   - oj → /gdb-service/<path>;  lab → /gdb-service/lab/<path>;  cd → /gdb-service/courseDesign/<path>
//   - 默认 multipart 表单, 字段按原版 fn() 规则拍平(嵌套 a.b / 数组 a.[0]); ?mode=json 时 JSON 直传
//     (原版: OJ 全表单; lab 的 compile/run/start/updateBreakPoint/setWatchPoint/saveCode 是 JSON)
//   - Eid 头自动取 body/query 的 eID 字段(同原版 headers:{Eid: content.eID}), 没有就不带
//   - cd 变体自动注入 courseDesignExerciseId = labExerciseId(同原版 Lab.js)
//
// 端点与载荷出处: fuckView analysis/06 + student.js sourcemap(store/actions.js、
// coding-button-bar/debug-button.vue、editor-pane.vue、watch-pane.vue、debug-button-bar.vue)。
// relay 只做协议翻译, 不解释 payload; 响应原样透传({code,message,data} 信封由前端按原版逻辑消费)。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

func (s *server) handleGdb(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/gdb/")
	variant, path, ok := strings.Cut(rest, "/")
	if !ok || path == "" {
		fail(w, 400, "路径须为 /api/gdb/{oj|lab|cd}/<code|debug|exercise>/...")
		return
	}
	var prefix string
	switch variant {
	case "oj":
	case "lab":
		prefix = "lab/"
	case "cd", "courseDesign":
		prefix = "courseDesign/"
	default:
		fail(w, 400, "未知变体: "+variant+"(oj|lab|cd)")
		return
	}
	switch strings.SplitN(path, "/", 2)[0] {
	case "code", "debug", "exercise":
	default:
		fail(w, 400, "只镜像 gdb-service 的 code/debug/exercise")
		return
	}

	if s.mock {
		s.mockGdb(w, r, path)
		return
	}
	sess := s.sessions.current(r)
	if sess == nil || !sess.loggedIn() {
		fail(w, 401, "未登录")
		return
	}

	query := r.URL.Query()
	jsonMode := query.Get("mode") == "json"
	query.Del("mode")
	var (
		body        []byte
		contentType string
		extra       map[string]string
	)

	if r.Method == http.MethodGet {
		if prefix == "courseDesign/" {
			if q := query.Get("labExerciseId"); q != "" && query.Get("courseDesignExerciseId") == "" {
				query.Set("courseDesignExerciseId", q)
			}
		}
		if eid := query.Get("eID"); eid != "" {
			extra = map[string]string{"Eid": eid}
		}
	} else {
		dec := json.NewDecoder(io.LimitReader(r.Body, 4<<20))
		dec.UseNumber()
		var payload map[string]any
		if err := dec.Decode(&payload); err != nil {
			fail(w, 400, "body 须为 JSON 对象(原版 content): "+err.Error())
			return
		}
		// Eid 头只收标量(同原版 axios 对 null/undefined 头的省略行为), 对象/数组不带
		if eid := eidScalar(payload["eID"]); eid != "" {
			extra = map[string]string{"Eid": eid}
		}
		if prefix == "courseDesign/" {
			if lid, ok := payload["labExerciseId"]; ok {
				if _, has := payload["courseDesignExerciseId"]; !has {
					payload["courseDesignExerciseId"] = lid
				}
			}
		}
		var err error
		if jsonMode {
			if body, err = json.Marshal(payload); err != nil {
				fail(w, 500, err.Error())
				return
			}
			contentType = "application/json"
		} else {
			if contentType, body, err = encodeMultipart(flatten(payload, "")); err != nil {
				fail(w, 500, err.Error())
				return
			}
		}
	}

	status, respBody, err := s.doUpstreamHdr(r.Context(), r.Method, "/gdb-service/"+prefix+path, query, body, contentType, extra, sess.Token())
	if err != nil {
		failUpstream(w, err)
		return
	}
	if wafHit(status, respBody) {
		rem, _ := s.lim.cooling()
		failCooldown(w, rem) // WAF 拦截页原样透传只会把 HTML 汤喂给前端
		return
	}
	sess.markInvalid(status, respBody)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(respBody)
}

// eidScalar: eID 字段的标量字符串形式; null/对象/数组返回 ""(不带 Eid 头)。
func eidScalar(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number, bool:
		return fmt.Sprint(t)
	}
	return ""
}

// flatten: 原版 xdb/utils.js fn() 的 Go 版。嵌套对象拍平成 a.b 点路径, 数组拍平成
// a.[0](格式与原版逐字一致), 空数组/空对象按原版记 "[]"/"{}"。键按字典序输出(multipart
// 字段顺序不影响 Spring 绑定, 与原版仅差顺序)。
func flatten(v any, prefix string) [][2]string {
	var out [][2]string
	walk(v, prefix, &out)
	return out
}

func walk(v any, prop string, out *[][2]string) {
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 0 {
			if prop != "" {
				*out = append(*out, [2]string{prop, "{}"})
			}
			return
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			child := k
			if prop != "" {
				child = prop + "." + k
			}
			walk(t[k], child, out)
		}
	case []any:
		if len(t) == 0 {
			if prop != "" {
				*out = append(*out, [2]string{prop, "[]"})
			}
			return
		}
		for i, item := range t {
			walk(item, fmt.Sprintf("%s.[%d]", prop, i), out)
		}
	case nil:
		*out = append(*out, [2]string{prop, ""})
	case json.Number:
		*out = append(*out, [2]string{prop, t.String()})
	case string:
		*out = append(*out, [2]string{prop, t})
	case bool:
		if t {
			*out = append(*out, [2]string{prop, "true"})
		} else {
			*out = append(*out, [2]string{prop, "false"})
		}
	default:
		*out = append(*out, [2]string{prop, fmt.Sprint(t)})
	}
}
