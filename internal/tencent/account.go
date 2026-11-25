package tencent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
	"github.com/nova1751/openmusic-meting-api-go/internal/providerhttp"
)

func (c *Client) Account(ctx context.Context, cookie string) (music.AccountProfile, error) {
	uin := first(cookieField(cookie, "uin"), cookieField(cookie, "wxuin"))
	key := first(cookieField(cookie, "qqmusic_key"), cookieField(cookie, "qm_keyst"))
	if uin == "" || key == "" || uin == "0" {
		return music.AccountProfile{}, music.ErrAccountInvalid
	}
	payload, err := json.Marshal(map[string]any{
		"comm":  map[string]any{"ct": 19, "cv": 0, "uin": uin, "format": "json", "authst": key},
		"req_0": map[string]any{"module": "music.UserInfoSvr", "method": "GetUserInfo", "param": map[string]any{}},
	})
	if err != nil {
		return music.AccountProfile{}, err
	}
	var reply providerhttp.Object
	q := url.Values{"data": {string(payload)}}
	if err := c.request(ctx, http.MethodGet, "https://u.y.qq.com/cgi-bin/musicu.fcg?"+q.Encode(), cookie, nil, &reply); err != nil {
		return music.AccountProfile{}, err
	}
	result := reply.Object("req_0")
	code := result.Int("code")
	if code == 1000 || code == 104401 || code == 104400 {
		return music.AccountProfile{}, music.ErrAccountInvalid
	}
	if code == 104009 {
		return music.AccountProfile{}, music.ErrVerificationRequired
	}
	user := result.Object("data")
	if code != 0 || user == nil {
		return music.AccountProfile{}, fmt.Errorf("tencent user response code %d", code)
	}
	vip := providerhttp.Positive(user["vip"])
	svip := false
	if device, ok := parseLoginDevice(cookie); ok {
		comm := device.comm()
		comm["uin"], comm["authst"], comm["tmeLoginType"] = uin, key, loginType(cookie, key)
		if membership, err := c.mobileCall(ctx, comm, "VipLogin.VipLoginInter", "vip_login_base", map[string]any{}); err == nil && membership != nil {
			identity := membership.Object("identity", "Identity")
			svip = providerhttp.Positive(membership.Get("svip", "SVip")) || providerhttp.Positive(identity.Get("HugeVip", "hugeVip", "huge_vip"))
			if expiry := svipExpiry(membership, 0); expiry > 0 {
				svip = svip && expiry > time.Now().Unix()
			}
			vip = svip || providerhttp.Positive(identity.Get("vip", "Vip"))
		}
	}
	return music.AccountProfile{UserID: first(user.Text("uin"), uin), Nickname: first(user.Text("nick"), "QQ用户"),
		AvatarURL: user.Text("headpic"), IsVIP: vip, CanPlayVIP: vip, IsSVIP: svip, CanPlaySVIP: svip}, nil
}

func svipExpiry(obj providerhttp.Object, depth int) int64 {
	if depth > 4 {
		return 0
	}
	normalize := strings.NewReplacer("_", "", "-", "")
	for key, value := range obj {
		switch normalize.Replace(strings.ToLower(key)) {
		case "svipendtime", "svipexpiretime", "svipend", "svipexpire", "hugevipendtime", "hugevipexpiretime", "hugevipend", "hugevipexpire":
			if stamp, err := time.Parse(time.RFC3339, providerhttp.Text(value)); err == nil {
				return stamp.Unix()
			}
			n := providerhttp.Number(value)
			if n > 100_000_000_000 {
				n /= 1000
			}
			if n > 0 {
				return int64(n)
			}
		}
		if expiry := svipExpiry(providerhttp.AsObject(value), depth+1); expiry > 0 {
			return expiry
		}
	}
	return 0
}
