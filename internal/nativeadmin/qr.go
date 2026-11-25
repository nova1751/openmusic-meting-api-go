package nativeadmin

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/migrate"
)

func (a *Admin) createQR(w http.ResponseWriter, r *http.Request, s *session) {
	var input struct{ Platform string }
	if decode(r, &input) != nil {
		reject(w, 400, "二维码请求无效")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clearQR()
	switch input.Platform {
	case "", "netease":
		qr, err := a.client.CreateQR(ctx)
		if err != nil {
			reject(w, 502, "无法创建网易云二维码，请稍后重试")
			return
		}
		s.qr, s.kugouQR, s.qrSaved = qr, nil, false
	case "kugou":
		if a.kugou == nil {
			reject(w, 501, "酷狗扫码服务未配置")
			return
		}
		qr, err := a.kugou.CreateQR(ctx)
		if err != nil {
			reject(w, 502, "无法创建酷狗二维码，请稍后重试")
			return
		}
		s.qr, s.kugouQR, s.qrSaved = nil, qr, false
	case "tencent":
		if a.tencent == nil {
			reject(w, 501, "QQ 音乐扫码服务未配置")
			return
		}
		qr, err := a.tencent.CreateQR(ctx)
		if err != nil {
			reject(w, 502, "无法创建 QQ 音乐二维码，请稍后重试")
			return
		}
		s.qr, s.kugouQR, s.qqQR, s.qrSaved = nil, nil, qr, false
	case "qishui":
		if a.qishui == nil {
			reject(w, 501, "汽水扫码安全组件未配置")
			return
		}
		qr, err := a.qishui.CreateQR(ctx)
		if err != nil {
			slog.Warn("qishui QR not available", "error", err)
			reject(w, 502, "无法创建汽水二维码")
			return
		}
		s.qr, s.kugouQR, s.qishuiQR, s.qrSaved = nil, nil, qr, false
	default:
		reject(w, 400, "不支持此平台扫码")
		return
	}
	reply(w, 200, map[string]any{"success": true})
}

func (a *Admin) pollKugouQR(w http.ResponseWriter, r *http.Request, s *session) {
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	status, cookie, err := a.kugou.PollQR(ctx, s.kugouQR)
	if err != nil {
		reject(w, 502, "扫码状态查询失败，请重试")
		return
	}
	a.saveQR(w, ctx, s, "kugou", "酷狗扫码登录", status, cookie)
}

func (s *session) closeQR() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clearQR()
}

func (s *session) clearQR() {
	s.qishuiVerifyKey = ""
	if s.qishuiQR != nil {
		s.qishuiQR.Close()
		s.qishuiQR = nil
	}
	if s.qqQR != nil {
		s.qqQR.Close()
		s.qqQR = nil
	}
}

func (a *Admin) saveQR(w http.ResponseWriter, ctx context.Context, s *session, platform, note, status, cookie string) {
	if platform == "qishui" && status == "second_verify" {
		if s.qishuiVerifyKey == "" {
			var err error
			s.qishuiVerifyKey, err = secureRandom(32)
			if err != nil {
				reject(w, 503, "无法创建验证会话")
				return
			}
		}
		reply(w, 200, map[string]any{"success": true, "status": status, "verifyKey": s.qishuiVerifyKey})
		return
	}
	if status == "confirmed" {
		profile, err := a.checkAccount(ctx, platform, cookie)
		if err != nil {
			reject(w, 502, "音源账号验证失败，请重试")
			return
		}
		a.writeMu.Lock()
		_, err = migrate.SaveAccount(a.dataDir, a.keyFile, a.keyValue, "", migrate.AccountChange{
			Cookie: cookie, Platform: platform, Username: s.username, MatchUserID: profile.UserID, Note: &note, Profile: &profile,
		})
		a.writeMu.Unlock()
		if accountError(w, err) {
			return
		}
		s.clearQR()
		s.kugouQR, s.qrSaved = nil, true
		if err := a.backup(); err != nil {
			slog.Warn("music account backup failed", "error", err)
		}
	}
	reply(w, 200, map[string]any{"success": true, "status": status})
}
