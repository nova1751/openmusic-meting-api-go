package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/auth"
	"github.com/nova1751/openmusic-meting-api-go/internal/legacy"
	"github.com/nova1751/openmusic-meting-api-go/internal/music"
)

type Source struct {
	Provider music.Provider
	Accounts *music.AccountPool
}

type Audio interface {
	http.Handler
	Issue(music.Link) (url.Values, error)
}

type Options struct {
	Observe        func(RequestObservation)
	AdminPage      func(string) bool
	LoudnessURL    func() string
	LoudnessClient *http.Client
	Contributions  func(http.ResponseWriter, *http.Request, []byte)
	Verifier       *auth.Verifier
	Sources        map[string]Source
	Audio          Audio
	Legacy         *legacy.Backend
	Admin          http.Handler
	PublicBase     *url.URL
	Timeout        time.Duration
	Logger         *slog.Logger
}

type Server struct{ Options }

func New(options Options) *Server { return &Server{options} }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if s.Admin != nil && s.AdminPage != nil && r.Method == http.MethodGet && s.AdminPage(path) {
		s.Admin.ServeHTTP(w, r)
		return
	}

	if s.Admin != nil && (path == "/admin" || strings.HasPrefix(path, "/admin/")) &&
		path != "/admin/openmusic" && path != "/admin/fm" && !contributionPath(path) {
		s.Admin.ServeHTTP(w, r)
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if path == legacy.BridgePrefix || strings.HasPrefix(path, legacy.BridgePrefix+"/") {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Range, X-OpenMusic-Assertion, X-OpenMusic-Signature, X-OpenMusic-Cookie, X-Auth-Username, X-Auth-Token")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if path == "/livez" || path == "/readyz" {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			fail(w, 405, "METHOD_NOT_ALLOWED", "此接口支持 GET 和 HEAD")
			return
		}
		if path == "/readyz" && s.Legacy != nil {
			if !s.Legacy.Ready(r.Context()) {
				fail(w, 503, "NOT_READY", "兼容服务尚未就绪")
				return
			}
		}
		jsonResponse(w, 200, map[string]string{"status": "ok"})
		return
	}
	if s.Legacy != nil && !s.useNative(r) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		s.Legacy.Proxy.ServeHTTP(w, r.WithContext(ctx))
		return
	}
	switch path {
	case "/":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		jsonResponse(w, 200, map[string]any{"name": "OpenMusic Meting API Go", "api": "/api", "platforms": slices.Sorted(maps.Keys(s.Sources))})
	case "/api", "/test", "/admin/openmusic", "/admin/fm", "/audio/qishui", "/admin/contribute", "/admin/contributions", "/admin/contributions/revoke":
		s.programmatic(w, r)
	default:
		fail(w, 404, "NOT_FOUND", "接口不存在")
	}
}

func (s *Server) useNative(r *http.Request) bool {
	if !s.Legacy.CanAuthorize() || r.URL.EscapedPath() != "/api" ||
		r.Method != http.MethodGet {
		return false
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q["server"]) > 1 || len(q["type"]) > 1 {
		return false
	}
	req := publicRequest(q)
	return req.Platform == "netease" && music.Supports(req.Platform, req.Type) &&
		req.Type != "url" && req.Type != "fm"
}

func contributionPath(path string) bool {
	return path == "/admin/contribute" || path == "/admin/contributions" || path == "/admin/contributions/revoke"
}

func (s *Server) programmatic(w http.ResponseWriter, r *http.Request) {
	w, finish := s.observe(w, r)
	defer finish()
	w.Header().Set("Cache-Control", "no-store, private")
	path := r.URL.Path
	write := path == "/admin/fm" || path == "/admin/contribute" || path == "/admin/contributions/revoke"
	if (write && r.Method != http.MethodPost) || (!write && r.Method != http.MethodGet && r.Method != http.MethodHead) {
		if write {
			w.Header().Set("Allow", "POST")
		} else {
			w.Header().Set("Allow", "GET, HEAD")
		}
		fail(w, 405, "METHOD_NOT_ALLOWED", "请求方法不受支持")
		return
	}

	timeout := s.Timeout
	if path == "/audio/qishui" {
		timeout = max(timeout, 65*time.Second)
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	r = r.WithContext(ctx)
	var cookie string
	var body []byte
	if s.Legacy != nil {
		selected, result, err := s.Legacy.Authorize(r)
		if err != nil {
			s.upstreamError(w, path, "", err)
			return
		}
		if result != nil {
			jsonResponse(w, result.Status, result.Body)
			return
		}
		cookie = selected
	} else {
		if !s.Verifier.Preflight(r) {
			authError(w, auth.Required)
			return
		}
		if write {
			_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(10 * time.Second))
			var err error
			body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
			if err != nil {
				switch {
				case errors.As(err, new(*http.MaxBytesError)):
					fail(w, 413, "REQUEST_BODY_TOO_LARGE", "请求体超过 1 MiB")
				case isTimeout(err):
					fail(w, 408, "REQUEST_BODY_TIMEOUT", "读取请求体超时")
				default:
					fail(w, 400, "INVALID_REQUEST", "读取请求体失败")
				}
				return
			}
		}
		if code := s.Verifier.Verify(r, body); code != "" {
			authError(w, code)
			return
		}
		cookie = strings.TrimSpace(r.Header.Get("X-OpenMusic-Cookie"))
	}
	switch path {
	case "/test":
		jsonResponse(w, 200, map[string]string{"status": "ok", "api": "/api"})
	case "/audio/qishui":
		if s.Audio == nil {
			fail(w, 501, "PLATFORM_NOT_CONFIGURED", "汽水音频服务未配置")
			return
		}
		s.Audio.ServeHTTP(w, r)
	case "/admin/fm":
		s.roomFM(w, r, body)
	case "/admin/contribute", "/admin/contributions", "/admin/contributions/revoke":
		if s.Contributions == nil {
			fail(w, 501, "PLATFORM_NOT_CONFIGURED", "共享账号没有可用的数据目录")
			return
		}
		s.Contributions(w, r, body)
	case "/api", "/admin/openmusic":
		s.lookup(w, r, cookie)
	}
}

func (s *Server) lookup(w http.ResponseWriter, r *http.Request, cookie string) {
	room := r.URL.Path == "/admin/openmusic"
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		fail(w, 400, "INVALID_MUSIC_REQUEST", "查询参数格式无效")
		return
	}
	req := publicRequest(q)
	if room {
		req.Platform, req.Type, req.ID = normalized(q.Get("server")), normalized(q.Get("type")), strings.TrimSpace(q.Get("id"))
		req.Redirect = false
	}
	req.Cookie = cookie
	if req.Type == "lrc" && req.Format == "json" && !music.LyricID.MatchString(req.ID) {
		jsonResponse(w, 400, map[string]string{"error": "invalid lyric id", "code": "INVALID_LYRIC_ID"})
		return
	}
	if !music.Supports(req.Platform, req.Type) {
		if room {
			fail(w, 400, "INVALID_MUSIC_REQUEST", "房间私有接口参数无效")
		} else {
			badParam(w, "server 参数不合法", map[string]string{"server": req.Platform, "type": req.Type, "id": req.ID})
		}
		return
	}
	if !music.ValidQuality(req.Platform, req.Quality) {
		badParam(w, "quality 参数不合法", map[string]string{"server": req.Platform, "quality": req.Quality})
		return
	}
	req.Page, err = pagination(q, req)
	if err != nil {
		if room {
			fail(w, 400, "INVALID_PAGINATION", err.Error())
		} else {
			badParam(w, "分页参数不合法：limit 为 1-100，page 从 1 开始，offset 从 0 开始", nil)
		}
		return
	}
	if room && req.Cookie == "" {
		fail(w, 400, "MUSIC_COOKIE_REQUIRED", "房间音源登录凭证为空")
		return
	}
	source, ok := s.Sources[req.Platform]
	if !ok {
		fail(w, 501, "PLATFORM_NOT_CONFIGURED", "此平台未配置")
		return
	}
	var result music.Result
	if s.Legacy != nil || room {
		result, err = source.Provider.Lookup(r.Context(), req)
	} else {
		result, err = source.Accounts.Lookup(r.Context(), req, source.Provider)
	}
	if err != nil {
		s.upstreamError(w, r.URL.Path, req.Platform, err)
		return
	}
	if req.Type == "url" && result.Link.URL != "" {
		if err := s.wrapMedia(r, req.Platform, &result.Link); err != nil {
			s.upstreamError(w, r.URL.Path, req.Platform, err)
			return
		}
		if !room && !req.Redirect {
			s.augmentLoudness(r, source.Provider, req, &result)
		}
	}
	if room && !(req.Type == "lrc" && req.Format == "json") {
		writeRoom(w, req.Type, result)
		return
	}
	s.writeResult(w, r, req, result)
}

func normalized(v string) string { return strings.ToLower(strings.TrimSpace(v)) }

func (s *Server) roomFM(w http.ResponseWriter, r *http.Request, body []byte) {
	var input struct {
		Platform, Cookie, Mode string
		ExcludeIDs             []json.RawMessage
	}
	if json.Unmarshal(body, &input) != nil {
		fail(w, 400, "INVALID_REQUEST", "请求格式无效")
		return
	}
	platform := valueOr(normalized(input.Platform), "netease")
	cookie := strings.TrimSpace(input.Cookie)
	if !music.KnownPlatform(platform) || cookie == "" {
		fail(w, 400, "INVALID_FM_REQUEST", "漫游平台或登录凭证无效")
		return
	}
	source, ok := s.Sources[platform]
	if !ok {
		fail(w, 501, "PLATFORM_NOT_CONFIGURED", "此平台未配置")
		return
	}
	req := music.Request{Platform: platform, Type: "fm", Cookie: cookie, ID: strings.TrimSpace(input.Mode)}
	batches := 1
	excluded := make(map[string]bool)
	if platform == "qishui" {
		batches = 3
		for _, raw := range input.ExcludeIDs {
			var id string
			if json.Unmarshal(raw, &id) != nil {
				var number json.Number
				if json.Unmarshal(raw, &number) != nil {
					continue
				}
				id = number.String()
			}
			excluded[strings.TrimSpace(id)] = true
		}
	}
	songs := []music.Item{}
	for range batches {
		result, err := source.Provider.Lookup(r.Context(), req)
		if err != nil {
			status, code, message := classify(err)
			jsonResponse(w, status, map[string]any{"success": false, "code": code, "error": message, "data": []music.Item{}})
			return
		}
		if platform != "qishui" {
			songs = nonNilItems(result.Items)
			break
		}
		for _, item := range result.Items {
			if !excluded[item.ID] {
				songs = append(songs, item)
			}
		}
		if len(songs) > 0 {
			rand.Shuffle(len(songs), func(i, j int) { songs[i], songs[j] = songs[j], songs[i] })
			break
		}
	}
	jsonResponse(w, 200, map[string]any{"success": true, "data": songs})
}

func jsonResponse(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func fail(w http.ResponseWriter, status int, code, message string) {
	jsonResponse(w, status, map[string]any{"success": false, "code": code, "error": message})
}

func badParam(w http.ResponseWriter, message string, param map[string]string) {
	response := map[string]any{"status": 400, "message": message}
	if param != nil {
		response["param"] = param
	}
	jsonResponse(w, 400, response)
}

func authError(w http.ResponseWriter, code string) {
	status := 401
	if code == auth.Busy {
		status = 503
	}
	fail(w, status, code, "需要 OpenMusic 服务端授权，请登录 OpenMusic 后重试")
}

func isTimeout(err error) bool {
	var network net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network) && network.Timeout()
}

func classify(err error) (int, string, string) {
	switch {
	case errors.Is(err, music.ErrInvalidID):
		return 400, "INVALID_MUSIC_REQUEST", "资源 ID 无效"
	case errors.Is(err, music.ErrVerificationRequired):
		return 422, "MUSIC_VERIFICATION_REQUIRED", "账号需要完成上游平台验证"
	case errors.Is(err, music.ErrAccountInvalid):
		return 422, "MUSIC_ACCOUNT_INVALID", "账号已无法使用，请重新登录"
	case errors.Is(err, music.ErrNoAccount):
		return 503, "MUSIC_ACCOUNT_UNAVAILABLE", "音源暂无可用账号，请稍后重试"
	case errors.Is(err, music.ErrNoURL):
		return 403, "MUSIC_URL_UNAVAILABLE", "未获得可用地址"
	case isTimeout(err):
		return 504, "UPSTREAM_TIMEOUT", "音源上游请求超时，请稍后重试"
	default:
		return 502, "UPSTREAM_REQUEST_FAILED", "音源上游请求失败，请稍后重试"
	}
}

func (s *Server) upstreamError(w http.ResponseWriter, path, platform string, err error) {
	status, code, message := classify(err)
	s.Logger.Warn("music request failed", "path", path, "platform", platform, "code", code, "status", status)
	fail(w, status, code, message)
}
