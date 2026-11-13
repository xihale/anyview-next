// anyview-next relay: AnyView 的个人中继后端。
// 边界原则: 凡上游会话键控的东西归 relay(token/XFF 会话一致性、exam 计时器、
// gdb 会话生命周期、上游协议形状), 凡 UI 状态归 web; 前端永不接触上游。
// 不是"relay 越薄越好"——exam 计时这类需要常驻连接的有状态服务就该住这层。
// 职责(全部收敛在这一个二进制):
//   - 登录(RSA 流程)与多账号并发会话: 每个浏览器一份上游 token 状态(an_sid
//     cookie 键控, 详见 sessions.go), 登录/登出/失效只影响当前浏览器
//   - 通用 API 透传(自动注入 Token/XFF/UA, 会话绑定 XFF 由 relay 统一保证)
//   - exam WS 计时(401 计时对 + 117 心跳 + 403 重连), 前端两个 REST 调用即可挂时长
//   - 编译/判题/喂输入管线(multipart + base64 码)
//   - 题面/样板/题库缓存池(先回缓存+后台校验+save/compile 写穿, 上游挂了也能看题)
//   - 上游限速与 WAF 防封(滚动 60s 窗口 -rpm 起/间隔自动拉开, 命中拦截页全局静默冷却,
//     详见 limiter.go——云端 WAF 几十个/分钟即拉黑, 封禁期内继续请求会续期)
//   - gdb-service 通用镜像(/api/gdb/{oj|lab|cd}/*, 1:1 复刻原版 xdb/{Oj,Lab}.js 请求形状;
//     OJ 调试端上游已下线, lab 变体仍在)。
//     relay 不内置执行引擎(不 spawn gdb/g++), 本地调试是前端的活, 不在这层做。
//   - -mock 离线假上游(前端开发/截图用), -web 托管 SPA 静态产物(生产免 CORS)
//
// 上游协议出处: fuckView analysis/ 逆向文档(2026-09-07 实测)。
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8787", "监听地址")
	upstream := flag.String("upstream", "https://anyview.gdut.edu.cn", "上游基址")
	dialAddr := flag.String("dial", "", "可选: 强制连接地址 ip:port(SNI/Host 仍用上游域名, 绕 DNS/WAF)")
	xff := flag.String("xff", "183.236.19.198", "固定 X-Forwarded-For(登录与会话必须一致)")
	ua := flag.String("ua", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36", "User-Agent")
	stateFile := flag.String("state", "relay-state.json", "会话状态文件(含上游 token, 勿入库; -mock 模式不读写)")
	cacheOn := flag.Bool("cache", true, "题面/样板/题库缓存池(先回缓存+后台校验, 详见 cache.go)")
	cacheFile := flag.String("cacheFile", "relay-cache.json", "缓存落盘文件(重启/断网后题库仍可用; 置空则仅内存)")
	rpm := flag.Int("rpm", 20, "发往上游的请求预算: 滚动 60s 窗口内最多放行数(0=不限), 相邻请求自动按 60s/rpm 拉开间隔。云端 WAF 实测几十个/分钟即拉黑, 默认 20 留余量; 8s 间隔(=-rpm 7)为实测安全节奏")
	wafCooldown := flag.Duration("wafCooldown", 30*time.Minute, "命中 WAF 拦截页(403 wzws/429)后的全局静默时长: 封禁期内继续请求会续期, 实测需静默 ≥30m")
	webDir := flag.String("web", "", "可选: 托管 SPA 静态目录(生产模式, 免 CORS)")
	corsOrigin := flag.String("cors", "", "可选: 允许跨域的 Origin(逗号分隔或 *, 默认空=同源部署无需开启)")
	mock := flag.Bool("mock", false, "离线假上游模式(不碰真实 AnyView)")
	flag.Parse()

	s := newServer(*upstream, *dialAddr, *xff, *ua, *stateFile, *mock, *cacheFile, *cacheOn, *rpm, *wafCooldown)

	webStatus := "disabled"
	if *webDir != "" {
		if checkWebDir(*webDir) {
			webStatus = *webDir + " (ready)"
		} else {
			webStatus = *webDir + " (missing index.html)"
			log.Printf("[web] 警告: 静态目录 %q 不存在或缺少 index.html (请先运行 make web-build 或 cd web && bun run generate)", *webDir)
		}
	}

	cs := s.cache.stats()
	log.Printf("anyview-next relay @ http://%s  upstream=%s dial=%q mock=%v rpm=%d wafCooldown=%s sessions=%d cache=(enabled:%v entries:%d) web=%s",
		*listen, s.base.String(), *dialAddr, *mock, *rpm, *wafCooldown, s.sessions.count(), cs.Enabled, cs.Entries, webStatus)

	srv := &http.Server{
		Addr:              *listen,
		Handler:           s.handler(*webDir, *corsOrigin),
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      300 * time.Second, // work/run 全链路(编译+判题+喂输入)最长约 3×60s
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		err := srv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		s.shutdown()
		if err != nil {
			log.Fatal(err)
		}
	case <-ctx.Done():
		log.Printf("[relay] 收到退出信号, 正在优雅停机...")
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			log.Printf("[relay] HTTP 关闭超时: %v", err)
		}
		s.shutdown()
		<-serveErr
		log.Printf("[relay] 停机完成")
	}
}

func (s *server) handler(webDir, corsOrigin string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("GET /api/auth/status", s.handleAuthStatus)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	mux.HandleFunc("POST /api/exam/tick", s.handleExamTick)
	mux.HandleFunc("GET /api/exam/status", s.handleExamStatus)
	mux.HandleFunc("POST /api/work/run", s.handleRun)
	mux.HandleFunc("POST /api/work/save", s.handleSave)
	mux.HandleFunc("/api/gdb/", s.handleGdb)
	mux.HandleFunc("/api/up/", s.handleProxy)
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"ok":        true,
			"mock":      s.mock,
			"upstream":  s.base.String(),
			"uptimeSec": int64(time.Since(s.startedAt).Seconds()),
			"limiter":   s.lim.status(),
			"sessions":  s.sessions.count(),
			"cache":     s.cache.stats(),
			"web": map[string]any{
				"configured": webDir != "",
				"ready":      webDir != "" && checkWebDir(webDir),
			},
		})
	})
	if webDir != "" {
		mux.HandleFunc("/", spaHandler(webDir))
	}

	var h http.Handler = gzipMiddleware(mux)
	if strings.TrimSpace(corsOrigin) != "" {
		h = corsMiddleware(corsOrigin, h)
	}
	return h
}

func checkWebDir(dir string) bool {
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return false
	}
	idx, err := os.Stat(filepath.Join(dir, "index.html"))
	return err == nil && !idx.IsDir()
}

// spaHandler: 静态文件 + SPA fallback(未命中路径回 index.html)。
// /_nuxt/* 带哈希不可变长缓存; index.html 必须 no-cache, 否则部署新 SPA 后浏览器拿旧壳+旧 chunk 引用。
func spaHandler(dir string) http.HandlerFunc {
	cleanDir := filepath.Clean(dir)
	fs := http.FileServer(http.Dir(cleanDir))
	indexPath := filepath.Join(cleanDir, "index.html")
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			fail(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		cleanPath := path.Clean("/" + r.URL.Path)
		if cleanPath == "/api" || strings.HasPrefix(cleanPath, "/api/") {
			fail(w, http.StatusNotFound, "API endpoint not found")
			return
		}
		target := filepath.Join(cleanDir, filepath.FromSlash(cleanPath))
		st, err := os.Stat(target)
		if err != nil || st.IsDir() {
			// 缺失的构建产物直接 404, 不拿 index.html 冒充 JS/CSS
			if strings.HasPrefix(cleanPath, "/_nuxt/") {
				http.NotFound(w, r)
				return
			}
			if _, idxErr := os.Stat(indexPath); idxErr != nil {
				http.Error(w, "前端静态资源尚未构建或 -web 目录不可用，请先运行 make web-build (cd web && bun run generate)", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, indexPath)
			return
		}
		switch {
		case strings.HasPrefix(cleanPath, "/_nuxt/"):
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		case cleanPath == "/index.html" || strings.HasSuffix(cleanPath, ".html"):
			w.Header().Set("Cache-Control", "no-cache")
		default:
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		fs.ServeHTTP(w, r)
	}
}

func corsMiddleware(allowSpec string, next http.Handler) http.Handler {
	allowAll := strings.TrimSpace(allowSpec) == "*"
	allowed := map[string]bool{}
	if !allowAll {
		for _, item := range strings.Split(allowSpec, ",") {
			if o := strings.TrimSpace(item); o != "" {
				allowed[o] = true
			}
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && (allowAll || allowed[origin]) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Add("Vary", "Origin")
		}
		if r.Method == http.MethodOptions && origin != "" {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

const gzipThreshold = 256

var gzipWriterPool = sync.Pool{
	New: func() any {
		w, _ := gzip.NewWriterLevel(io.Discard, gzip.DefaultCompression)
		return w
	},
}

var jsonBufPool = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
	},
}

func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead ||
			r.Header.Get("Range") != "" ||
			r.Header.Get("Upgrade") != "" ||
			!acceptsGzip(r.Header.Get("Accept-Encoding")) {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipResponseWriter{w: w, status: http.StatusOK}
		defer gw.finish()
		next.ServeHTTP(gw, r)
	})
}

func acceptsGzip(hdr string) bool {
	if hdr == "" {
		return false
	}
	for _, part := range strings.Split(hdr, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
			continue
		}
		for _, param := range strings.Split(params, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(param), "=")
			if ok && strings.EqualFold(strings.TrimSpace(k), "q") {
				if q, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && q <= 0 {
					return false
				}
			}
		}
		return true
	}
	return false
}

func isCompressibleContentType(ct string) bool {
	if ct == "" {
		return false
	}
	mime, _, _ := strings.Cut(ct, ";")
	mime = strings.TrimSpace(strings.ToLower(mime))
	if strings.HasPrefix(mime, "text/") {
		return true
	}
	switch mime {
	case "application/json", "application/javascript", "application/x-javascript",
		"application/ecmascript", "application/xml", "application/wasm", "image/svg+xml":
		return true
	}
	return false
}

type gzipResponseWriter struct {
	w           http.ResponseWriter
	gz          *gzip.Writer
	buf         []byte
	status      int
	wroteHeader bool
	passthrough bool
}

func (g *gzipResponseWriter) Header() http.Header { return g.w.Header() }

func (g *gzipResponseWriter) WriteHeader(code int) {
	if g.wroteHeader {
		return
	}
	g.status = code
	g.wroteHeader = true
	ct := g.w.Header().Get("Content-Type")
	if code == http.StatusNoContent || code == http.StatusNotModified || code < 200 ||
		g.w.Header().Get("Content-Encoding") != "" ||
		(ct != "" && !isCompressibleContentType(ct)) {
		g.passthrough = true
		g.w.WriteHeader(code)
	}
}

func (g *gzipResponseWriter) Write(p []byte) (int, error) {
	if !g.wroteHeader {
		g.WriteHeader(http.StatusOK)
	}
	if g.passthrough {
		return g.w.Write(p)
	}
	if g.gz != nil {
		return g.gz.Write(p)
	}
	g.buf = append(g.buf, p...)
	if len(g.buf) < gzipThreshold {
		return len(p), nil
	}
	ct := g.w.Header().Get("Content-Type")
	if ct == "" {
		ct = http.DetectContentType(g.buf)
		g.w.Header().Set("Content-Type", ct)
	}
	if !isCompressibleContentType(ct) || g.w.Header().Get("Content-Encoding") != "" {
		g.passthrough = true
		g.w.WriteHeader(g.status)
		_, err := g.w.Write(g.buf)
		g.buf = nil
		if err != nil {
			return 0, err
		}
		return len(p), nil
	}
	g.w.Header().Del("Content-Length")
	g.w.Header().Set("Content-Encoding", "gzip")
	g.w.Header().Add("Vary", "Accept-Encoding")
	g.w.WriteHeader(g.status)
	gz := gzipWriterPool.Get().(*gzip.Writer)
	gz.Reset(g.w)
	g.gz = gz
	_, err := gz.Write(g.buf)
	g.buf = nil
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (g *gzipResponseWriter) Flush() {
	if g.passthrough {
		if fl, ok := g.w.(http.Flusher); ok {
			fl.Flush()
		}
		return
	}
	if g.gz != nil {
		_ = g.gz.Flush()
		if fl, ok := g.w.(http.Flusher); ok {
			fl.Flush()
		}
		return
	}
	if g.wroteHeader {
		g.passthrough = true
		g.w.WriteHeader(g.status)
		if len(g.buf) > 0 {
			_, _ = g.w.Write(g.buf)
			g.buf = nil
		}
		if fl, ok := g.w.(http.Flusher); ok {
			fl.Flush()
		}
	}
}

func (g *gzipResponseWriter) Unwrap() http.ResponseWriter { return g.w }

func (g *gzipResponseWriter) finish() {
	if g.gz != nil {
		_ = g.gz.Close()
		g.gz.Reset(io.Discard)
		gzipWriterPool.Put(g.gz)
		g.gz = nil
		return
	}
	if !g.passthrough && g.wroteHeader {
		g.w.WriteHeader(g.status)
		if len(g.buf) > 0 {
			_, _ = g.w.Write(g.buf)
			g.buf = nil
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	buf := jsonBufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer func() {
		if buf.Cap() <= 1<<20 {
			jsonBufPool.Put(buf)
		}
	}()
	if err := json.NewEncoder(buf).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"code": status, "message": msg})
}
