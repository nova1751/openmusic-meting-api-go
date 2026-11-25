package qishui

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
	"github.com/nova1751/openmusic-meting-api-go/internal/providerhttp"
)

const (
	pcHost      = "https://api.qishui.com"
	mobileHost  = "https://beta-luna.douyin.com"
	webHost     = "https://api-vehicle.volcengine.com"
	webAgent    = "LunaPC/3.3.0(359450208)"
	mobileAgent = "Luna/19.1.0 Android"
)

type Client struct {
	http                        *http.Client
	signer                      func() string
	device, install             string
	mobileDevice, mobileInstall string
	feedsMu                     sync.Mutex
	feeds                       map[[32]byte]*feedState
}

func New(client *http.Client, signer func() string) *Client {
	return &Client{http: client, signer: signer, device: deviceID(), install: deviceID(),
		mobileDevice: deviceID(), mobileInstall: deviceID(), feeds: make(map[[32]byte]*feedState)}
}

func deviceID() string {
	var v [2]byte
	_, _ = rand.Read(v[:])
	return fmt.Sprintf("%d%03d", time.Now().UnixMilli(), binary.BigEndian.Uint16(v[:])%1000)
}

func fingerprint(cookie string) bool {
	return providerhttp.CookieValue(cookie, "sessionid", "sessionid_ss", "sid_guard", "sid_tt") != ""
}

func (c *Client) params(mobile bool, extra url.Values) url.Values {
	q := url.Values{
		"aid": {"386088"}, "app_name": {"luna_pc"}, "region": {"cn"}, "geo_region": {"cn"}, "os_region": {"cn"},
		"sim_region": {""}, "cdid": {""}, "network_carrier": {""}, "ac": {"wifi"}, "tz_name": {"Asia/Shanghai"}, "resolution": {""},
		"device_id": {c.device}, "iid": {c.install}, "fp": {c.device},
		"version_name": {"3.3.0"}, "version_code": {"359450208"}, "channel": {"official"}, "build_mode": {"master"},
		"device_platform": {"windows"}, "device_type": {"Windows"}, "os_version": {"Windows 11"},
	}
	if mobile {
		q.Set("app_name", "luna")
		q.Set("device_id", c.mobileDevice)
		q.Set("iid", c.mobileInstall)
		q.Set("fp", c.mobileDevice)
		q.Set("version_name", "19.1.0")
		q.Set("version_code", "19010000")
		q.Set("build_mode", "release")
		q.Set("device_platform", "android")
		q.Set("device_type", "Pixel 8")
		q.Set("os_version", "15")
	}
	maps.Copy(q, extra)
	return q
}

type upstreamRequest struct {
	endpoint, cookie, agent string
	query                   url.Values
	body                    any
	signed                  bool
}

func (c *Client) request(ctx context.Context, input upstreamRequest) (providerhttp.Object, error) {
	var trace [24]byte
	if _, err := rand.Read(trace[:]); err != nil {
		return nil, err
	}
	headers := http.Header{"Accept": {"application/json,text/plain,*/*"},
		"Content-Type": {"application/json; charset=utf-8"},
		"User-Agent":   {providerhttp.First(input.agent, webAgent)},
		"Referer":      {"https://www.qishui.com/"}, "X-Luna-Background-Type": {"foreground"},
		"X-Luna-Is-Background-Req": {"0"}, "X-Luna-Is-Local-User": {"1"},
		"X-Tt-Trace-Id": {"00-" + hex.EncodeToString(trace[:16]) + "-" + hex.EncodeToString(trace[16:]) + "-01"}}
	endpoint := input.endpoint
	query := maps.Clone(input.query)
	if !input.signed {
		for key, values := range query {
			if len(values) == 1 && values[0] == "" {
				delete(query, key)
			}
		}
	}
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	method := http.MethodGet
	if input.body != nil {
		method = http.MethodPost
		payload, err := json.Marshal(input.body)
		if err != nil {
			return nil, err
		}
		digest := md5.Sum(payload)
		headers.Set("X-SS-STUB", strings.ToUpper(hex.EncodeToString(digest[:])))
		input.body = json.RawMessage(payload)
	}
	if input.signed {
		if err := c.sign(ctx, endpoint, headers); err != nil {
			return nil, err
		}
	}
	if input.cookie != "" {
		headers.Set("Cookie", input.cookie)
	}
	var reply providerhttp.Object
	if err := providerhttp.JSON(ctx, c.http, method, endpoint, headers, input.body, &reply); err != nil {
		if errors.Is(err, providerhttp.StatusError{Code: http.StatusUnauthorized}) {
			return nil, music.ErrAccountInvalid
		}
		return nil, err
	}
	status := reply.Object("status_info", "statusInfo")
	code := status.Int("status_code", "statusCode")
	if code == 0 {
		code = reply.Int("status_code")
	}
	if code != 0 {
		if msg := strings.ToLower(status.Text("status_msg", "statusMsg")); strings.Contains(msg, "未登录") ||
			strings.Contains(msg, "login required") || strings.Contains(msg, "not logged in") {
			return nil, music.ErrAccountInvalid
		}
		return nil, fmt.Errorf("Qishui returned status %d", code)
	}
	return reply, nil
}

func (c *Client) sign(ctx context.Context, target string, headers http.Header) error {
	var endpoint string
	if c.signer != nil {
		endpoint = strings.TrimSpace(c.signer())
	}
	if endpoint == "" {
		return music.ErrNoURL
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("invalid configured Qishui signer URL")
	}
	items := make([]string, 0, len(headers)*2)
	for _, key := range slices.Sorted(maps.Keys(headers)) {
		for _, value := range headers[key] {
			items = append(items, strings.ToLower(key), value)
		}
	}
	body := map[string]any{"deviceId": c.device, "url": target, "headers": items}
	var result struct {
		OK     bool   `json:"ok"`
		Helios string `json:"X-Helios"`
		Medusa string `json:"X-Medusa"`
	}
	if err := providerhttp.JSON(ctx, c.http, http.MethodPost, u.String(), nil, body, &result); err != nil {
		return err
	}
	if !result.OK || result.Helios == "" || result.Medusa == "" || strings.ContainsAny(result.Helios+result.Medusa, "\r\n") ||
		len(result.Helios)+len(result.Medusa) > 16384 {
		return errors.New("invalid Qishui signing response")
	}
	headers.Set("X-Helios", result.Helios)
	headers.Set("X-Medusa", result.Medusa)
	return nil
}

func (c *Client) Lookup(ctx context.Context, req music.Request) (music.Result, error) {
	switch req.Type {
	case "search":
		return c.search(ctx, req)
	case "search_playlist":
		return c.searchPlaylists(ctx, req)
	case "playlist":
		return c.playlist(ctx, req)
	case "fm":
		return c.fm(ctx, req)
	case "lrc":
		return c.lyrics(ctx, req)
	case "url":
		return c.play(ctx, req)
	case "pic", "song":
		return c.song(ctx, req)
	default:
		return music.Result{}, errors.New("unsupported Qishui operation")
	}
}

func idParam(id string) error {
	if id == "" || len(id) > 128 || strings.ContainsAny(id, "\r\n") {
		return music.ErrInvalidID
	}
	return nil
}
