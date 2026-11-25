package netease

import (
	"net/http"
	"testing"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
)

func TestQRCodeToMemberPlayback(t *testing.T) {
	const login = "fixture-netease-member"
	poll := 0
	client := New(&http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/weapi/login/qrcode/unikey":
			return reply(r, 200, `{"code":200,"unikey":"fixture-netease-qr"}`), nil
		case "/weapi/login/qrcode/client/login":
			poll++
			switch poll {
			case 1:
				return reply(r, 200, `{"code":801}`), nil
			case 2:
				return reply(r, 200, `{"code":802}`), nil
			case 3:
				res := reply(r, 200, `{"code":803}`)
				res.Header = http.Header{"Set-Cookie": {"MUSIC_U=" + login + "; Path=/; Secure"}}
				return res, nil
			}
		case "/weapi/w/nuser/account/get":
			if cookieValue(r.Header.Get("Cookie"), "MUSIC_U") != login {
				t.Fatal("login cookie did not reach the profile endpoint")
			}
			return reply(r, 200, `{"code":200,"account":{"id":123},"profile":{"nickname":"fixture","vipType":1}}`), nil
		case "/eapi/song/enhance/player/url/v1":
			if cookieValue(r.Header.Get("Cookie"), "MUSIC_U") != login {
				t.Fatal("member playback did not use the authorized account")
			}
			data := decodeEnhanced(t, r)
			if string(data["level"]) != `"lossless"` {
				t.Fatalf("unexpected quality: %s", data["level"])
			}
			return reply(r, 200, `{"data":[{"url":"https://m1.music.126.net/fixture.flac","time":180000,"level":"lossless","freeTrialInfo":null}]}`), nil
		}
		t.Fatalf("unexpected request %s", r.URL.Path)
		return nil, nil
	})}, 0)
	qr, err := client.CreateQR(t.Context())
	if err != nil || qr.URL() != "https://music.163.com/login?codekey=fixture-netease-qr" {
		t.Fatalf("create QR: %v", err)
	}
	var result QRResult
	for _, want := range []string{"waiting", "scanned", "confirmed", "confirmed"} {
		result, err = client.PollQR(t.Context(), qr)
		if err != nil || result.Status != want {
			t.Fatalf("wanted %s, got %s (%v)", want, result.Status, err)
		}
	}
	if poll != 3 {
		t.Fatal("confirmed QR polled upstream again")
	}
	profile, err := client.ValidateAccount(t.Context(), result.Cookie)
	if err != nil || !profile.CanPlayVIP {
		t.Fatalf("member profile: %v %v", profile, err)
	}
	accounts := music.NewAccountPool([]music.Account{{Cookie: result.Cookie, Membership: music.VIP}})
	got, err := accounts.Lookup(t.Context(), music.Request{Platform: "netease", Type: "url", ID: "123", Quality: "lossless"}, client)
	if err != nil || got.Link.URL != "https://m1.music.126.net/fixture.flac" || got.Link.Quality != "无损" {
		t.Fatalf("member link: %+v %v", got.Link, err)
	}
}
