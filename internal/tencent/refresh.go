package tencent

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/nova1751/openmusic-meting-api-go/internal/providerhttp"
)

func loginType(cookie, key string) int {
	if id, _ := strconv.Atoi(cookieField(cookie, "tmeLoginType")); id > 0 {
		return id
	}
	if strings.HasPrefix(key, "W_X") {
		return 1
	}
	if id, _ := strconv.Atoi(cookieField(cookie, "login_type")); id > 0 {
		return id
	}
	return 2
}

var ErrRefreshData = errors.New("QQ Music refresh requires the existing device and refresh credentials")

// Refresh preserves the original device and exchanges the existing platform
// credentials for a fresh QQ Music session.
func (c *Client) Refresh(ctx context.Context, cookie string) (string, error) {
	device, ok := parseLoginDevice(cookie)
	uin := first(cookieField(cookie, "uin"), cookieField(cookie, "wxuin"), cookieField(cookie, "qqmusic_uin"))
	idText := strings.TrimPrefix(uin, "o")
	id, parseErr := strconv.ParseUint(idText, 10, 64)
	key := first(cookieField(cookie, "qqmusic_key"), cookieField(cookie, "qm_keyst"))
	refreshKey := cookieField(cookie, "refresh_key")
	refreshToken := first(cookieField(cookie, "psrf_qqrefresh_token"), cookieField(cookie, "refresh_token"))
	if !ok || parseErr != nil || id == 0 || key == "" || refreshKey == "" && refreshToken == "" {
		return "", ErrRefreshData
	}
	kind := loginType(cookie, key)
	params := map[string]any{
		"openid":        first(cookieField(cookie, "psrf_qqopenid"), cookieField(cookie, "openid")),
		"refresh_token": refreshToken, "musickey": key, "refresh_key": refreshKey, "loginMode": 2,
	}
	if kind != 1 {
		expiry, _ := strconv.ParseInt(first(cookieField(cookie, "psrf_access_token_expiresAt"), cookieField(cookie, "expired_at")), 10, 64)
		params["access_token"] = first(cookieField(cookie, "psrf_qqaccess_token"), cookieField(cookie, "access_token"))
		params["expired_in"], params["musicid"] = expiry, id
	}
	if kind != 2 {
		params["str_musicid"] = first(cookieField(cookie, "str_musicid"), idText)
		params["unionid"] = cookieField(cookie, "unionid")
	}
	comm := device.comm()
	comm["tmeLoginType"], comm["qq"], comm["authst"], comm["uin"] = kind, idText, key, id
	data, err := c.mobileCall(ctx, comm, "music.login.LoginServer", "Login", params)
	if err != nil {
		return "", err
	}
	return mergeRefreshedCookie(cookie, idText, data)
}

func mergeRefreshedCookie(cookie, priorID string, data providerhttp.Object) (string, error) {
	newKey := data.Text("musickey")
	if newKey == "" || strings.ContainsAny(newKey, ";\r\n") {
		return "", errors.New("invalid refreshed QQ Music credential")
	}
	id := strings.TrimPrefix(first(data.Text("str_musicid", "musicid"), priorID), "o")
	if _, err := strconv.ParseUint(id, 10, 64); err != nil || id != priorID {
		return "", errors.New("QQ Music refresh returned a different account")
	}
	fields := make(map[string]string)
	for _, pair := range strings.Split(cookie, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && name != "" && value != "" {
			fields[name] = value
		}
	}
	fields["uin"], fields["wxuin"] = "o"+id, "o"+id
	fields["qqmusic_key"], fields["qm_keyst"], fields["p_lskey"] = newKey, newKey, newKey
	for name, field := range map[string]string{
		"refresh_key": "refresh_key", "psrf_qqaccess_token": "access_token", "psrf_qqopenid": "openid",
		"psrf_qqrefresh_token": "refresh_token", "psrf_access_token_expiresAt": "expired_at",
		"psrf_musickey_createtime": "musickeyCreateTime", "euin": "encryptUin", "tmeLoginType": "loginType",
	} {
		if value := data.Text(field); value != "" && !strings.ContainsAny(value, ";\r\n") {
			fields[name] = value
		}
	}
	result := make([]string, 0, len(fields))
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		result = append(result, name+"="+fields[name])
	}
	return strings.Join(result, "; "), nil
}
