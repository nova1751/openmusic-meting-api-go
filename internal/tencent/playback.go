package tencent

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
)

type audioQuality struct {
	name, prefix, suffix, label string
}

var audioQualities = []audioQuality{
	{"master", "AI00", ".flac", "臻品母带"},
	{"atmos", "Q000", ".flac", "臻品全景声"},
	{"lossless", "F000", ".flac", "无损"},
	{"exhigh", "M800", ".mp3", "极高"},
	{"standard", "M500", ".mp3", "标准"},
}

func cookieField(cookie, name string) string {
	r := &http.Request{Header: http.Header{"Cookie": {cookie}}}
	item, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return item.Value
}

func allowedAudioURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "qq.com" || strings.HasSuffix(host, ".qq.com") ||
		host == "gtimg.cn" || strings.HasSuffix(host, ".gtimg.cn")
}

func (c *Client) playable(ctx context.Context, address string) bool {
	if !allowedAudioURL(address) {
		return false
	}
	client := *c.http
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 4 || !allowedAudioURL(req.URL.String()) {
			return errors.New("untrusted tencent audio redirect")
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Range", "bytes=0-1")
	req.Header.Set("Referer", "https://y.qq.com/")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	response, err := client.Do(req)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK || response.StatusCode == http.StatusPartialContent
}

func (c *Client) playback(ctx context.Context, req music.Request) (music.Result, error) {
	if !midPattern.MatchString(req.ID) {
		return music.Result{}, music.ErrInvalidID
	}
	quality := music.NormalizeQuality(req.Quality)
	start := slices.IndexFunc(audioQualities, func(q audioQuality) bool { return q.name == quality })
	if start < 0 {
		return music.Result{}, music.ErrNoURL
	}
	mediaMID := req.ID
	if track, err := c.songDetails(ctx, req.ID); err == nil && midPattern.MatchString(track.File.MediaMID) {
		mediaMID = track.File.MediaMID
	} else if ctx.Err() != nil {
		return music.Result{}, ctx.Err()
	}
	uin := first(cookieField(req.Cookie, "uin"), cookieField(req.Cookie, "wxuin"))
	authst := first(cookieField(req.Cookie, "qqmusic_key"), cookieField(req.Cookie, "qm_keyst"))
	random, err := rand.Int(rand.Reader, big.NewInt(10000000))
	if err != nil {
		return music.Result{}, err
	}
	guid := random.String()
	var lastErr error
	for _, level := range audioQualities[start:] {
		payload := map[string]any{
			"req_0": map[string]any{"module": "vkey.GetVkeyServer", "method": "CgiGetVkey", "param": map[string]any{
				"filename": []string{level.prefix + req.ID + mediaMID + level.suffix}, "guid": guid,
				"songmid": []string{req.ID}, "songtype": []int{0}, "uin": uin, "loginflag": 1, "platform": "20",
			}},
			"comm": map[string]any{"uin": uin, "format": "json", "ct": 19, "cv": 0, "authst": authst},
		}
		var result struct {
			Code int `json:"code"`
			Req  struct {
				Code int `json:"code"`
				Data struct {
					RetCode int      `json:"retcode"`
					SIP     []string `json:"sip"`
					Info    []struct {
						PURL   string `json:"purl"`
						Result int    `json:"result"`
					} `json:"midurlinfo"`
				} `json:"data"`
			} `json:"req_0"`
		}
		if err := c.request(ctx, http.MethodPost, "https://u.y.qq.com/cgi-bin/musicu.fcg", req.Cookie, payload, &result); err != nil {
			if ctx.Err() != nil {
				return music.Result{}, ctx.Err()
			}
			lastErr = err
			continue
		}
		if result.Code == 104009 || result.Req.Code == 104009 || result.Req.Data.RetCode == 104009 {
			return music.Result{}, music.ErrAccountInvalid
		}
		if result.Code != 0 || result.Req.Code != 0 || len(result.Req.Data.Info) == 0 {
			continue
		}
		info := result.Req.Data.Info[0]
		if info.Result == 104009 {
			return music.Result{}, music.ErrAccountInvalid
		}
		if info.Result != 0 || info.PURL == "" {
			continue
		}
		for _, domain := range result.Req.Data.SIP {
			base, err := url.Parse(strings.Replace(domain, "http://", "https://", 1))
			if err != nil || !allowedAudioURL(base.String()) {
				continue
			}
			reference, err := url.Parse(info.PURL)
			if err != nil || reference.IsAbs() || reference.Host != "" {
				continue
			}
			address := base.ResolveReference(reference).String()
			if c.playable(ctx, address) {
				return music.Result{Link: music.Link{URL: address, Quality: level.label}}, nil
			}
		}
	}
	if lastErr != nil {
		return music.Result{}, lastErr
	}
	return music.Result{}, music.ErrNoURL
}
