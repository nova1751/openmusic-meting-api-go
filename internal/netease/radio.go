package netease

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/qq01-hub/openmusic-meting-api-go/internal/music"
)

type station struct {
	ID              numericID
	Name            string
	PicURL          string
	IntervenePicURL string
	ProgramCount    int
	DJ              struct{ Nickname string }
}

func stationItems(stations []station) music.Result {
	result := music.Result{Items: make([]music.Item, 0, len(stations))}
	for _, radio := range stations {
		result.Items = append(result.Items, music.Item{
			ID: string(radio.ID), Title: radio.Name, Author: radio.DJ.Nickname,
			Pic: rawString(first(radio.PicURL, radio.IntervenePicURL)), URL: rawString(string(radio.ID)),
			TrackCount: &radio.ProgramCount,
		})
	}
	return result
}

type program struct {
	ID       numericID
	Name     string
	CoverURL string
	MainSong song
	DJ       struct{ Nickname string }
}

func (p program) item() music.Item {
	track := p.MainSong.item()
	track.ID = string(p.ID)
	track.Title = first(p.Name, track.Title)
	track.Author = first(track.Author, p.DJ.Nickname)
	track.Duration = 0
	id := numericID(first(string(p.MainSong.ID), string(p.ID)))
	track.URL, track.Lyric, track.Pic = id.raw(), id.raw(), id.raw()
	if cover := first(p.CoverURL, p.MainSong.Album.PicURL); cover != "" {
		track.Pic = rawString(cover)
	}
	return track
}

func (c *Client) radio(ctx context.Context, req music.Request) (music.Result, error) {
	switch req.Type {
	case "dj":
		return c.programs(ctx, req)
	case "dj_detail":
		var reply struct{ Data, DJRadio *station }
		err := c.request(ctx, webAPI("/api/djradio/v2/get"), req.Cookie, parameters{"id": req.ID}, &reply)
		if err != nil {
			return music.Result{}, err
		}
		if reply.Data != nil {
			return stationItems([]station{*reply.Data}), nil
		}
		if reply.DJRadio != nil {
			return stationItems([]station{*reply.DJRadio}), nil
		}
		return music.Result{}, errors.New("netease radio missing details")
	case "djprogram":
		var reply struct{ Program *program }
		err := c.request(ctx, webAPI("/api/dj/program/detail"), req.Cookie, parameters{"id": req.ID}, &reply)
		if err != nil {
			return music.Result{}, err
		}
		if reply.Program == nil {
			return music.Result{}, errors.New("netease program missing details")
		}
		return music.Result{Items: []music.Item{reply.Program.item()}}, nil
	default:
		var reply struct{ DJRadios []station }
		path, fields := "/api/djradio/hot/v1", parameters{"limit": 30, "offset": 0}
		if strings.EqualFold(req.ID, "recommend") {
			path, fields = "/api/djradio/recommend/v1", parameters{}
		}
		err := c.request(ctx, webAPI(path), req.Cookie, fields, &reply)
		return stationItems(reply.DJRadios), err
	}
}

func (c *Client) programs(ctx context.Context, req music.Request) (music.Result, error) {
	limit, offset := int64(100), int64(0)
	if req.Page.Explicit {
		limit, offset = req.Page.Limit, req.Page.Offset
	}
	result := music.Result{Items: make([]music.Item, 0)}
	seen := make(map[numericID]bool)
	for {
		var reply struct {
			Programs []program
			More     *bool
			Count    int64
		}
		var err error
		for attempt := 0; ; attempt++ {
			err = c.request(ctx, webAPI("/api/dj/program/byradio"), req.Cookie,
				parameters{"radioId": req.ID, "limit": limit, "offset": offset, "asc": false}, &reply)
			if !rateLimited(err) || attempt == 2 {
				break
			}
			if err := wait(ctx, c.djPageDelay*2*time.Duration(attempt+1)); err != nil {
				return music.Result{}, err
			}
		}
		if err != nil {
			if rateLimited(err) && len(result.Items) > 0 {
				return result, nil
			}
			return music.Result{}, err
		}
		previous := len(result.Items)
		for _, p := range reply.Programs {
			id := numericID(first(string(p.ID), string(p.MainSong.ID)))
			if !seen[id] {
				seen[id] = true
				result.Items = append(result.Items, p.item())
			}
		}
		count := int64(len(reply.Programs))
		offset += count
		if req.Page.Explicit || len(result.Items) == previous ||
			(reply.More != nil && !*reply.More) || (reply.Count > 0 && offset >= reply.Count) ||
			(reply.More == nil && reply.Count <= 0 && count < limit) {
			return result, nil
		}
		if err := wait(ctx, c.djPageDelay); err != nil {
			return music.Result{}, err
		}
	}
}

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func rateLimited(err error) bool {
	var upstream *upstreamError
	if !errors.As(err, &upstream) {
		return false
	}
	return upstream.code == 429 || upstream.code == 455 ||
		strings.Contains(strings.ToLower(upstream.message), "rate limit") ||
		strings.Contains(upstream.message, "频繁")
}

func (c *Client) personalFM(ctx context.Context, req music.Request) (music.Result, error) {
	fields := parameters{}
	mode, subMode, _ := strings.Cut(strings.ToUpper(req.ID), ":")
	switch mode {
	case "AIDJ", "DEFAULT", "FAMILIAR", "EXPLORE", "SCENE_RCMD":
		if mode == "AIDJ" {
			mode = "aidj"
		}
		fields["mode"], fields["limit"] = mode, 3
		switch subMode {
		case "EXERCISE", "FOCUS", "NIGHT_EMO":
			fields["subMode"] = subMode
		}
	}
	var reply struct{ Data []song }
	err := c.request(ctx, webAPI("/api/v1/radio/get"), req.Cookie, fields, &reply)
	return items(reply.Data), err
}
