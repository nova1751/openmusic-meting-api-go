package nativeadmin

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/migrate"
	"github.com/nova1751/openmusic-meting-api-go/internal/music"
	"github.com/nova1751/openmusic-meting-api-go/internal/providerhttp"
	"github.com/nova1751/openmusic-meting-api-go/internal/tencent"
)

func cookieFormat(platform, cookie string) bool {
	if len(cookie) < 10 || len(cookie) > 7500 || strings.ContainsAny(cookie, "\r\n") {
		return false
	}
	switch platform {
	case "netease":
		return providerhttp.CookieValue(cookie, "MUSIC_U", "MUSIC_A") != ""
	case "tencent":
		return providerhttp.CookieValue(cookie, "uin", "wxuin") != "" && providerhttp.CookieValue(cookie, "qqmusic_key", "qm_keyst") != ""
	case "kugou":
		return providerhttp.CookieValue(cookie, "token") != "" && providerhttp.CookieValue(cookie, "userid") != ""
	case "qishui":
		return providerhttp.CookieValue(cookie, "sessionid", "sessionid_ss", "sid_guard", "sid_tt") != ""
	default:
		return false
	}
}

func accountReply(w http.ResponseWriter, id string, accounts []migrate.AccountSummary) {
	for _, account := range accounts {
		if account.ID == id {
			reply(w, 200, map[string]any{"success": true, "data": account})
			return
		}
	}
	reject(w, 404, "账号不存在")
}

func (a *Admin) checkAccount(ctx context.Context, platform, cookie string) (music.AccountProfile, error) {
	if !cookieFormat(platform, cookie) {
		return music.AccountProfile{}, music.ErrAccountInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	if platform == "netease" {
		p, err := a.client.ValidateAccount(ctx, cookie)
		return music.AccountProfile{UserID: p.UserID.String(), Nickname: p.Nickname, IsVIP: p.IsVIP, IsSVIP: p.IsSVIP,
			CanPlayVIP: p.CanPlayVIP, CanPlaySVIP: p.CanPlaySVIP}, err
	}
	if validate := a.validators[platform]; validate != nil {
		return validate(ctx, cookie)
	}
	return music.AccountProfile{}, errors.New("account validator not configured")
}

func (a *Admin) accountRoutes(w http.ResponseWriter, r *http.Request, s *session) {
	path := strings.TrimPrefix(r.URL.Path, "/admin/cookies")
	id, action, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	if r.Method == http.MethodGet && action == "" {
		accounts, err := migrate.List(a.dataDir)
		if err != nil {
			reject(w, 503, "读取账号失败")
		} else if id != "" {
			accountReply(w, id, accounts)
		} else {
			reply(w, 200, map[string]any{"success": true, "data": accounts})
		}
		return
	}
	switch {
	case r.Method == http.MethodPost && id == "validate" && action == "":
		var input struct{ Platform, Cookie string }
		if decode(r, &input) != nil || !cookieFormat(input.Platform, input.Cookie) {
			reject(w, 400, "平台或 Cookie 无效")
			return
		}
		profile, err := a.checkAccount(r.Context(), input.Platform, input.Cookie)
		var data any = map[string]any{"valid": true, "userInfo": profile}
		if err != nil {
			data = map[string]any{"valid": false, "userInfo": nil, "error": "上游账号验证失败"}
		}
		reply(w, 200, map[string]any{"success": true, "data": data})
	case r.Method == http.MethodPost && id == "" && action == "",
		r.Method == http.MethodPut && id != "" && action == "":
		a.editAccount(w, r, s, id)
	case r.Method == http.MethodDelete && id != "" && action == "":
		a.writeMu.Lock()
		err := migrate.DeleteAccount(a.dataDir, id)
		a.writeMu.Unlock()
		if accountError(w, err) {
			return
		}
		reply(w, 200, map[string]any{"success": true})
	case r.Method == http.MethodPost && id != "" && action == "verify":
		a.verifyAccount(w, r, id)
	case r.Method == http.MethodPost && id != "" && action == "refresh":
		a.refreshAccount(w, r, id)
	default:
		reject(w, 404, "接口不存在")
	}
}

func (a *Admin) editAccount(w http.ResponseWriter, r *http.Request, s *session, id string) {
	var input struct {
		Platform       string
		Cookie, Note   *string
		IsActive       *bool
		SkipValidation bool
	}
	if decode(r, &input) != nil || (id == "" && input.Cookie == nil) || input.Note != nil && len(*input.Note) > 400 {
		reject(w, 400, "账号数据无效")
		return
	}
	change := migrate.AccountChange{Platform: input.Platform, Username: s.username, Note: input.Note, Active: input.IsActive}
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	if id != "" && input.Cookie != nil {
		platform, _, err := migrate.AccountCredential(a.dataDir, a.keyFile, a.keyValue, id)
		if accountError(w, err) {
			return
		}
		change.Platform = platform
	}
	if input.Cookie != nil {
		change.Cookie = strings.TrimSpace(*input.Cookie)
		if !cookieFormat(change.Platform, change.Cookie) {
			reject(w, 400, "平台或 Cookie 无效")
			return
		}
		if !input.SkipValidation {
			profile, err := a.checkAccount(r.Context(), change.Platform, change.Cookie)
			if err != nil {
				reject(w, 400, "上游账号验证失败，凭据未保存")
				return
			}
			change.Profile = &profile
			change.MatchUserID = profile.UserID
		}
	}
	id, err := migrate.SaveAccount(a.dataDir, a.keyFile, a.keyValue, id, change)
	if accountError(w, err) {
		return
	}
	accounts, err := migrate.List(a.dataDir)
	if !accountError(w, err) {
		accountReply(w, id, accounts)
	}
}

func (a *Admin) verifyAccount(w http.ResponseWriter, r *http.Request, id string) {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	platform, cookie, err := migrate.AccountCredential(a.dataDir, a.keyFile, a.keyValue, id)
	if accountError(w, err) {
		return
	}
	profile, err := a.checkAccount(r.Context(), platform, cookie)
	change := migrate.AccountChange{}
	switch {
	case err == nil:
		change.Profile = &profile
	case errors.Is(err, music.ErrAccountInvalid):
		change.Invalid = true
	default:
		reject(w, 502, "暂时无法验证账号")
		return
	}
	_, err = migrate.SaveAccount(a.dataDir, a.keyFile, a.keyValue, id, change)
	if accountError(w, err) {
		return
	}
	accounts, err := migrate.List(a.dataDir)
	if !accountError(w, err) {
		accountReply(w, id, accounts)
	}
}

func accountError(w http.ResponseWriter, err error) bool {
	if errors.Is(err, os.ErrNotExist) {
		reject(w, 404, "账号不存在")
	} else if err != nil {
		reject(w, 503, "无法读写账号数据")
	}
	return err != nil
}

func (a *Admin) refreshAccount(w http.ResponseWriter, r *http.Request, id string) {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	platform, cookie, err := migrate.AccountCredential(a.dataDir, a.keyFile, a.keyValue, id)
	if accountError(w, err) {
		return
	}
	if platform != "tencent" || a.tencent == nil {
		reject(w, 400, "仅支持 QQ 音乐续期")
		return
	}
	refreshed, err := a.tencent.Refresh(r.Context(), cookie)
	switch {
	case errors.Is(err, tencent.ErrRefreshData):
		reject(w, 400, "Cookie 缺少扫码设备信息或续期凭证，请重新登录")
		return
	case err != nil:
		reject(w, 502, "QQ 音乐续期失败")
		return
	}
	profile, err := a.checkAccount(r.Context(), platform, refreshed)
	if err != nil {
		// The exchange may have revoked the previous key. Preserve the new
		// credential so it can be verified when the account endpoint recovers.
		profile = music.AccountProfile{}
	}
	change := migrate.AccountChange{Cookie: refreshed}
	if err == nil {
		change.Profile = &profile
	}
	_, err = migrate.SaveAccount(a.dataDir, a.keyFile, a.keyValue, id, change)
	if !accountError(w, err) {
		reply(w, 200, map[string]any{"success": true, "data": map[string]any{"id": id, "validated": change.Profile != nil}})
	}
}
