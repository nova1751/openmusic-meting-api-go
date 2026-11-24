// Package tencent implements the public QQ Music catalog endpoints. Account
// management remains separate from the provider.
package tencent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
)

const maxResponse = 16 << 20

var midPattern = regexp.MustCompile(`^[A-Za-z0-9]{1,64}$`)

type Client struct{ http *http.Client }

func New(client *http.Client) *Client { return &Client{http: client} }

// Supports deliberately excludes account-specific FM until its selection and
// failure semantics have been ported.
func Supports(kind string) bool {
	switch kind {
	case "playlist", "song", "pic", "lrc", "url", "search", "search_playlist":
		return true
	default:
		return false
	}
}

func (c *Client) Supports(kind string) bool { return Supports(kind) }

func (c *Client) Lookup(ctx context.Context, req music.Request) (music.Result, error) {
	switch req.Type {
	case "playlist":
		return c.playlist(ctx, req)
	case "song", "pic":
		return c.song(ctx, req)
	case "lrc":
		return c.lyrics(ctx, req)
	case "url":
		return c.playback(ctx, req)
	case "search", "search_playlist":
		return c.search(ctx, req)
	default:
		return music.Result{}, errors.New("unsupported tencent operation")
	}
}

type track struct {
	SongMID   string `json:"songmid"`
	MID       string `json:"mid"`
	SongName  string `json:"songname"`
	Name      string `json:"name"`
	Title     string `json:"title"`
	AlbumMID  string `json:"albummid"`
	AlbumName string `json:"albumname"`
	Album     struct {
		MID  string `json:"mid"`
		Name string `json:"name"`
	} `json:"album"`
	Singer []struct {
		Name string `json:"name"`
	} `json:"singer"`
	Interval int64 `json:"interval"`
	File     struct {
		MediaMID string `json:"media_mid"`
	} `json:"file"`
}

func first(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (t track) item() (music.Item, error) {
	mid := first(t.SongMID, t.MID)
	if !midPattern.MatchString(mid) {
		return music.Item{}, errors.New("invalid tencent track identifier")
	}
	name := first(t.SongName, t.Name, t.Title)
	album := first(t.AlbumName, t.Album.Name)
	albumMID := first(t.AlbumMID, t.Album.MID)
	cover := ""
	if midPattern.MatchString(albumMID) {
		cover = "https://y.gtimg.cn/music/photo_new/T002R300x300M000" + albumMID + ".jpg"
	}
	artists := make([]string, 0, len(t.Singer))
	for _, singer := range t.Singer {
		artists = append(artists, singer.Name)
	}
	urlID, _ := json.Marshal(mid)
	image, _ := json.Marshal(cover)
	return music.Item{ID: mid, Title: name, Album: &album, Author: strings.Join(artists, " / "),
		SongMID: mid, URL: urlID, Lyric: urlID, Pic: image, Duration: t.Interval * 1000}, nil
}

func items(tracks []track) (music.Result, error) {
	result := music.Result{Items: make([]music.Item, 0, len(tracks))}
	for _, track := range tracks {
		item, err := track.item()
		if err != nil {
			return music.Result{}, err
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func (c *Client) request(ctx context.Context, method, address, cookie string, payload any, out any) error {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, address, body)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Referer", "https://y.qq.com/")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("tencent upstream returned HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponse+1))
	if err != nil {
		return err
	}
	if len(data) > maxResponse {
		return errors.New("tencent response exceeds size limit")
	}
	if err := json.Unmarshal(data, out); err != nil {
		return errors.New("invalid tencent response")
	}
	return nil
}

func (c *Client) playlist(ctx context.Context, req music.Request) (music.Result, error) {
	id, err := strconv.ParseUint(req.ID, 10, 63)
	if err != nil || id == 0 {
		return music.Result{}, music.ErrInvalidID
	}
	uin := first(cookieField(req.Cookie, "uin"), cookieField(req.Cookie, "wxuin"), "0")
	query := url.Values{"type": {"1"}, "utf8": {"1"}, "disstid": {strconv.FormatUint(id, 10)},
		"loginUin": {uin}, "format": {"json"}}
	var old struct {
		CDList []struct {
			Songs []track `json:"songlist"`
		} `json:"cdlist"`
	}
	legacy := "https://c.y.qq.com/qzone/fcg-bin/fcg_ucc_getcdinfo_byids_cp.fcg?" + query.Encode()
	if err := c.request(ctx, http.MethodGet, legacy, req.Cookie, nil, &old); err == nil &&
		len(old.CDList) > 0 && old.CDList[0].Songs != nil {
		return items(old.CDList[0].Songs)
	}
	var tracks []track
	for offset := 0; offset < 10000; {
		payload := map[string]any{
			"comm": map[string]any{"cv": 4747474, "ct": 24, "format": "json", "platform": "yqq.json", "uin": uin},
			"req_1": map[string]any{"module": "music.srfDissInfo.aiDissInfo", "method": "uniform_get_Dissinfo",
				"param": map[string]any{"disstid": id, "userinfo": 1, "tag": 1, "orderlist": 1,
					"song_begin": offset, "song_num": 1000, "onlysonglist": 0, "enc_host_uin": ""}},
		}
		var response struct {
			Code    int `json:"code"`
			Request struct {
				Code int `json:"code"`
				Data struct {
					Songs     []track `json:"songlist"`
					SongCount int     `json:"songnum"`
					DirInfo   struct {
						SongCount int `json:"songnum"`
					} `json:"dirinfo"`
				} `json:"data"`
			} `json:"req_1"`
		}
		if err := c.request(ctx, http.MethodPost, "https://u.y.qq.com/cgi-bin/musicu.fcg", req.Cookie, payload, &response); err != nil {
			return music.Result{}, err
		}
		if response.Code != 0 || response.Request.Code != 0 || response.Request.Data.Songs == nil {
			return music.Result{}, errors.New("tencent playlist unavailable")
		}
		page := response.Request.Data.Songs
		tracks = append(tracks, page...)
		offset += len(page)
		total := response.Request.Data.DirInfo.SongCount
		if total == 0 {
			total = response.Request.Data.SongCount
		}
		if len(page) < 1000 || (total > 0 && offset >= total) {
			return items(tracks)
		}
	}
	return music.Result{}, errors.New("tencent playlist exceeds size limit")
}

func (c *Client) song(ctx context.Context, req music.Request) (music.Result, error) {
	track, err := c.songDetails(ctx, req.ID)
	if err != nil {
		return music.Result{}, err
	}
	item, err := track.item()
	if err != nil {
		return music.Result{}, err
	}
	if req.Type == "pic" {
		var image string
		if json.Unmarshal(item.Pic, &image) != nil || image == "" {
			return music.Result{}, music.ErrNoURL
		}
		return music.Result{Image: image}, nil
	}
	return music.Result{Items: []music.Item{item}}, nil
}

func (c *Client) songDetails(ctx context.Context, id string) (track, error) {
	if !midPattern.MatchString(id) {
		return track{}, music.ErrInvalidID
	}
	payload := map[string]any{"songinfo": map[string]any{"method": "get_song_detail_yqq",
		"module": "music.pf_song_detail_svr", "param": map[string]any{"song_mid": id}}}
	var reply struct {
		SongInfo struct {
			Code int `json:"code"`
			Data struct {
				Track track `json:"track_info"`
			} `json:"data"`
		} `json:"songinfo"`
	}
	if err := c.request(ctx, http.MethodPost, "https://u.y.qq.com/cgi-bin/musicu.fcg", "", payload, &reply); err != nil {
		return track{}, err
	}
	if reply.SongInfo.Code != 0 {
		return track{}, errors.New("tencent track unavailable")
	}
	return reply.SongInfo.Data.Track, nil
}

func (c *Client) lyrics(ctx context.Context, req music.Request) (music.Result, error) {
	if !midPattern.MatchString(req.ID) {
		return music.Result{}, music.ErrInvalidID
	}
	query := url.Values{"songmid": {req.ID}, "format": {"json"}, "g_tk": {"5381"},
		"inCharset": {"utf8"}, "outCharset": {"utf-8"}, "platform": {"yqq"}, "needNewCode": {"0"}}
	var reply struct {
		Lyric string `json:"lyric"`
		Trans string `json:"trans"`
	}
	address := "https://c.y.qq.com/lyric/fcgi-bin/fcg_query_lyric_new.fcg?" + query.Encode()
	if err := c.request(ctx, http.MethodGet, address, req.Cookie, nil, &reply); err != nil {
		return music.Result{}, err
	}
	lyric, err := base64.StdEncoding.DecodeString(reply.Lyric)
	if err != nil {
		return music.Result{}, errors.New("invalid tencent lyric encoding")
	}
	translation, err := base64.StdEncoding.DecodeString(reply.Trans)
	if err != nil {
		return music.Result{}, errors.New("invalid tencent translation encoding")
	}
	return music.Result{Lyrics: music.Lyrics{Lyric: string(lyric), Translation: string(translation)}}, nil
}
