package tencent

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
)

func TestQRCodeExchangeRefreshAndMemberPlayback(t *testing.T) {
	const scan = `{"cookies":{"qqmusic_uin":"o123","qqmusic_key":{"value":"fixture-scan-token"},"qrcode_id":"fixture-qq-qr"}}`
	calls := 0
	client := New(&http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		var payload struct {
			Comm    map[string]any
			Request struct {
				Module, Method string
				Param          map[string]any
			} `json:"req_0"`
		}
		body := ""
		status := 200
		switch calls {
		case 1:
			if r.URL.Host != "api.tencentmusic.com" || r.Header.Get("Method") != "GetQimei" {
				t.Fatal("wrong device registration request")
			}
			body = `{"data":"{\"data\":{\"q16\":\"fixture-q16\",\"q36\":\"fixture-q36\"}}"}`
		case 2, 3, 4:
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.Comm["QIMEI"] != "fixture-q16" {
				t.Fatal("missing device identity")
			}
			switch calls {
			case 2:
				if payload.Request.Method != "GetSession" {
					t.Fatal("missing mobile login preflight")
				}
				body = `{"req_0":{"code":0,"data":{"session":{"uid":"fixture-uid","sid":"fixture-sid"}}}}`
			case 3:
				if payload.Request.Method != "Login" || payload.Request.Param["qrCodeID"] != "fixture-qq-qr" ||
					payload.Request.Param["token"] != "fixture-scan-token" || payload.Comm["sid"] != "fixture-sid" {
					t.Fatal("invalid QR exchange")
				}
				body = `{"req_0":{"code":0,"data":{"musicid":123,"musickey":"fixture-original-key","refresh_key":"fixture-refresh-key","refresh_token":"fixture-refresh-token","loginType":6}}}`
			case 4:
				if payload.Request.Method != "Login" || payload.Request.Param["loginMode"] != float64(2) ||
					payload.Request.Param["musickey"] != "fixture-original-key" || payload.Request.Param["refresh_token"] != "fixture-refresh-token" ||
					payload.Request.Param["refresh_key"] != "fixture-refresh-key" || payload.Comm["tmeLoginType"] != float64(6) {
					t.Fatal("invalid refresh request")
				}
				body = `{"req_0":{"code":0,"data":{"musicid":123,"musickey":"fixture-renewed-key","refresh_token":"fixture-next-refresh"}}}`
			}
		case 5:
			body = `{"songinfo":{"code":0,"data":{"track_info":{"mid":"TrackMID","file":{"media_mid":"RecordingMID"}}}}}`
		case 6:
			if err := json.Unmarshal([]byte(r.URL.Query().Get("data")), &payload); err != nil {
				t.Fatal(err)
			}
			filename, ok := payload.Request.Param["filename"].([]any)
			if r.Method != http.MethodGet || r.URL.Query().Get("-") != "getplaysongvkey" || payload.Request.Method != "CgiGetVkey" ||
				payload.Comm["authst"] != "fixture-renewed-key" || !ok || len(filename) != 1 || filename[0] != "F000TrackMIDRecordingMID.flac" {
				t.Fatal("invalid QQ member playback request")
			}
			body = `{"req_0":{"code":0,"data":{"sip":["https://isure.stream.qqmusic.qq.com/"],"midurlinfo":[{"purl":"fixture.flac","result":0}]}}}`
		case 7:
			if r.URL.Path != "/fixture.flac" || r.Header.Get("Range") != "bytes=0-1" || r.Header.Get("Cookie") != "" {
				t.Fatal("invalid playback probe")
			}
			status = 206
		default:
			t.Fatalf("unexpected upstream call %d", calls)
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	if _, err := client.confirmQR(t.Context(), "wrong-qr", []byte(scan)); err == nil || calls != 0 {
		t.Fatal("accepted a mismatched QR session")
	}
	credential, err := client.confirmQR(t.Context(), "fixture-qq-qr", []byte(scan))
	if err != nil {
		t.Fatal(err)
	}
	device := cookieField(credential, "psrf_qqdevice")
	if device == "" || cookieField(credential, "qqmusic_key") != "fixture-original-key" {
		t.Fatal("QR exchange did not retain the device credential")
	}
	if _, err := client.Refresh(t.Context(), "uin=o123; qqmusic_key=fixture-original-key"); !errors.Is(err, ErrRefreshData) {
		t.Fatalf("accepted incomplete refresh credential: %v", err)
	}
	credential, err = client.Refresh(t.Context(), credential)
	if err != nil || cookieField(credential, "psrf_qqdevice") != device || cookieField(credential, "psrf_qqrefresh_token") != "fixture-next-refresh" {
		t.Fatalf("refresh failed or lost the saved device: %v", err)
	}
	got, err := client.Lookup(t.Context(), music.Request{Platform: "tencent", Type: "url", ID: "TrackMID", Cookie: credential, Quality: "lossless"})
	if err != nil || got.Link.URL != "https://isure.stream.qqmusic.qq.com/fixture.flac" || got.Link.Quality != "SQ无损品质" {
		t.Fatalf("member link: %+v %v", got.Link, err)
	}
}
