package kugou

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/nova1751/openmusic-meting-api-go/internal/providerhttp"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestQRReusesDeviceForPolling(t *testing.T) {
	var device string
	calls := 0
	client := New(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		q := r.URL.Query()
		signed := q.Get("signature")
		q.Del("signature")
		params := make(map[string]string)
		for key := range q {
			params[key] = q.Get(key)
		}
		if signed != signature(params, "", "NVPh5oo715z5DIWAeQlhMDsWXXQV4hwt") || r.URL.Host != "login-user.kugou.com" {
			t.Fatal("Kugou QR request did not have expected signature or host")
		}
		body := `{"status":1,"data":{"qrcode":"test-qr"}}`
		if calls == 1 {
			device = providerhttp.CookieValue(r.Header.Get("Cookie"), "KUGOU_API_DEV")
			if r.URL.Path != "/v2/qrcode" || len(device) != 10 {
				t.Fatal("missing QR device")
			}
		} else {
			if r.URL.Path != "/v2/get_userinfo_qrcode" || q.Get("dev") != device || q.Get("qrcode") != "test-qr" {
				t.Fatal("polling did not reuse the session device")
			}
			body = `{"status":1,"data":{"status":4,"token":"synthetic","userid":10001}}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	session, err := client.CreateQR(t.Context())
	if err != nil || !strings.Contains(session.URL(), "qrcode=test-qr") {
		t.Fatalf("create: %v %v", session, err)
	}
	status, cookie, err := client.PollQR(t.Context(), session)
	if err != nil || status != "confirmed" || providerhttp.CookieValue(cookie, "userid") != "10001" ||
		providerhttp.CookieValue(cookie, "KUGOU_API_DEV") != device {
		t.Fatalf("poll: %q %v", status, err)
	}
}
