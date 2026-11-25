package nativeadmin

import (
	"context"
	"net/http"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/migrate"
	"github.com/nova1751/openmusic-meting-api-go/internal/music"
)

func (a *Admin) tencentChallenges(w http.ResponseWriter) {
	items, err := migrate.List(a.dataDir)
	if accountError(w, err) {
		return
	}
	data := make([]map[string]any, 0)
	if a.tencent != nil {
		for _, item := range items {
			if item.Platform != "tencent" {
				continue
			}
			_, cookie, err := migrate.AccountCredential(a.dataDir, a.keyFile, a.keyValue, item.ID)
			if err != nil {
				continue
			}
			pending, ok := a.tencent.Challenge(cookie)
			if !ok {
				continue
			}
			data = append(data, map[string]any{"id": item.ID, "nickname": item.Nickname, "note": item.Note,
				"songmid": pending.SongMID, "confirmed": true, "tencentCooldownUntil": pending.Expires.UnixMilli()})
		}
	}
	reply(w, 200, map[string]any{"success": true, "data": data})
}

func (a *Admin) tencentVerification(w http.ResponseWriter, r *http.Request, s *session, id, action string) {
	if a.qqBrowser == nil || a.tencent == nil {
		reject(w, 501, "QQ 验证服务未配置")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch action {
	case "verification/start", "retry-play":
		if r.Method != http.MethodPost {
			reject(w, 405, "请求方法无效")
			return
		}
		platform, cookie, err := migrate.AccountCredential(a.dataDir, a.keyFile, a.keyValue, id)
		if accountError(w, err) {
			return
		}
		pending, ok := a.tencent.Challenge(cookie)
		if platform != "tencent" || !ok {
			reject(w, 400, "没有待处理的 QQ 音乐官方验证")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		if action == "retry-play" {
			result, err := a.tencent.Lookup(ctx, music.Request{Platform: "tencent", Type: "url", ID: pending.SongMID, Cookie: cookie, Quality: "standard"})
			if err != nil || result.Link.URL == "" {
				reply(w, 200, map[string]any{"success": false, "error": "播放仍需验证或无可用播放链接"})
				return
			}
			if s.qqWindow != nil {
				s.qqWindow.Close()
				s.qqWindow = nil
			}
			reply(w, 200, map[string]any{"success": true})
			return
		}
		if s.qqWindow != nil {
			s.qqWindow.Close()
			s.qqWindow = nil
		}
		window, err := a.qqBrowser.Open(ctx, cookie, pending)
		if err != nil {
			reject(w, 502, "无法启动 QQ 验证窗口，请确认 Chromium 可用")
			return
		}
		s.qqWindow, s.qqWindowID = window, id
		reply(w, 200, map[string]any{"success": true})
	case "verification/frame", "verification/pointer", "verification/close":
		window := s.qqWindow
		if window == nil || s.qqWindowID != id || action != "verification/close" && !window.Active() {
			reject(w, 404, "验证窗口已关闭")
			return
		}
		switch {
		case action == "verification/frame" && r.Method == http.MethodGet:
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			image, err := window.Frame(ctx)
			if err != nil {
				reject(w, 404, "验证窗口已关闭")
				return
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(image)
		case action == "verification/pointer" && r.Method == http.MethodPost:
			var point struct {
				Type string
				X, Y float64
			}
			ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			defer cancel()
			if decode(r, &point) != nil || window.Pointer(ctx, point.Type, point.X, point.Y) != nil {
				reject(w, 400, "鼠标事件无效")
				return
			}
			reply(w, 200, map[string]any{"success": true})
		case action == "verification/close" && r.Method == http.MethodPost:
			window.Close()
			s.qqWindow = nil
			reply(w, 200, map[string]any{"success": true})
		default:
			reject(w, 405, "请求方法无效")
		}
	default:
		reject(w, 404, "接口不存在")
	}
}
