package kugou

import (
	"context"
	"errors"
	"strings"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
	"github.com/nova1751/openmusic-meting-api-go/internal/providerhttp"
)

func (c *Client) Account(ctx context.Context, cookie string) (music.AccountProfile, error) {
	userID := providerhttp.CookieValue(cookie, "userid")
	if userID == "" || providerhttp.CookieValue(cookie, "token") == "" {
		return music.AccountProfile{}, music.ErrAccountInvalid
	}
	verification, err := c.call(ctx, request{address: "https://trackercdngz.kugou.com/v1/user_verify",
		cookie: cookie, params: map[string]string{"module_id": "51"}, profile: lite})
	if err != nil {
		return music.AccountProfile{}, err
	}
	if verification.Int("status") != 1 || verification.Object("data").Text("auth") == "" {
		return music.AccountProfile{}, music.ErrAccountInvalid
	}
	profile := music.AccountProfile{UserID: userID, Nickname: "酷狗用户"}
	reply, err := c.call(ctx, request{address: "https://kugouvip.kugou.com/v1/get_union_vip",
		cookie: cookie, params: map[string]string{"busi_type": "concept"}, profile: lite})
	if err != nil {
		var code serviceError
		if errors.As(err, &code) && (code == 20010 || code == 20017) {
			return profile, nil
		}
		return music.AccountProfile{}, err
	}
	if reply.Text("status") == "0" {
		return music.AccountProfile{}, music.ErrAccountInvalid
	}
	info := reply.Object("data")
	if info == nil {
		info = reply
	}
	user := info.Object("user", "userinfo")
	if user == nil {
		user = info
	}
	profile.UserID = providerhttp.First(user.Text("userid", "user_id"), userID)
	profile.Nickname = providerhttp.First(user.Text("nickname", "nick_name"), profile.Nickname)
	profile.AvatarURL = user.Text("avatar", "avatar_url", "headimg")
	level := strings.ToLower(info.Text("product_type", "productType", "vip_name", "vipName"))
	profile.IsSVIP = level == "svip" || level == "supervip" || level == "super_vip"
	for _, key := range []string{"is_svip", "isSvip", "svip", "super_vip", "superVip", "is_super_vip", "isSuperVip"} {
		profile.IsSVIP = profile.IsSVIP || providerhttp.Positive(info[key])
	}
	profile.IsVIP = profile.IsSVIP || info.Int("vip_type", "vipType", "type") > 0
	for _, key := range []string{"is_vip", "isVip", "vip", "vip_status", "vipStatus"} {
		profile.IsVIP = profile.IsVIP || providerhttp.Positive(info[key])
	}
	profile.CanPlayVIP, profile.CanPlaySVIP = profile.IsVIP, profile.IsSVIP
	return profile, nil
}
