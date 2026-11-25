package qishui

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"maps"
	"net/url"
	"strings"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/providerhttp"
)

type QRSession struct {
	tab                                                              context.Context
	close                                                            func()
	created                                                          time.Time
	token, scanURL, msToken, device, install, portrait, cookie, last string
	next                                                             time.Time
}

func (q *QRSession) URL() string { return q.scanURL }
func (q *QRSession) Close()      { q.close() }

func (q *QRSession) passport(ctx context.Context, method, path string, query, form url.Values) (object, error) {
	trace := qrHex(4)
	params := url.Values{
		"passport_jssdk_version": {"2.4.13"}, "passport_jssdk_type": {"normal"}, "is_from_ttaccountsdk": {"1"},
		"aid": {"386088"}, "language": {"zh"}, "account_sdk_source": {"web"}, "p_js_v": {"2.4.13"}, "p_js_t": {"pro"},
		"p_zt": {"3.3.5"}, "p_ver": {"1.0.29"}, "request_host": {"app%3A%2F%2Fresources"}, "p_bd": {"1.0.0.41"},
		"biz_trace_id": {trace}, "is_new_login": {"1"}, "is_from_iesaccountsaas": {"1"},
		"device_id": {q.device}, "install_id": {q.install}, "did": {q.device}, "iid": {q.install},
		"device_platform": {"PC"}, "version_code": {"3.5.2"}, "account_sdk_source_info": {"00"}, "msToken": {q.msToken},
	}
	maps.Copy(params, query)
	request := passportRequest{Method: method, URL: pcHost + path + "?" + params.Encode(), Timeout: 12000, Headers: map[string]string{
		"Accept": "application/json, text/javascript", "x-tt-passport-verify-portrait": q.portrait, "x-tt-passport-trace-id": trace,
	}}
	if form != nil {
		body := form.Encode()
		request.Body = &body
		sum := md5.Sum([]byte(body))
		request.Headers["Content-Type"] = "application/x-www-form-urlencoded"
		request.Headers["x-ss-stub"] = strings.ToUpper(hex.EncodeToString(sum[:]))
	}
	reply, cookies, err := q.browserRequest(ctx, request)
	if err != nil {
		return nil, err
	}
	parts := make([]string, 0, len(cookies))
	for _, cookie := range cookies {
		if value := cookie.String(); value != "" {
			parts = append(parts, value)
		}
	}
	q.cookie = strings.Join(parts, "; ")
	return providerhttp.Decode([]byte(reply.Body))
}

func qrHex(n int) string {
	data := make([]byte, n)
	rand.Read(data)
	return hex.EncodeToString(data)
}

func qrDigits(n int) string {
	data := make([]byte, n)
	rand.Read(data)
	data[0] = '1' + data[0]%8
	for i := 1; i < len(data); i++ {
		data[i] = '0' + data[i]%10
	}
	return string(data)
}

func (b *QRBrowser) CreateQR(ctx context.Context) (*QRSession, error) {
	tab, closeTab, err := b.open(ctx)
	if err != nil {
		return nil, err
	}
	buffer := make([]byte, 88)
	rand.Read(buffer)
	uuid := qrHex(16)
	q := &QRSession{tab: tab, close: closeTab, created: time.Now(), device: qrDigits(16), install: qrDigits(15),
		portrait: uuid[:8] + "-" + uuid[8:12] + "-4" + uuid[13:16] + "-8" + uuid[17:20] + "-" + uuid[20:] + ".login",
		msToken:  base64.RawURLEncoding.EncodeToString(buffer) + "==", last: "waiting"}
	payload, err := q.passport(ctx, "GET", "/passport/web/get_qrcode/", url.Values{
		"next": {pcHost}, "need_logo": {"false"}, "need_short_url": {"false"},
	}, nil)
	if err != nil {
		q.Close()
		return nil, err
	}
	data := payload.Object("data")
	index, err := url.Parse(data.Text("qrcode_index_url"))
	q.token = data.Text("token")
	if err != nil || payload.Text("message") != "success" || data.Int("error_code") != 0 || q.token == "" || index.Query().Get("token") == "" {
		q.Close()
		return nil, errors.New("Qishui did not issue a QR code")
	}
	q.scanURL = "https://bff-pc.qishui.com/light/invoke/scan_login?" + url.Values{
		"token": {index.Query().Get("token")}, "os": {"Windows"}, "computer_name": {"OpenMusic"},
	}.Encode()
	return q, nil
}

func (q *QRSession) Poll(ctx context.Context) (status, cookie string, err error) {
	if q.last == "confirmed" {
		return q.last, q.cookie, nil
	}
	if time.Since(q.created) >= 3*time.Minute || q.tab.Err() != nil {
		return "expired", "", nil
	}
	if time.Now().Before(q.next) {
		return q.last, "", nil
	}
	payload, err := q.passport(ctx, "POST", "/passport/web/check_qrconnect/", nil, url.Values{
		"need_logo": {"false"}, "need_short_url": {"false"}, "is_frontier": {"true"}, "token": {q.token}, "is_new_login": {"1"}, "next": {pcHost},
	})
	if err != nil {
		return "", "", err
	}
	q.next = time.Now().Add(2500 * time.Millisecond)
	data := payload.Object("data")
	state := strings.ToLower(data.Text("status"))
	switch data.Int("error_code") {
	case 2046:
		q.last = "second_verify"
		return q.last, "", nil
	case 2:
		q.Close()
		return "expired", "", nil
	case 7:
		q.next = time.Now().Add(5 * time.Second)
		return q.last, "", nil
	case 0:
	default:
		return "", "", errors.New("Qishui QR polling failed")
	}
	if providerhttp.Positive(data.Get("logged_in", "loggedIn")) || data.Text("session_cookie") != "" || fingerprint(q.cookie) {
		state = "confirmed"
	}
	switch state {
	case "3", "confirmed", "success":
		if !fingerprint(q.cookie) {
			for _, raw := range []string{data.Text("session_cookie", "cookie"), payload.Text("session_cookie", "cookie")} {
				if fingerprint(raw) && !strings.ContainsAny(raw, "\r\n") {
					q.cookie = raw
					break
				}
			}
			if !fingerprint(q.cookie) {
				sessionID := providerhttp.First(data.Text("sessionid", "session_id"), data.Object("auth").Text("sessionid", "session_id"))
				if sessionID != "" && !strings.ContainsAny(sessionID, ";\r\n") {
					q.cookie += "; sessionid=" + sessionID
				}
			}
		}
		if !fingerprint(q.cookie) {
			return "", "", errors.New("Qishui QR verification returned no session")
		}
		q.last = "confirmed"
		q.Close()
		return q.last, q.cookie, nil
	case "1", "scanned", "scan", "已扫码":
		q.last = "scanned"
	case "expired", "expire", "timeout":
		q.Close()
		return "expired", "", nil
	}
	return q.last, "", nil
}
