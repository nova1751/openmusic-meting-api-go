package qishui

import (
	"context"
	"strings"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
	"github.com/nova1751/openmusic-meting-api-go/internal/providerhttp"
)

func (c *Client) Account(ctx context.Context, cookie string) (music.AccountProfile, error) {
	if !fingerprint(cookie) {
		return music.AccountProfile{}, music.ErrAccountInvalid
	}
	reply, err := c.request(ctx, upstreamRequest{endpoint: pcHost + "/luna/pc/me",
		query: c.params(false, nil), cookie: cookie})
	if err != nil {
		return music.AccountProfile{}, err
	}
	root := reply.Object("data")
	if root == nil {
		root = reply
	}
	names := []string{"my_info", "myInfo", "user", "user_info", "account", "me"}
	user := root.Object(names...)
	if user == nil {
		user = reply.Object(names...)
	}
	if user == nil {
		user = root
	}
	profile := music.AccountProfile{
		UserID: user.Text("id", "user_id", "uid"), Nickname: providerhttp.First(user.Text("nickname", "nick_name", "name", "douyin_id"), "汽水用户"),
		AvatarURL: cover(user.Get("avatar_url", "avatar", "larger_avatar_url")),
	}
	if profile.UserID == "" {
		return music.AccountProfile{}, music.ErrAccountInvalid
	}
	replacer := strings.NewReplacer("_", "", "-", "", " ", "")
	budget := 600
	var visit func(any, int)
	visit = func(value any, depth int) {
		if depth > 6 || budget == 0 {
			return
		}
		budget--
		if list, ok := value.([]any); ok {
			for _, item := range list[:min(len(list), 120)] {
				visit(item, depth+1)
			}
			return
		}
		for key, item := range providerhttp.AsObject(value) {
			switch replacer.Replace(strings.ToLower(key)) {
			case "sviptype", "sviplevel", "superviptype", "superviplevel", "issvip", "issupervip", "hassvip", "hassupervip", "svipactive", "svipenabled":
				profile.IsSVIP = profile.IsSVIP || providerhttp.Positive(item)
			case "viptype", "viplevel", "membertype", "memberlevel", "musicviptype", "musicviplevel", "isvip", "ismember", "hasvip", "hasmembership", "vipactive", "vipenabled":
				profile.IsVIP = profile.IsVIP || providerhttp.Positive(item)
			case "viplevelname", "vipname", "vipstage", "memberlevelname", "membername", "membershiplevel", "membershiptype":
				switch replacer.Replace(strings.ToLower(providerhttp.Text(item))) {
				case "svip", "supervip", "超级会员":
					profile.IsSVIP = true
				case "vip", "普通会员", "会员":
					profile.IsVIP = true
				}
			}
			visit(item, depth+1)
		}
	}
	visit(reply, 0)
	profile.IsVIP = profile.IsVIP || profile.IsSVIP
	profile.CanPlayVIP, profile.CanPlaySVIP = profile.IsVIP, profile.IsSVIP
	return profile, nil
}
