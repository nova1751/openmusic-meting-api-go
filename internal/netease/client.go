package netease

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
)

type protocol string

const (
	plain    protocol = "api"
	web      protocol = "weapi"
	enhanced protocol = "eapi"
)

type endpoint struct {
	origin   string
	path     string
	crypto   protocol
	wirePath string
	maxBytes int64
}

const webOrigin = "https://music.163.com"
const interfaceOrigin = "https://interface.music.163.com"
const maxResponse = 16 << 20

type parameters = map[string]any

type upstreamError struct {
	code    int
	message string
}

func (e *upstreamError) Error() string { return fmt.Sprintf("netease upstream returned %d", e.code) }

func webAPI(path string) endpoint {
	return endpoint{origin: webOrigin, path: path, crypto: web}
}

type Client struct {
	http        *http.Client
	djPageDelay time.Duration
}

func New(client *http.Client, djPageDelay time.Duration) *Client {
	return &Client{http: client, djPageDelay: djPageDelay}
}

func (c *Client) request(ctx context.Context, ep endpoint, cookie string, fields parameters, output any) error {
	incoming := &http.Request{Header: http.Header{"Cookie": {cookie}}}
	cookies := make(map[string]string)
	for _, item := range incoming.Cookies() {
		cookies[item.Name] = item.Value
	}
	cookies["__remember_me"] = "true"
	if cookies["_ntes_nuid"] == "" {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return err
		}
		cookies["_ntes_nuid"] = hex.EncodeToString(id[:])
	}
	path := ep.wirePath
	if path == "" {
		path = "/" + string(ep.crypto) + strings.TrimPrefix(ep.path, "/api")
	}
	var form url.Values
	var err error
	switch ep.crypto {
	case plain:
		form = make(url.Values, len(fields))
		for key, value := range fields {
			form.Set(key, fmt.Sprint(value))
		}
	case web:
		fields["csrf_token"] = cookies["__csrf"]
		body, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		form, err = webForm(body)
		if err != nil {
			return err
		}
	case enhanced:
		fields["header"] = enhancedHeader(cookies)
		body, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		form = enhancedForm(ep.path, body)
	default:
		return errors.New("unsupported netease request format")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.origin+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", webOrigin)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	for name, value := range cookies {
		req.AddCookie(&http.Cookie{Name: name, Value: url.PathEscape(value)})
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return music.ErrAccountInvalid
	}
	if resp.StatusCode != http.StatusOK {
		return &upstreamError{code: resp.StatusCode}
	}
	max := ep.maxBytes
	if max == 0 {
		max = maxResponse
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return err
	}
	if int64(len(body)) > max {
		return errors.New("netease response exceeds size limit")
	}
	var status struct {
		Code    json.Number `json:"code"`
		Message string      `json:"message"`
		Msg     string      `json:"msg"`
	}
	if err := json.Unmarshal(body, &status); err != nil {
		return fmt.Errorf("decode netease status: %w", err)
	}
	if status.Code == "" || status.Code == "0" || status.Code == "200" {
		if strings.TrimSpace(string(body)) == "null" {
			return errors.New("empty netease response")
		}
		return json.Unmarshal(body, output)
	}
	if status.Code == "301" || status.Code == "401" || strings.Contains(status.Message, "未登录") {
		return music.ErrAccountInvalid
	}
	code, _ := strconv.Atoi(string(status.Code))
	return &upstreamError{code: code, message: status.Message + " " + status.Msg}
}

func enhancedHeader(cookies map[string]string) map[string]string {
	now := time.Now()
	header := map[string]string{
		"appver": "8.7.01", "versioncode": "140", "buildver": strconv.FormatInt(now.Unix(), 10),
		"resolution": "1920x1080", "os": "android", "__csrf": cookies["__csrf"],
		"requestId": fmt.Sprintf("%d_%04d ", now.UnixMilli(), now.Nanosecond()%1000),
	}
	for _, field := range []string{"appver", "versioncode", "buildver", "resolution", "os", "osver", "deviceId", "mobilename", "channel"} {
		if value := cookies[field]; value != "" {
			header[field] = value
		}
	}
	for key, value := range header {
		cookies[key] = value
	}
	for _, key := range []string{"MUSIC_U", "MUSIC_A"} {
		if cookies[key] != "" {
			header[key] = cookies[key]
		}
	}
	return header
}

func cookieValue(cookie, name string) string {
	r := &http.Request{Header: http.Header{"Cookie": {cookie}}}
	item, _ := r.Cookie(name)
	if item == nil {
		return ""
	}
	return item.Value
}

func setCookie(cookie, name, value string) string {
	r := &http.Request{Header: make(http.Header)}
	incoming := &http.Request{Header: http.Header{"Cookie": {cookie}}}
	for _, item := range incoming.Cookies() {
		if item.Name != name {
			r.AddCookie(item)
		}
	}
	r.AddCookie(&http.Cookie{Name: name, Value: value})
	return r.Header.Get("Cookie")
}
