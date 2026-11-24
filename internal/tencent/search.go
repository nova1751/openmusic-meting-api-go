package tencent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
)

type playlistHit struct {
	ID        json.Number `json:"dissid"`
	Name      string      `json:"dissname"`
	Image     string      `json:"imgurl"`
	SongCount int         `json:"song_count"`
	Creator   struct {
		Name string `json:"name"`
	} `json:"creator"`
}

func playlists(hits []playlistHit) (music.Result, error) {
	result := music.Result{Items: make([]music.Item, 0, len(hits))}
	for _, hit := range hits {
		id := string(hit.ID)
		if parsed, err := strconv.ParseUint(id, 10, 63); err != nil || parsed == 0 {
			return music.Result{}, errors.New("invalid tencent playlist identifier")
		}
		image := strings.Replace(hit.Image, "http://", "https://", 1)
		imageURL, err := url.Parse(image)
		if err != nil || imageURL.Scheme != "https" || imageURL.Host == "" || imageURL.User != nil {
			image = ""
		}
		encodedID, _ := json.Marshal(id)
		encodedImage, _ := json.Marshal(image)
		count := hit.SongCount
		result.Items = append(result.Items, music.Item{ID: id, Title: hit.Name, Author: hit.Creator.Name,
			URL: encodedID, Pic: encodedImage, TrackCount: &count})
	}
	return result, nil
}

func (c *Client) search(ctx context.Context, req music.Request) (music.Result, error) {
	if strings.TrimSpace(req.ID) == "" || len(req.ID) > 1024 {
		return music.Result{}, music.ErrInvalidID
	}
	searchType := 0
	if req.Type == "search_playlist" {
		searchType = 3
	}
	uin := cookieField(req.Cookie, "uin")
	key := cookieField(req.Cookie, "qqmusic_key")
	payload := map[string]any{
		"req": map[string]any{"method": "DoSearchForQQMusicDesktop", "module": "music.search.SearchCgiService",
			"param": map[string]any{"query": req.ID, "page_num": 1, "num_per_page": 30, "search_type": searchType}},
		"comm": map[string]any{"uin": uin, "format": "json", "ct": 24, "cv": 0, "authst": key},
	}
	var reply struct {
		Request struct {
			Data struct {
				Body struct {
					Song struct {
						List []track `json:"list"`
					} `json:"song"`
					Songlist struct {
						List []playlistHit `json:"list"`
					} `json:"songlist"`
				} `json:"body"`
			} `json:"data"`
		} `json:"req"`
	}
	if err := c.request(ctx, http.MethodPost, "https://u.y.qq.com/cgi-bin/musicu.fcg", req.Cookie, payload, &reply); err == nil {
		if searchType == 0 && len(reply.Request.Data.Body.Song.List) > 0 {
			return items(reply.Request.Data.Body.Song.List)
		}
		if searchType == 3 && len(reply.Request.Data.Body.Songlist.List) > 0 {
			return playlists(reply.Request.Data.Body.Songlist.List)
		}
	}
	if ctx.Err() != nil {
		return music.Result{}, ctx.Err()
	}
	if searchType == 3 {
		q := url.Values{"remoteplace": {"txt.yqq.playlist"}, "query": {req.ID},
			"page_no": {"0"}, "num_per_page": {"30"}, "format": {"json"}}
		var fallback struct {
			Data struct {
				List []playlistHit `json:"list"`
			} `json:"data"`
		}
		if err := c.request(ctx, http.MethodGet, "https://c.y.qq.com/soso/fcgi-bin/client_music_search_songlist?"+q.Encode(), req.Cookie, nil, &fallback); err != nil {
			return music.Result{}, err
		}
		return playlists(fallback.Data.List)
	}
	q := url.Values{"p": {"1"}, "n": {"30"}, "w": {req.ID}, "format": {"json"}}
	var fallback struct {
		Data struct {
			Song struct {
				List []track `json:"list"`
			} `json:"song"`
		} `json:"data"`
	}
	if err := c.request(ctx, http.MethodGet, "https://c.y.qq.com/soso/fcgi-bin/client_search_cp?"+q.Encode(), req.Cookie, nil, &fallback); err != nil {
		return music.Result{}, err
	}
	return items(fallback.Data.Song.List)
}
