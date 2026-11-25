package tencent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/providerhttp"
	"github.com/nova1751/openmusic-meting-api-go/internal/tencent/qrc"
)

var qrcTiming = regexp.MustCompile(`\[\d+,\d+\]`)

func (c *Client) lyricsRequest(ctx context.Context, module, method, cookie string, params map[string]any, output any) error {
	body := map[string]any{
		"comm":    map[string]any{"ct": 11, "cv": "1003006", "tmeAppID": "qqmusiclight"},
		"request": map[string]any{"module": module, "method": method, "param": params},
	}
	var response struct {
		Code    int
		Request struct {
			Code int
			Data json.RawMessage
		}
	}
	headers := http.Header{"Referer": {"https://y.qq.com"}, "User-Agent": {"okhttp/3.14.9"}}
	if cookie != "" {
		headers.Set("Cookie", cookie)
	}
	if err := providerhttp.JSON(ctx, c.http, http.MethodPost, "https://u.y.qq.com/cgi-bin/musicu.fcg", headers, body, &response); err != nil {
		return err
	}
	if response.Code != 0 || response.Request.Code != 0 || len(response.Request.Data) > providerhttp.LyricsLimit {
		return errors.New("invalid QQ lyric response")
	}
	return json.Unmarshal(response.Request.Data, output)
}

func (c *Client) wordLyrics(ctx context.Context, id, cookie string) (numericID, word, translation, romanized string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	params := map[string]any{"song_mid": id}
	if numeric, err := strconv.ParseUint(id, 10, 64); err == nil {
		params = map[string]any{"song_id": numeric}
	}
	var detail struct {
		Track track `json:"track_info"`
	}
	err := c.lyricsRequest(ctx, "music.pf_song_detail_svr", "get_song_detail_yqq", cookie, params, &detail)
	if err != nil || detail.Track.ID == "" {
		return "", "", "", ""
	}
	song := detail.Track
	numericID = song.ID.String()
	text64 := func(value string) string { return base64.StdEncoding.EncodeToString([]byte(value)) }
	names := make([]string, 0, len(song.Singer))
	for _, item := range song.Singer {
		names = append(names, item.Name)
	}
	params = map[string]any{"songID": song.ID, "songName": text64(first(song.Name, song.SongName, song.Title)),
		"albumName": text64(first(song.Album.Name, song.AlbumName)), "singerName": text64(strings.Join(names, ", ")),
		"interval": song.Interval, "crypt": 1, "ct": 19, "cv": 2111, "lrc_t": 0, "qrc": 1, "qrc_t": 0, "roma": 1,
		"roma_t": 0, "trans": 1, "trans_t": 0, "type": 0,
	}
	var data struct{ Lyric, Trans, Roma string }
	if err := c.lyricsRequest(ctx, "music.musichallSong.PlayLyricInfo", "GetPlayLyricInfo", cookie, params, &data); err != nil {
		return numericID, "", "", ""
	}
	word, _ = qrc.Decode(data.Lyric)
	if !qrcTiming.MatchString(word) {
		word = ""
	}
	translation, _ = qrc.Decode(data.Trans)
	romanized, _ = qrc.Decode(data.Roma)
	return numericID, word, translation, romanized
}
