// Package nativeadmin provides a restricted, first-party account manager for
// standalone deployments. It never exposes music cookies to the browser.
package nativeadmin

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/kugou"
	"github.com/nova1751/openmusic-meting-api-go/internal/migrate"
	"github.com/nova1751/openmusic-meting-api-go/internal/music"
	"github.com/nova1751/openmusic-meting-api-go/internal/netease"
	"github.com/nova1751/openmusic-meting-api-go/internal/qishui"
	"github.com/nova1751/openmusic-meting-api-go/internal/tencent"
	qrcode "github.com/skip2/go-qrcode"
)

//go:embed ui/*
var assets embed.FS

type Admin struct {
	logMu                              sync.Mutex
	requestLogs                        []json.RawMessage
	qqBrowser                          *tencent.VerificationBrowser
	monitorMu                          sync.Mutex
	monitor                            monitorStatus
	dataDir, keyFile, keyValue, origin string
	client                             *netease.Client
	validators                         map[string]music.AccountValidator
	kugou                              *kugou.Client
	tencent                            *tencent.Client
	qishui                             *qishui.QRBrowser
	mu                                 sync.Mutex
	authMu                             sync.Mutex
	writeMu                            sync.Mutex
	credentialMu                       sync.Mutex
	sessions                           map[[32]byte]*session
	attempts                           map[[32]byte]loginAttempt
}

func New(dataDir, keyFile, keyValue, origin string, client *netease.Client, kg *kugou.Client, qq *tencent.Client, qs *qishui.QRBrowser, validators map[string]music.AccountValidator) (*Admin, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" ||
		dataDir == "" || client == nil {
		return nil, errors.New("native admin requires a data directory and HTTPS origin")
	}
	var users map[string]user
	if err := readFileJSON(filepath.Join(dataDir, "users.json"), &users); err != nil {
		return nil, err
	}
	valid := false
	for _, user := range users {
		if user.Role == "admin" && user.Password != "" {
			valid = true
		}
	}
	if !valid {
		return nil, errors.New("native admin requires an existing administrator")
	}
	return &Admin{dataDir: dataDir, keyFile: keyFile, keyValue: keyValue, origin: strings.TrimRight(origin, "/"),
		client: client, kugou: kg, tencent: qq, qishui: qs, validators: validators, sessions: make(map[[32]byte]*session), attempts: make(map[[32]byte]loginAttempt)}, nil
}

func reply(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func reject(w http.ResponseWriter, status int, message string) {
	reply(w, status, map[string]any{"success": false, "error": message})
}

func decode(r *http.Request, value any) error {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		return errors.New("JSON required")
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 8193))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func (a *Admin) getSession(r *http.Request) (*session, [32]byte) {
	cookie, err := r.Cookie("openmusic_admin")
	if err != nil || len(cookie.Value) != 43 {
		return nil, [32]byte{}
	}
	key := sha256.Sum256([]byte(cookie.Value))
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.sessions[key]
	if s == nil || time.Now().After(s.expires) {
		delete(a.sessions, key)
		return nil, key
	}
	return s, key
}

func (a *Admin) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if decode(r, &input) != nil || input.Username == "" || len(input.Username) > 128 ||
		input.Password == "" || len(input.Password) > 1024 || len(input.Code) > 10 {
		reject(w, 400, "请输入有效的登录信息")
		return
	}
	a.credentialMu.Lock()
	defer a.credentialMu.Unlock()
	ok, needsCode, err := a.authenticate(input.Username, input.Password, input.Code)
	if err != nil {
		reject(w, 503, "暂时无法验证管理账号")
		return
	}
	if needsCode {
		reply(w, 200, map[string]any{"success": true, "require2FA": true})
		return
	}
	if !ok {
		reject(w, 401, "登录失败或账号暂时锁定")
		return
	}
	if err := a.backup(); err != nil {
		slog.Warn("music account backup failed", "error", err)
	}
	token, err := secureRandom(32)
	if err != nil {
		reject(w, 503, "无法创建会话")
		return
	}
	csrf, err := secureRandom(32)
	if err != nil {
		reject(w, 503, "无法创建会话")
		return
	}
	entry, err := a.userEntry(input.Username)
	if err != nil || !validRole(entry.Role) {
		reject(w, 401, "会话无法创建")
		return
	}
	key := sha256.Sum256([]byte(token))
	s := &session{role: entry.Role, username: input.Username, csrf: csrf, expires: time.Now().Add(12 * time.Hour)}
	a.mu.Lock()
	for key, item := range a.sessions {
		if time.Now().After(item.expires) {
			delete(a.sessions, key)
		}
	}
	if len(a.sessions) >= 32 {
		a.mu.Unlock()
		reject(w, 503, "管理会话过多")
		return
	}
	a.sessions[key] = s
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "openmusic_admin", Value: token, Path: "/admin",
		MaxAge: 12 * 60 * 60, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	reply(w, 200, map[string]any{"success": true, "csrf": csrf, "role": s.role})
}

func (a *Admin) SetQQVerification(browser *tencent.VerificationBrowser) {
	a.qqBrowser = browser
}

func (a *Admin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Del("Access-Control-Allow-Origin")
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; frame-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	if r.Method != http.MethodGet && r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, 8192)
	}
	if strings.HasPrefix(r.URL.Path, qishuiAssetsRoot) {
		a.qishuiAsset(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, qishuiVerifyRoot) {
		a.qishuiVerify(w, r)
		return
	}
	if r.Method == http.MethodGet {
		if a.PagePath(r.URL.Path) {
			data, _ := assets.ReadFile("ui/index.html")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(data)
			return
		}
		switch r.URL.Path {

		case "/admin/assets/app.js":
			data, _ := assets.ReadFile("ui/app.js")
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			_, _ = w.Write(data)
			return
		case "/admin/assets/style.css":
			data, _ := assets.ReadFile("ui/style.css")
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
			_, _ = w.Write(data)
			return
		}
	}
	if r.Method != http.MethodGet && r.Header.Get("Origin") != a.origin {
		reject(w, 403, "请求来源不允许")
		return
	}
	if r.Method == http.MethodPost && (r.URL.Path == "/admin/api/login" || r.URL.Path == "/admin/login") {
		a.login(w, r)
		return
	}
	s, key := a.getSession(r)
	if s == nil {
		reject(w, 401, "请先登录")
		return
	}
	if r.Method != http.MethodGet && !equal(r.Header.Get("X-CSRF-Token"), s.csrf) {
		reject(w, 403, "请求校验失败")
		return
	}
	if s.role != "admin" && !ordinaryUserPath(r.URL.Path) {
		reject(w, 403, "权限不足")
		return
	}
	switch {
	case r.URL.Path == "/admin/profile" || r.URL.Path == "/admin/users" || strings.HasPrefix(r.URL.Path, "/admin/users/"):
		a.userRoutes(w, r, s, key)
	case r.URL.Path == "/admin/monitor" || strings.HasPrefix(r.URL.Path, "/admin/monitor/"):
		a.monitorRoutes(w, r)
	case r.URL.Path == "/admin/webhook" || strings.HasPrefix(r.URL.Path, "/admin/webhook/"):
		a.webhookRoutes(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/admin/logs":
		a.showLogs(w, r, "logs.json")
	case r.Method == http.MethodGet && r.URL.Path == "/admin/api-logs":
		a.showLogs(w, r, "api_logs.json")
	case r.URL.Path == "/admin/config" || strings.HasPrefix(r.URL.Path, "/admin/config/"):
		a.configRoutes(w, r)
	case r.URL.Path == "/admin/tokens" || strings.HasPrefix(r.URL.Path, "/admin/tokens/"):
		a.tokenRoutes(w, r, s)
	case r.URL.Path == "/admin/cookies" || strings.HasPrefix(r.URL.Path, "/admin/cookies/"):
		a.accountRoutes(w, r, s)
	case r.Method == http.MethodGet && (r.URL.Path == "/admin/api/session" || r.URL.Path == "/admin/check"):
		reply(w, 200, map[string]any{"success": true, "username": s.username, "csrf": s.csrf, "role": s.role})
	case r.Method == http.MethodPost && (r.URL.Path == "/admin/api/logout" || r.URL.Path == "/admin/logout"):
		a.mu.Lock()
		delete(a.sessions, key)
		a.mu.Unlock()
		s.closeQR()
		s.mu.Lock()
		if s.qqWindow != nil {
			s.qqWindow.Close()
			s.qqWindow = nil
		}
		s.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "openmusic_admin", Path: "/admin", MaxAge: -1,
			Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
		reply(w, 200, map[string]any{"success": true})
	case r.Method == http.MethodGet && r.URL.Path == "/admin/api/accounts":
		accounts, err := migrate.List(a.dataDir)
		if err != nil {
			reject(w, 503, "读取账号失败")
			return
		}
		reply(w, 200, map[string]any{"success": true, "accounts": accounts})
	case r.Method == http.MethodGet && r.URL.Path == "/admin/api/2fa":
		enabled, err := a.twoFactorEnabled(s.username)
		if err != nil {
			reject(w, 503, "无法读取双重验证状态")
			return
		}
		reply(w, 200, map[string]any{"success": true, "enabled": enabled})
	case r.Method == http.MethodPost && r.URL.Path == "/admin/api/password":
		a.changePassword(w, r, s, key)
	case r.Method == http.MethodPost && r.URL.Path == "/admin/api/2fa/setup":
		a.setupTwoFactor(w, s)
	case r.Method == http.MethodGet && r.URL.Path == "/admin/api/2fa/image":
		a.twoFactorImage(w, s)
	case r.Method == http.MethodPost && r.URL.Path == "/admin/api/2fa/enable":
		a.enableTwoFactor(w, r, s, key)
	case r.Method == http.MethodPost && r.URL.Path == "/admin/api/2fa/disable":
		a.disableTwoFactor(w, r, s, key)
	case r.Method == http.MethodPost && r.URL.Path == "/admin/api/accounts/active":
		var input struct {
			ID     string `json:"id"`
			Active bool   `json:"active"`
		}
		if decode(r, &input) != nil || input.ID == "" || len(input.ID) > 128 {
			reject(w, 400, "无效的账号")
			return
		}
		a.writeMu.Lock()
		err := migrate.SetActive(a.dataDir, input.ID, input.Active)
		a.writeMu.Unlock()
		if err != nil {
			reject(w, 503, "更新账号失败")
			return
		}
		if err := a.backup(); err != nil {
			slog.Warn("music account backup failed", "error", err)
		}
		reply(w, 200, map[string]any{"success": true})
	case r.Method == http.MethodPost && r.URL.Path == "/admin/api/qr/create":
		a.createQR(w, r, s)
	case r.Method == http.MethodGet && r.URL.Path == "/admin/api/qr/image":
		s.mu.Lock()
		if s.qqQR != nil {
			png := s.qqQR.PNG()
			s.mu.Unlock()
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(png)
			return
		}
		var qrURL string
		if s.qr != nil {
			qrURL = s.qr.URL()
		}
		if s.kugouQR != nil {
			qrURL = s.kugouQR.URL()
		}
		if s.qishuiQR != nil {
			qrURL = s.qishuiQR.URL()
		}
		s.mu.Unlock()
		if qrURL == "" {
			reject(w, 404, "请先创建二维码")
			return
		}
		png, err := qrcode.Encode(qrURL, qrcode.Medium, 256)
		if err != nil {
			reject(w, 500, "无法生成二维码")
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	case r.Method == http.MethodPost && r.URL.Path == "/admin/api/qr/poll":
		a.pollQR(w, r, s)
	default:
		reject(w, 404, "接口不存在")
	}
}

func (a *Admin) pollQR(w http.ResponseWriter, r *http.Request, s *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.qrSaved {
		reply(w, 200, map[string]any{"success": true, "status": "confirmed"})
		return
	}
	if s.kugouQR != nil {
		a.pollKugouQR(w, r, s)
		return
	}
	if s.qishuiQR != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		status, cookie, err := s.qishuiQR.Poll(ctx)
		if err != nil {
			reject(w, 502, "汽水扫码状态查询失败，请重试")
			return
		}
		a.saveQR(w, ctx, s, "qishui", "汽水扫码登录", status, cookie)
		return
	}
	if s.qqQR != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		status, cookie := s.qqQR.Poll()
		a.saveQR(w, ctx, s, "tencent", "QQ 音乐扫码登录", status, cookie)
		return
	}
	if s.qr == nil {
		reject(w, 404, "请先创建二维码")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	status, err := a.client.PollQR(ctx, s.qr)
	if err != nil {
		reject(w, 502, "扫码状态查询失败，请重试")
		return
	}
	if status.Status == "confirmed" {
		profile, err := a.client.ValidateAccount(ctx, status.Cookie)
		if err != nil {
			reject(w, 502, "网易云账号验证失败，请重新扫码")
			return
		}
		a.writeMu.Lock()
		err = migrate.SaveNetease(a.dataDir, a.keyFile, a.keyValue, s.username, status.Cookie, profile)
		a.writeMu.Unlock()
		if err != nil {
			reject(w, 503, "无法保存网易云账号")
			return
		}
		if err := a.backup(); err != nil {
			slog.Warn("music account backup failed", "error", err)
		}
		s.qrSaved = true
		s.qr = nil
	}
	reply(w, 200, map[string]any{"success": true, "status": status.Status})
}

func ordinaryUserPath(path string) bool {
	switch path {
	case "/admin/profile", "/admin/check", "/admin/logout", "/admin/api/session", "/admin/api/logout", "/admin/api/password",
		"/admin/api/2fa", "/admin/api/2fa/setup", "/admin/api/2fa/image", "/admin/api/2fa/enable", "/admin/api/2fa/disable":
		return true
	default:
		return false
	}
}
