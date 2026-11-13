package main

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSPAHandlerCacheHeadersAndFallback(t *testing.T) {
	webDir := t.TempDir()
	nuxtDir := filepath.Join(webDir, "_nuxt")
	if err := os.MkdirAll(nuxtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte("<html><body>SPA</body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	jsBody := strings.Repeat("console.log('chunk');\n", 30)
	if err := os.WriteFile(filepath.Join(nuxtDir, "app.abc123.js"), []byte(jsBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webDir, "favicon.ico"), []byte("ico"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := newServer("http://127.0.0.1:1", "", "1.2.3.4", "ua", filepath.Join(t.TempDir(), "state.json"), true, "", false, 20, time.Minute)
	h := s.handler(webDir, "")

	// 1. / 应带 no-cache 并返回 index.html
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-cache" || !strings.Contains(rec.Body.String(), "SPA") {
		t.Fatalf("/ 响应异常: code=%d cc=%q body=%q", rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
	}

	// 2. SPA fallback (/login) 同样带 no-cache 并返回 index.html
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-cache" || !strings.Contains(rec.Body.String(), "SPA") {
		t.Fatalf("/login fallback 响应异常: code=%d cc=%q body=%q", rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
	}

	// 3. /_nuxt/* 静态资源带 immutable 长缓存
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_nuxt/app.abc123.js", nil))
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("/_nuxt/* Cache-Control 异常: code=%d cc=%q", rec.Code, rec.Header().Get("Cache-Control"))
	}

	// 4. 缺失的 /_nuxt/* 应 404，不得回退 index.html
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_nuxt/missing.js", nil))
	if rec.Code != 404 {
		t.Fatalf("缺失的 /_nuxt/* 应 404, got %d", rec.Code)
	}

	// 5. 未知 /api/* 应返回 404 JSON，不得回退 index.html
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/nonexistent", nil))
	if rec.Code != 404 || !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("未知 /api/* 应 404 JSON, got code=%d ct=%q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestSPAHandlerMissingWebDir(t *testing.T) {
	s := newServer("http://127.0.0.1:1", "", "1.2.3.4", "ua", filepath.Join(t.TempDir(), "state.json"), true, "", false, 20, time.Minute)
	h := s.handler(filepath.Join(t.TempDir(), "missing"), "")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("缺失 -web 目录应返回 503, got %d", rec.Code)
	}
}

func TestGzipMiddleware(t *testing.T) {
	large := strings.Repeat(`{"key":"value","nested":[1,2,3,4]}`, 30)
	small := `{"ok":true}`
	pngBytes := make([]byte, 512)
	copy(pngBytes, "\x89PNG\r\n\x1a\n")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /large", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(large))
	})
	mux.HandleFunc("GET /small", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(small))
	})
	mux.HandleFunc("GET /img", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	})
	mux.HandleFunc("GET /notmod", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	})
	h := gzipMiddleware(mux)

	// 1. 大 JSON + Accept-Encoding: gzip → 压缩
	req := httptest.NewRequest(http.MethodGet, "/large", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("大 JSON 应启用 gzip, got ce=%q", rec.Header().Get("Content-Encoding"))
	}
	gr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("gzip 解压初始化失败: %v", err)
	}
	got, err := io.ReadAll(gr)
	_ = gr.Close()
	if err != nil || string(got) != large {
		t.Fatalf("gzip 解压内容不一致: err=%v len=%d", err, len(got))
	}

	// 2. q=0 不压缩
	req = httptest.NewRequest(http.MethodGet, "/large", nil)
	req.Header.Set("Accept-Encoding", "gzip;q=0")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != large {
		t.Fatalf("gzip;q=0 不应压缩: ce=%q", rec.Header().Get("Content-Encoding"))
	}

	// 3. 小响应 (<256B) 不压缩
	req = httptest.NewRequest(http.MethodGet, "/small", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != small {
		t.Fatalf("小响应不应压缩: ce=%q body=%q", rec.Header().Get("Content-Encoding"), rec.Body.String())
	}

	// 4. 图片二进制不压缩
	req = httptest.NewRequest(http.MethodGet, "/img", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "" || len(rec.Body.Bytes()) != len(pngBytes) {
		t.Fatalf("PNG 不应压缩: ce=%q len=%d", rec.Header().Get("Content-Encoding"), len(rec.Body.Bytes()))
	}

	// 5. 304 透传
	req = httptest.NewRequest(http.MethodGet, "/notmod", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified || rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("304 应原样透传: code=%d ce=%q", rec.Code, rec.Header().Get("Content-Encoding"))
	}
}

func TestCORSMiddlewareAndHealthDiagnostics(t *testing.T) {
	s := newServer("https://anyview.gdut.edu.cn", "", "1.2.3.4", "ua", filepath.Join(t.TempDir(), "state.json"), true, "", false, 20, time.Minute)
	h := s.handler("", "https://app.example.com")

	// OPTIONS 预检
	req := httptest.NewRequest(http.MethodOptions, "/api/health", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "https://app.example.com" {
		t.Fatalf("CORS 预检失败: code=%d acao=%q", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}

	// 未授权 Origin 不下发 ACAO
	req = httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("未授权 Origin 不应返回 ACAO")
	}

	// /api/health 诊断字段齐全
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("health JSON 解析失败: %v", err)
	}
	for _, k := range []string{"ok", "mock", "upstream", "uptimeSec", "limiter", "sessions", "cache", "web"} {
		if _, ok := payload[k]; !ok {
			t.Fatalf("/api/health 缺少诊断字段 %q: %v", k, payload)
		}
	}
}
