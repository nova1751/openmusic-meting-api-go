package netease

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
)

const lyricsLimit = 1 << 20

type lyricField struct{ Lyric string }

func (c *Client) lyrics(ctx context.Context, req music.Request) (music.Result, error) {
	var plainReply struct{ LRC, TLyric lyricField }
	ep := endpoint{origin: webOrigin, path: "/api/song/lyric?_nmclfl=1", crypto: plain, maxBytes: lyricsLimit}
	err := c.request(ctx, ep, req.Cookie, parameters{
		"id": req.ID, "tv": -1, "lv": -1, "rv": -1, "kv": -1,
	}, &plainReply)
	lyrics := music.Lyrics{Lyric: plainReply.LRC.Lyric, Translation: plainReply.TLyric.Lyric, Source: "lrc"}
	if req.Format != "json" {
		return music.Result{Lyrics: lyrics}, err
	}
	var words struct{ LRC, YRC, YTLRC, TLyric, YRomaLRC, RomaLRC lyricField }
	ep = endpoint{origin: webOrigin, path: "/api/song/lyric/v1", crypto: enhanced, maxBytes: lyricsLimit}
	wordsCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = c.request(wordsCtx, ep, req.Cookie, parameters{
		"id": req.ID, "cp": false, "lv": 0, "tv": 0, "rv": 0, "kv": 0, "yv": 0, "ytv": 0, "yrv": 0,
	}, &words)
	cancel()
	if err == nil {
		lyrics.Lyric = first(words.LRC.Lyric, lyrics.Lyric)
		lyrics.Translation = first(words.YTLRC.Lyric, words.TLyric.Lyric, lyrics.Translation)
		lyrics.Romanized = first(words.YRomaLRC.Lyric, words.RomaLRC.Lyric)
		lyrics.WordLyric = words.YRC.Lyric
		if lyrics.WordLyric != "" {
			lyrics.WordFormat, lyrics.Source = "yrc", "platform"
		}
	}
	if lyrics.WordLyric == "" && len(req.ID) <= 80 {
		if _, err := parseIDs(req.ID); err == nil {
			if ttml := c.amll(ctx, req.ID); ttml != "" {
				lyrics.WordLyric, lyrics.WordFormat, lyrics.Source = ttml, "ttml", "amll"
			}
		}
	}
	return music.Result{Lyrics: lyrics}, nil
}

func (c *Client) amll(ctx context.Context, id string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	target := "https://api.amll.dev/v1/lyrics/get?" + url.Values{"ncmMusicId": {id}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return ""
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, lyricsLimit+1))
	if err != nil || len(body) > lyricsLimit {
		return ""
	}
	var reply struct {
		Status int
		Data   struct{ Format, Lyrics string }
	}
	if json.Unmarshal(body, &reply) == nil && reply.Status == 200 && reply.Data.Format == "ttml" {
		return reply.Data.Lyrics
	}
	return ""
}
