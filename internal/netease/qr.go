package netease

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
)

const anonymousToken = "de91e1f8119d32e01cc73efcb82c0a30c9137e8d4f88dbf5e3d7bf3f28998f21add2bc8204eeee5e56c0bbb8743574b46ca2c10c35dc172199bef9bf4d60ecdeab066bb4dc737d1c3324751bcc9aaf44c3061cd18d77b7a0"

// QRSession is held on the server; callers must not expose Key or Cookie to a
// third-party QR generation service.
type QRSession struct {
	mu      sync.Mutex
	key     string
	cookies map[string]string
	status  string
	cookie  string
	expires time.Time
}

type QRResult struct {
	Status string
	Cookie string `json:"-"`
}

func randomID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func (c *Client) qrRequest(ctx context.Context, endpoint string, fields parameters, cookies map[string]string) (map[string]json.RawMessage, error) {
	fields["csrf_token"] = ""
	data, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	form, err := webForm(data)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webOrigin+"/weapi/"+endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", webOrigin+"/")
	req.Header.Set("Origin", webOrigin)
	req.Header.Set("User-Agent", "Mozilla/5.0 NeteaseMusicDesktop/3.1.30.203753")
	for name, value := range cookies {
		req.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("netease QR request failed")
	}
	for _, item := range resp.Cookies() {
		cookies[item.Name] = item.Value
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return nil, errors.New("invalid netease QR response")
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(body, &result); err != nil || result == nil {
		return nil, errors.New("invalid netease QR response")
	}
	return result, nil
}

func (c *Client) CreateQR(ctx context.Context) (*QRSession, error) {
	nuid, err := randomID()
	if err != nil {
		return nil, err
	}
	nmtid, err := randomID()
	if err != nil {
		return nil, err
	}
	cookies := map[string]string{"__remember_me": "true", "_ntes_nuid": nuid, "NMTID": nmtid,
		"os": "pc", "appver": "3.1.30.203753", "MUSIC_A": anonymousToken}
	result, err := c.qrRequest(ctx, "login/qrcode/unikey", parameters{"type": 3}, cookies)
	if err != nil {
		return nil, err
	}
	var key string
	_ = json.Unmarshal(result["unikey"], &key)
	if key == "" {
		var nested struct {
			Key string `json:"unikey"`
		}
		_ = json.Unmarshal(result["data"], &nested)
		key = nested.Key
	}
	if len(key) < 8 || len(key) > 128 || strings.ContainsAny(key, "\r\n") {
		return nil, errors.New("netease did not return a valid QR key")
	}
	return &QRSession{key: key, cookies: cookies, status: "waiting", expires: time.Now().Add(5 * time.Minute)}, nil
}

func (s *QRSession) URL() string {
	return "https://music.163.com/login?codekey=" + url.QueryEscape(s.key)
}

func (c *Client) PollQR(ctx context.Context, s *QRSession) (QRResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status == "confirmed" {
		return QRResult{Status: s.status, Cookie: s.cookie}, nil
	}
	if s.status == "expired" || time.Now().After(s.expires) {
		s.status = "expired"
		return QRResult{Status: s.status}, nil
	}
	result, err := c.qrRequest(ctx, "login/qrcode/client/login", parameters{"key": s.key, "type": 3}, s.cookies)
	if err != nil {
		return QRResult{}, err
	}
	var code int
	if json.Unmarshal(result["code"], &code) != nil {
		return QRResult{}, errors.New("invalid netease QR status")
	}
	switch code {
	case 800:
		s.status = "expired"
	case 802:
		s.status = "scanned"
	case 803:
		var cookie string
		_ = json.Unmarshal(result["cookie"], &cookie)
		if cookie == "" {
			r := &http.Request{Header: make(http.Header)}
			for name, value := range s.cookies {
				r.AddCookie(&http.Cookie{Name: name, Value: value})
			}
			cookie = r.Header.Get("Cookie")
		}
		cookie = strings.TrimSpace(cookie)
		if strings.ContainsAny(cookie, "\r\n") || cookieValue(cookie, "MUSIC_U") == "" {
			return QRResult{}, errors.New("netease QR completed without a login cookie")
		}
		s.cookie, s.status = cookie, "confirmed"
	case 801:
		// A scanned session must never regress to waiting after a concurrent poll.
		if s.status != "scanned" {
			s.status = "waiting"
		}
	default:
		return QRResult{}, errors.New("netease QR requires retry or verification")
	}
	return QRResult{Status: s.status, Cookie: s.cookie}, nil
}

type AccountProfile struct {
	UserID      json.Number `json:"userId"`
	Nickname    string      `json:"nickname"`
	VIPType     int         `json:"vipType"`
	IsVIP       bool        `json:"isVip"`
	IsSVIP      bool        `json:"isSvip"`
	CanPlayVIP  bool        `json:"canPlayVip"`
	CanPlaySVIP bool        `json:"canPlaySvip"`
}

// ValidateAccount verifies the login and derives membership from the provider,
// not from a value supplied by the browser.
func (c *Client) ValidateAccount(ctx context.Context, cookie string) (AccountProfile, error) {
	if cookieValue(cookie, "MUSIC_U") == "" {
		return AccountProfile{}, errors.New("netease login cookie is missing")
	}
	var reply struct {
		Account *struct {
			ID       json.Number `json:"id"`
			IsSVIP   bool        `json:"isSvip"`
			SVIPType int         `json:"svipType"`
		} `json:"account"`
		Profile *struct {
			Nickname string `json:"nickname"`
			VIPType  int    `json:"vipType"`
			IsSVIP   bool   `json:"isSvip"`
			SVIPType int    `json:"svipType"`
		} `json:"profile"`
	}
	if err := c.request(ctx, webAPI("/api/w/nuser/account/get"), cookie, parameters{}, &reply); err != nil {
		return AccountProfile{}, err
	}
	if reply.Account == nil || reply.Profile == nil {
		return AccountProfile{}, music.ErrAccountInvalid
	}
	profile := AccountProfile{UserID: reply.Account.ID, Nickname: reply.Profile.Nickname, VIPType: reply.Profile.VIPType}
	profile.IsVIP = profile.VIPType > 0
	profile.IsSVIP = reply.Account.IsSVIP || reply.Account.SVIPType > 0 || reply.Profile.IsSVIP || reply.Profile.SVIPType > 0
	profile.CanPlaySVIP = profile.IsSVIP
	profile.CanPlayVIP = profile.IsVIP || profile.IsSVIP
	if !profile.CanPlayVIP {
		result, err := c.playback(ctx, music.Request{Type: "url", ID: "254059", Quality: "standard", Cookie: cookie})
		profile.CanPlayVIP = err == nil && result.Link.URL != ""
	}
	return profile, nil
}
