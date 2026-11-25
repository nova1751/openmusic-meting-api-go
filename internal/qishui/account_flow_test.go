package qishui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
)

func TestQRCodeSecondaryVerificationAndMemberPlayback(t *testing.T) {
	const credential = "fixture-qishui-member"
	browserCalls := 0
	closed := false
	qr := &QRSession{created: time.Now(), tab: t.Context(), last: "waiting", close: func() { closed = true }}
	qr.exchange = func(_ context.Context, req passportRequest, signed bool) (passportResponse, []*http.Cookie, error) {
		browserCalls++
		u, err := url.Parse(req.URL)
		if err != nil {
			t.Fatal(err)
		}
		var data string
		var cookies []*http.Cookie
		switch browserCalls {
		case 1:
			if !signed || u.Path != "/passport/web/get_qrcode/" || u.Query().Get("aid") != "386088" {
				t.Fatal("invalid initial QR request")
			}
			data = `{"message":"success","data":{"error_code":0,"token":"fixture-qr","qrcode_index_url":"https://example.invalid/qr?token=fixture-public"}}`
		case 2:
			if !signed || req.Method != "POST" || u.Path != "/passport/web/check_qrconnect/" || req.Body == nil {
				t.Fatal("invalid poll")
			}
			data = `{"data":{"error_code":2046,"biz_params":"fixture_key=fixture-value"}}`
		case 3:
			if signed || u.Path != "/passport/safe/pack_verify_ways_data/" || req.Headers["Cookie"] != "" {
				t.Fatal("invalid verification proxy")
			}
			data = `{"data":{"verification":"fixture"}}`
		case 4:
			if !signed || u.Query().Get("isResend") != "true" || req.Body == nil {
				t.Fatal("verification did not replay the login request")
			}
			params, err := url.ParseQuery(*req.Body)
			if err != nil || params.Get("token") != "fixture-qr" || params.Get("fixture_key") != "fixture-value" {
				t.Fatal("verification parameters were lost")
			}
			data = `{"data":{"status":"3"}}`
			cookies = []*http.Cookie{{Name: "sessionid_ss", Value: credential}}
		default:
			t.Fatal("finished QR was sent to the upstream again")
		}
		return passportResponse{Status: 200, Body: data}, cookies, nil
	}
	if err := qr.create(t.Context()); err != nil || !strings.Contains(qr.URL(), "token=fixture-public") {
		t.Fatalf("create QR: %v", err)
	}
	if status, cookie, err := qr.Poll(t.Context()); status != "second_verify" || cookie != "" || err != nil {
		t.Fatalf("challenge: %q %v", status, err)
	}
	if start, err := qr.VerificationStart(); err != nil || start == nil {
		t.Fatalf("start verification: %v", err)
	}
	if _, err := qr.VerificationProxy(t.Context(), VerificationRequest{Method: "GET", URL: "https://example.invalid/passport/"}); err == nil {
		t.Fatal("accepted wrong verification origin")
	}
	if _, err := qr.VerificationProxy(t.Context(), VerificationRequest{Method: "POST", URL: "https://api.qishui.com/passport/safe/pack_verify_ways_data/", Headers: map[string]string{"Cookie": "wrong"}}); err != nil {
		t.Fatal(err)
	}
	if status, err := qr.VerificationComplete(t.Context()); status != "confirmed" || err != nil || !closed {
		t.Fatalf("confirmation: %q %v", status, err)
	}
	_, cookie, err := qr.Poll(t.Context())
	if err != nil || cookie != "sessionid_ss="+credential {
		t.Fatalf("missing cookie after confirmation: %v", err)
	}

	calls := 0
	client := New(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		var body string
		switch r.URL.Host {
		case "signer.example.invalid":
			var req struct {
				DeviceID, URL string
				Headers       []string
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if req.DeviceID == "" || !strings.HasPrefix(req.URL, "https://api.qishui.com/luna/pc/track_v2?") ||
				r.Header.Get("Cookie") != "" || slices.Contains(req.Headers, "cookie") || slices.Contains(req.Headers, cookie) {
				t.Fatal("invalid signer request or leaked credential")
			}
			body = `{"ok":true,"X-Helios":"fixture-helios","X-Medusa":"fixture-medusa"}`
		case "api.qishui.com":
			if r.URL.Path != "/luna/pc/track_v2" || r.Header.Get("X-Helios") != "fixture-helios" ||
				r.Header.Get("X-Medusa") != "fixture-medusa" || r.Header.Get("Cookie") != cookie {
				t.Fatal("signed playback request lacks credentials")
			}
			body = `{"track_player":{"video_model":{"streams":[{"main_play_url":"https://fixture.douyinvod.com/member.m4a","quality":"lossless","play_auth":"fixture-play-auth"}]}}}`
		default:
			t.Fatal("unexpected playback origin")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}, func() string { return "https://signer.example.invalid/sign" })
	got, err := client.Lookup(t.Context(), music.Request{Platform: "qishui", Type: "url", ID: "123", Cookie: cookie, Quality: "lossless"})
	if err != nil || calls != 2 || got.Link.URL != "https://fixture.douyinvod.com/member.m4a" ||
		got.Link.PlayAuth != "fixture-play-auth" || got.Link.Quality != "无损" {
		t.Fatalf("member link: %+v %v", got.Link, err)
	}
}
