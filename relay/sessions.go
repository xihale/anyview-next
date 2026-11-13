// 浏览器会话层: relay 自签 an_sid cookie ↔ 每会话独立持有上游 token。
// 多账号并发的根基: token 状态/exam 计时/个人缓存归属全部挂在会话上, 浏览器
// 之间互不可见; 登录/登出只影响当前浏览器那一份, 不再是全局开关。
// cookie 只是不透明句柄(随机 256bit), 上游真凭据仍只住 relay——前端永不接触上游。
// 同一账号允许在多浏览器同时登录(各持独立会话); 若上游把旧登录顶掉(exam 119),
// 该账号各会话会各自收到失效信号, 属平台侧行为, relay 不模拟也不阻止。
package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	sessionCookieName   = "an_sid"
	sessionCookieMaxAge = 7 * 24 * 3600 // 只需覆盖上游 token 生命(~12h); 真过期由 expired 冒泡弹登录
	// 不设 Secure: 生产经 Caddy 反代且内网走 http, 置了反而全量掉会话
	sessionPruneAfter = 48 * time.Hour // 重启加载时清理超期会话的宽限(给时区/停机留余量)
)

// ===== 单个浏览器会话: 该浏览器的上游 token 状态 + 私有 exam 计时器 =====

type session struct {
	sid string

	mu        sync.Mutex
	token     string
	username  string
	studentID string
	studentNm string
	exp       time.Time
	expired   bool

	exam *examMgr // 该会话私有: 计时对/kicked/WS 连接不跨浏览器共享
}

func (sess *session) Token() string {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.token
}

func (sess *session) Username() string {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.username
}

// cred: (token, username) 原子快照——后台任务凭它保证「抓取用的凭据」与
// 「内容归属账号」永远配对, 不受抓取途中重登录/换号影响。
func (sess *session) cred() (tok, username string) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.token, sess.username
}

func (sess *session) loggedIn() bool { return sess.Token() != "" }

// markInvalid: 上游返回会话失效信号时置位, 前端据此弹登录(只弹这一个浏览器)
func (sess *session) markInvalid(status int, body []byte) {
	if status == 401 || (status == 403 && strings.Contains(string(body), "状态失效")) {
		sess.mu.Lock()
		sess.expired = true
		sess.mu.Unlock()
	}
}

func (sess *session) expiredFlag() bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.expired || (!sess.exp.IsZero() && time.Now().After(sess.exp))
}

// set: 登录成功(含同浏览器换账号)——整体替换会话凭据。缓存按账号隔离、exam
// 归会话私有, 这里不再需要旧实现的「换号整池清空」动作。
func (sess *session) set(tok, username, studentID, studentNm string, exp time.Time) {
	sess.mu.Lock()
	sess.token, sess.username, sess.studentID, sess.studentNm = tok, username, studentID, studentNm
	sess.exp, sess.expired = exp, false
	sess.mu.Unlock()
}

func (sess *session) snapshot() map[string]any {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	info := map[string]any{
		"loggedIn":    sess.token != "",
		"expired":     sess.expired || (!sess.exp.IsZero() && time.Now().After(sess.exp)),
		"username":    sess.username,
		"studentId":   sess.studentID,
		"studentName": sess.studentNm,
	}
	if !sess.exp.IsZero() {
		info["exp"] = sess.exp.Unix()
		info["expInSec"] = int(time.Until(sess.exp).Seconds())
	}
	return info
}

// anonymousSnapshot: 无会话(无/未知 cookie)时 auth/status 的形状, 字段与 snapshot 对齐
func anonymousSnapshot() map[string]any {
	return map[string]any{
		"loggedIn":    false,
		"expired":     false,
		"username":    "",
		"studentId":   "",
		"studentName": "",
	}
}

// ===== 会话存储 =====

// sessionStore: sid → session。落盘使 relay 重启不踢人(cookie 仍有效即恢复)。
// mock 模式 persist=false: 会话仅内存, 永不读写真实文件(同旧 stateStore 边界)。
type sessionStore struct {
	srv     *server // 仅用于给会话接私有 examMgr
	file    string
	persist bool

	mu     sync.RWMutex
	saveMu sync.Mutex // 串行落盘, 不阻塞 current()/Token()
	bySID  map[string]*session
}

func newSessionStore(srv *server, file string, persist bool) *sessionStore {
	ss := &sessionStore{srv: srv, file: file, persist: persist, bySID: map[string]*session{}}
	if persist {
		ss.load()
	}
	return ss
}

// current: 请求 → 会话。无/未知 cookie 返回 nil(调用方 401 或回匿名快照)。
func (ss *sessionStore) current(r *http.Request) *session {
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return nil
	}
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.bySID[c.Value]
}

// put: 入册新会话并落盘。
func (ss *sessionStore) put(sess *session) {
	ss.mu.Lock()
	ss.bySID[sess.sid] = sess
	ss.mu.Unlock()
	ss.save()
}

// ensure: 登录链路专用——复用浏览器已有会话(过期重登/换号), 没有则新建入册。
func (ss *sessionStore) ensure(r *http.Request) *session {
	if sess := ss.current(r); sess != nil {
		return sess
	}
	sess := ss.newSession()
	ss.put(sess)
	return sess
}

// newSession: 生成随机 sid 并接好私有 exam 计时器; 不入册(put/ensure 负责)。
func (ss *sessionStore) newSession() *session {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("会话 ID 熵源不可用: " + err.Error())
	}
	return ss.newSessionWithSID(base64.RawURLEncoding.EncodeToString(b))
}

func (ss *sessionStore) newSessionWithSID(sid string) *session {
	sess := &session{sid: sid}
	sess.exam = newExamMgr(ss.srv, sess)
	return sess
}

// delete: 登出——只摘掉当前浏览器这一份, 其他浏览器/账号不受影响。
func (ss *sessionStore) delete(sess *session) {
	ss.mu.Lock()
	delete(ss.bySID, sess.sid)
	ss.mu.Unlock()
	ss.save()
}

func (ss *sessionStore) count() int {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return len(ss.bySID)
}

// liveOwners: 当前所有已登录会话的用户名集合——缓存磁盘卫生的保留名单
// (仍有人在线的账号, 其个人缓存条目不得被别人的登录清掉)。
func (ss *sessionStore) liveOwners() map[string]bool {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	live := map[string]bool{}
	for _, sess := range ss.bySID {
		if u := sess.Username(); u != "" {
			live[u] = true
		}
	}
	return live
}

// shutdown: 服务退出时停掉全部会话的 exam WS 计时(不删除磁盘会话文件)。
func (ss *sessionStore) shutdown() {
	ss.mu.RLock()
	list := make([]*session, 0, len(ss.bySID))
	for _, sess := range ss.bySID {
		list = append(list, sess)
	}
	ss.mu.RUnlock()
	for _, sess := range list {
		sess.exam.Reset()
	}
}

// ===== cookie =====

func bindSessionCookie(w http.ResponseWriter, sid string) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: sid, Path: "/",
		MaxAge: sessionCookieMaxAge, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/",
		MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

// ===== 落盘 =====

// v2: 多会话(sid → 凭据)。v1 为旧单用户单条记录, 结构不同整文件弃用——
// 旧格式没有会话句柄, 浏览器本来也无 cookie, 升级后重新登录一次即可。
type sessionFileJSON struct {
	Version  int                    `json:"version"`
	SavedAt  int64                  `json:"savedAt"`
	Sessions map[string]sessionJSON `json:"sessions"`
}

type sessionJSON struct {
	Token     string `json:"token"`
	Username  string `json:"username,omitempty"`
	StudentID string `json:"studentId,omitempty"`
	StudentNm string `json:"studentName,omitempty"`
	Exp       int64  `json:"exp,omitempty"`
}

// mockTokenPrefix 沿用 core.go 的哨兵: 真实模式拒载 mock 假 token(整条弃用)
func (ss *sessionStore) load() {
	b, err := os.ReadFile(ss.file)
	if err != nil {
		return
	}
	var j sessionFileJSON
	if json.Unmarshal(b, &j) != nil || j.Version != 2 {
		log.Printf("[auth] %s 是旧版单用户状态文件, 不再兼容——各浏览器请重新登录", ss.file)
		return
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	now := time.Now()
	for sid, sj := range j.Sessions {
		if sj.Token == "" || isMockToken(sj.Token) {
			continue
		}
		if sj.Exp > 0 && now.Sub(time.Unix(sj.Exp, 0)) > sessionPruneAfter {
			continue // 超期会话: 恢复无意义, 顺手清掉
		}
		sess := ss.newSessionWithSID(sid)
		var exp time.Time
		if sj.Exp > 0 {
			exp = time.Unix(sj.Exp, 0)
		}
		sess.set(sj.Token, sj.Username, sj.StudentID, sj.StudentNm, exp)
		ss.bySID[sid] = sess
	}
}

// save: 短锁快照 + saveMu 串行落盘(同 cacheStore/stateStore 手法)。
func (ss *sessionStore) save() {
	if !ss.persist || ss.file == "" {
		return
	}
	ss.saveMu.Lock()
	defer ss.saveMu.Unlock()
	j := sessionFileJSON{Version: 2, SavedAt: time.Now().Unix(), Sessions: map[string]sessionJSON{}}
	ss.mu.RLock()
	for sid, sess := range ss.bySID {
		sess.mu.Lock()
		var exp int64
		if !sess.exp.IsZero() {
			exp = sess.exp.Unix()
		}
		j.Sessions[sid] = sessionJSON{
			Token: sess.token, Username: sess.username,
			StudentID: sess.studentID, StudentNm: sess.studentNm, Exp: exp,
		}
		sess.mu.Unlock()
	}
	file := ss.file
	ss.mu.RUnlock()
	b, err := json.MarshalIndent(&j, "", "  ")
	if err != nil {
		log.Printf("[auth] 会话落盘序列化失败: %v", err)
		return
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		log.Printf("[auth] 会话落盘写入失败 (%s): %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, file); err != nil {
		_ = os.Remove(tmp)
		log.Printf("[auth] 会话落盘原子替换失败 (%s): %v", file, err)
	}
}
