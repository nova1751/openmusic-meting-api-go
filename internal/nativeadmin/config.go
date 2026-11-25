package nativeadmin

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/nova1751/openmusic-meting-api-go/internal/migrate"
)

func (a *Admin) configRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/admin/config")
	if r.Method == http.MethodGet && path == "" {
		data, err := migrate.Config(a.dataDir)
		if !accountError(w, err) {
			reply(w, 200, map[string]any{"success": true, "data": data})
		}
		return
	}
	if r.Method != http.MethodPut {
		reject(w, 404, "接口不存在")
		return
	}
	var err error
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	switch path {
	case "/openmusic-auth", "/backup":
		var input struct{ Enabled *bool }
		if decode(r, &input) != nil || input.Enabled == nil {
			reject(w, 400, "开关必须为布尔值")
			return
		}
		field := "backupEnabled"
		if path == "/openmusic-auth" {
			field = "openmusicAuthEnabled"
		}
		err = migrate.WriteSetting(a.dataDir, field, *input.Enabled)
	case "/qishui-signer":
		var input struct{ URL *string }
		if decode(r, &input) != nil {
			reject(w, 400, "无效的签名服务地址")
			return
		}
		var address any
		if input.URL != nil && strings.TrimSpace(*input.URL) != "" {
			u, parseErr := url.Parse(strings.TrimSpace(*input.URL))
			if parseErr != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
				reject(w, 400, "签名服务需要有效的 HTTP 或 HTTPS 地址")
				return
			}
			address = u.String()
		}
		err = migrate.WriteSetting(a.dataDir, "qishuiSignerUrl", address)
	case "/fm-priority":
		var input struct {
			Platform string
			CookieID string
		}
		if decode(r, &input) != nil {
			reject(w, 400, "账号数据无效")
			return
		}
		err = migrate.SetFMPriority(a.dataDir, input.Platform, input.CookieID)
	default:
		reject(w, 404, "接口不存在")
		return
	}
	if accountError(w, err) {
		return
	}
	data, err := migrate.Config(a.dataDir)
	if !accountError(w, err) {
		reply(w, 200, map[string]any{"success": true, "data": data})
	}
}
