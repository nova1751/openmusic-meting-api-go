package netease

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/qq01-hub/openmusic-meting-api-go/internal/music"
)

type numericID string

func (id *numericID) UnmarshalJSON(data []byte) error {
	var value json.Number
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*id = numericID(value)
	return nil
}

func rawString(value string) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}

func (id numericID) raw() json.RawMessage {
	if id == "" {
		return rawString("")
	}
	return json.RawMessage(id)
}

func parseIDs(value string) ([]numericID, error) {
	const maxIDs = 200
	if len(value) > maxIDs*21-1 || strings.Count(value, ",") >= maxIDs {
		return nil, music.ErrInvalidID
	}
	var ids []numericID
	for part := range strings.SplitSeq(value, ",") {
		n, err := strconv.ParseUint(part, 10, 64)
		if err != nil || len(part) > 20 {
			return nil, music.ErrInvalidID
		}
		ids = append(ids, numericID(strconv.FormatUint(n, 10)))
	}
	return ids, nil
}

type artist struct {
	Name string
}

type album struct {
	Name   string
	PicURL string
}

type song struct {
	ID       numericID
	Name     string
	AR       []artist
	Artists  []artist
	AL       album
	Album    album
	DT       json.Number
	Duration json.Number
}

func (s song) item() music.Item {
	artists := s.AR
	if artists == nil {
		artists = s.Artists
	}
	names := make([]string, 0, len(artists))
	for _, performer := range artists {
		names = append(names, performer.Name)
	}
	title := first(s.AL.Name, s.Album.Name)
	pic := s.ID.raw()
	if s.AL.PicURL != "" {
		pic = rawString(s.AL.PicURL)
	}
	return music.Item{
		Title: s.Name, Album: &title, Author: strings.Join(names, " / "), Pic: pic,
		URL: s.ID.raw(), Lyric: s.ID.raw(), Duration: duration(first(string(s.DT), string(s.Duration))),
	}
}

func duration(milliseconds string) int64 {
	value, err := strconv.ParseFloat(milliseconds, 64)
	if err != nil || !(value > 0) || value >= float64(math.MaxInt64) {
		return 0
	}
	return int64(math.Round(value))
}

func first(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (c *Client) Lookup(ctx context.Context, req music.Request) (music.Result, error) {
	switch req.Type {
	case "song", "pic":
		ids, err := parseIDs(req.ID)
		if err != nil {
			return music.Result{}, err
		}
		songs, err := c.songDetails(ctx, ids, req.Cookie)
		if err != nil {
			return music.Result{}, err
		}
		if req.Type == "pic" {
			if len(songs) == 0 {
				return music.Result{}, music.ErrNoURL
			}
			image := validMediaURL(songs[0].AL.PicURL)
			if image == "" {
				return music.Result{}, music.ErrNoURL
			}
			return music.Result{Image: image}, nil
		}
		return items(songs), nil
	case "playlist":
		return c.playlist(ctx, req)
	case "search", "search_playlist", "search_dj":
		return c.search(ctx, req)
	case "artist":
		ids, err := parseIDs(req.ID)
		if err != nil || len(ids) != 1 {
			return music.Result{}, music.ErrInvalidID
		}
		var reply struct{ Songs []song }
		err = c.request(ctx, webAPI("/api/artist/top/song"), req.Cookie, parameters{"id": json.Number(ids[0])}, &reply)
		return items(reply.Songs), err
	case "url":
		return c.playback(ctx, req)
	case "lrc":
		return c.lyrics(ctx, req)
	case "fm":
		return c.personalFM(ctx, req)
	case "dj", "dj_detail", "djprogram", "dj_hot":
		return c.radio(ctx, req)
	default:
		return music.Result{}, errors.New("unsupported netease operation")
	}
}

func items(songs []song) music.Result {
	result := music.Result{Items: make([]music.Item, 0, len(songs))}
	for _, track := range songs {
		result.Items = append(result.Items, track.item())
	}
	return result
}

func (c *Client) songDetails(ctx context.Context, ids []numericID, cookie string) ([]song, error) {
	type idEntry struct {
		ID json.Number `json:"id"`
	}
	entries := make([]idEntry, 0, len(ids))
	for _, id := range ids {
		entries = append(entries, idEntry{ID: json.Number(id)})
	}
	body, err := json.Marshal(entries)
	if err != nil {
		return nil, err
	}
	var reply struct{ Songs []song }
	if err := c.request(ctx, webAPI("/api/v3/song/detail"), cookie, parameters{"c": string(body)}, &reply); err != nil {
		return nil, err
	}
	if reply.Songs == nil {
		return nil, errors.New("netease song details missing songs")
	}
	return reply.Songs, nil
}

func (c *Client) playlist(ctx context.Context, req music.Request) (music.Result, error) {
	var reply struct {
		Playlist *struct{ TrackIDs []struct{ ID numericID } }
	}
	ep := endpoint{origin: webOrigin, path: "/api/v6/playlist/detail", crypto: plain}
	if err := c.request(ctx, ep, req.Cookie, parameters{"id": req.ID, "n": 100000, "s": 8}, &reply); err != nil {
		return music.Result{}, err
	}
	if reply.Playlist == nil {
		return music.Result{}, errors.New("netease playlist missing details")
	}
	ids := make([]numericID, 0, len(reply.Playlist.TrackIDs))
	for _, entry := range reply.Playlist.TrackIDs {
		if entry.ID != "" {
			ids = append(ids, entry.ID)
		}
	}
	var songs []song
	for batch := range slices.Chunk(ids, 200) {
		found, err := c.songDetails(ctx, batch, req.Cookie)
		if err != nil {
			return music.Result{}, err
		}
		songs = append(songs, found...)
	}
	return items(songs), nil
}

type playlistHit struct {
	ID          numericID
	Name        string
	CoverImgURL string
	TrackCount  int
	Creator     struct{ Nickname string }
}

func (c *Client) search(ctx context.Context, req music.Request) (music.Result, error) {
	kind, limit, offset := 1, int64(30), int64(0)
	switch req.Type {
	case "search_playlist":
		kind = 1000
	case "search_dj":
		kind = 1009
	default:
		if req.Page.Explicit {
			limit, offset = req.Page.Limit, req.Page.Offset
		}
	}
	var reply struct {
		Result struct {
			Songs     []song
			Playlists []playlistHit
			DJRadios  []station
		}
	}
	ep := endpoint{origin: interfaceOrigin, path: "/api/cloudsearch/pc", wirePath: "/eapi/cloudsearch", crypto: enhanced}
	fields := parameters{"s": req.ID, "type": kind, "limit": limit, "offset": offset, "total": true}
	if err := c.request(ctx, ep, req.Cookie, fields, &reply); err != nil {
		return music.Result{}, err
	}
	switch kind {
	case 1000:
		result := music.Result{Items: make([]music.Item, 0, len(reply.Result.Playlists))}
		for _, hit := range reply.Result.Playlists {
			item := music.Item{Title: hit.Name, Author: hit.Creator.Nickname, ID: string(hit.ID),
				URL: rawString(string(hit.ID)), Pic: rawString(hit.CoverImgURL), TrackCount: &hit.TrackCount}
			result.Items = append(result.Items, item)
		}
		return result, nil
	case 1009:
		return stationItems(reply.Result.DJRadios), nil
	default:
		return items(reply.Result.Songs), nil
	}
}

func validMediaURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	u.Scheme = "https"
	return u.String()
}
