package kugou

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
	"github.com/nova1751/openmusic-meting-api-go/internal/providerhttp"
)

const (
	gateway      = "https://gateway.kugou.com"
	lyricsHost   = "https://lyrics.kugou.com"
	playlistHost = "https://pubsongscdn.kugou.com"
)

type profile struct {
	appID, version, signSalt, trackSalt, pageID, playerID, playPages string
}

var (
	lite     = profile{"3116", "11440", "LnT6xpN3khm36zse0QzvmgTZ3waWdRSA", "185672dd44712f60bb1736df5a377e82", "967177915", "411", "356753938,823673182,967485191"}
	standard = profile{"1005", "20489", "OIlwieks28dk2k092lksi2UIkp", "57ae12eb6890223e355ccfcb74edf70d", "151369488", "2", "463467626,350369493,788954147"}
)

type Client struct {
	http     *http.Client
	guid     string
	deviceMu sync.Mutex
	devices  map[[32]byte]deviceEntry
}

func New(client *http.Client) *Client {
	var random [16]byte
	rand.Read(random[:])
	return &Client{http: client, guid: digest(hex.EncodeToString(random[:])), devices: make(map[[32]byte]deviceEntry)}
}

type request struct {
	address, router, cookie string
	params                  map[string]string
	omitCookie              bool
	body                    any
	profile                 profile
}

type serviceError int

func (e serviceError) Error() string { return fmt.Sprintf("Kugou Music returned code %d", e) }

func digest(value string) string {
	sum := md5.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

func signature(params map[string]string, body, salt string) string {
	var buf strings.Builder
	buf.WriteString(salt)
	for _, key := range slices.Sorted(maps.Keys(params)) {
		buf.WriteString(key)
		buf.WriteByte('=')
		buf.WriteString(params[key])
	}
	buf.WriteString(body)
	buf.WriteString(salt)
	return digest(buf.String())
}

func deviceMID(cookie string) string {
	return providerhttp.First(providerhttp.CookieValue(cookie, "KUGOU_API_MID", "mid", "kg_mid"), "0")
}

func (c *Client) call(ctx context.Context, input request) (providerhttp.Object, error) {
	query := make(url.Values)
	headers := http.Header{"Accept": {"application/json, text/plain, */*"},
		"User-Agent": {"Android15-1070-11083-46-0-DiscoveryDRADProtocol-wifi"}}
	if input.router != "" {
		headers.Set("X-Router", input.router)
	}
	if input.cookie != "" && !input.omitCookie {
		headers.Set("Cookie", input.cookie)
	}
	method, bodyText := http.MethodGet, ""
	if input.body != nil {
		method = http.MethodPost
		data, err := json.Marshal(input.body)
		if err != nil {
			return nil, err
		}
		bodyText = string(data)
		input.body = json.RawMessage(data)
	}
	params := input.params
	if input.profile.appID != "" {
		profile := input.profile
		clientTime := strconv.FormatInt(time.Now().Unix(), 10)
		dfid := providerhttp.First(providerhttp.CookieValue(input.cookie, "dfid", "kg_dfid"), "-")
		mid := deviceMID(input.cookie)
		params = map[string]string{"dfid": dfid, "mid": mid, "uuid": "-", "appid": profile.appID,
			"clientver": profile.version, "clienttime": clientTime}
		maps.Copy(params, input.params)
		for _, name := range []string{"token", "userid"} {
			if value := providerhttp.CookieValue(input.cookie, name); value != "" {
				params[name] = value
			}
		}
		params["signature"] = signature(params, bodyText, profile.signSalt)
		headers.Set("Dfid", dfid)
		headers.Set("Mid", mid)
		headers.Set("Clienttime", clientTime)
	}
	for k, v := range params {
		query.Set(k, v)
	}
	endpoint := input.address
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var data providerhttp.Object
	if err := providerhttp.JSON(ctx, c.http, method, endpoint, headers, input.body, &data); err != nil {
		if errors.Is(err, providerhttp.StatusError{Code: 401}) {
			return nil, music.ErrAccountInvalid
		}
		return nil, err
	}
	switch code := data.Int("error_code"); code {
	case 0:
		return data, nil
	case 35005:
		return nil, music.ErrAccountInvalid
	default:
		return nil, serviceError(code)
	}
}

func (c *Client) Lookup(ctx context.Context, req music.Request) (music.Result, error) {
	switch req.Type {
	case "search", "search_playlist":
		return c.search(ctx, req)
	case "playlist":
		return c.playlist(ctx, req)
	case "fm":
		return c.fm(ctx, req.Cookie)
	case "pic", "song", "url", "lrc":
		hash, albumAudioID, err := parseID(req.ID)
		if err != nil {
			return music.Result{}, err
		}
		switch req.Type {
		case "url":
			return c.play(ctx, req, hash, albumAudioID)
		case "lrc":
			return c.lyric(ctx, req, hash)
		default:
			return c.song(ctx, req, hash, albumAudioID)
		}
	}
	return music.Result{}, errors.New("unsupported Kugou Music operation")
}
