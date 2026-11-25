package nativeadmin

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/nova1751/openmusic-meting-api-go/internal/migrate"
)

var revokePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{16,64}$`)

// Contributions is called after the programmatic bearer and assertion check.
func (a *Admin) Contributions(w http.ResponseWriter, r *http.Request, body []byte) {
	switch r.URL.Path {
	case "/admin/contributions":
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit == 0 {
			limit = 20
		}
		out, err := migrate.Contributions(a.dataDir, min(max(limit, 1), 100))
		if !accountError(w, err) {
			reply(w, 200, map[string]any{"success": true, "data": out})
		}
	case "/admin/contributions/revoke":
		var input struct{ RevokeToken string }
		if json.Unmarshal(body, &input) != nil || !revokePattern.MatchString(input.RevokeToken) {
			reject(w, 400, "撤销令牌无效")
			return
		}
		a.writeMu.Lock()
		err := migrate.RevokeContribution(a.dataDir, input.RevokeToken)
		a.writeMu.Unlock()
		if !accountError(w, err) {
			reply(w, 200, map[string]any{"success": true})
		}
	case "/admin/contribute":
		var input struct{ Platform, Cookie, ProviderName, Provider, RevokeToken string }
		if json.Unmarshal(body, &input) != nil || !revokePattern.MatchString(input.RevokeToken) ||
			!cookieFormat(input.Platform, input.Cookie) {
			reject(w, 400, "平台、Cookie 或撤销令牌无效")
			return
		}
		profile, err := a.checkAccount(r.Context(), input.Platform, input.Cookie)
		if err != nil || profile.UserID == "" || (!profile.IsVIP && !profile.CanPlayVIP) {
			reject(w, 400, "账号验证失败或缺少会员播放权益")
			return
		}
		name := strings.TrimSpace(input.ProviderName)
		if name == "" {
			name = strings.TrimSpace(input.Provider)
		}
		if name == "" {
			name = "匿名用户"
		}
		letters := []rune(name)
		name = string(letters[:min(40, len(letters))])
		contribution := &migrate.Contribution{Key: input.Platform + ":" + profile.UserID, ProviderName: name, RevokeToken: input.RevokeToken}
		note := "首页共享 · " + name
		a.writeMu.Lock()
		id, err := migrate.SaveAccount(a.dataDir, a.keyFile, a.keyValue, "", migrate.AccountChange{
			Cookie: input.Cookie, Platform: input.Platform, Username: name, Profile: &profile, Note: &note, Contribution: contribution,
		})
		a.writeMu.Unlock()
		if accountError(w, err) {
			return
		}
		reply(w, 200, map[string]any{"success": true, "data": map[string]any{"id": id, "platform": input.Platform,
			"providerName": name, "userInfo": profile}, "updated": true, "revokeToken": input.RevokeToken})
	}
}
