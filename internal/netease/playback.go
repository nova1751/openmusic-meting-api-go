package netease

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strconv"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
)

var levels = []string{"dolby", "jymaster", "sky", "jyeffect", "hires", "lossless", "exhigh", "higher", "standard"}

func (c *Client) playback(ctx context.Context, req music.Request) (music.Result, error) {
	ids, err := parseIDs(req.ID)
	if err != nil {
		return music.Result{}, err
	}
	encoded, err := json.Marshal(idsAsNumbers(ids))
	if err != nil {
		return music.Result{}, err
	}
	level := music.NormalizeQuality(req.Quality)
	start := slices.Index(levels, level)
	if start < 0 {
		return music.Result{}, music.ErrNoURL
	}
	ep := endpoint{origin: interfaceOrigin, path: "/api/song/enhance/player/url/v1", crypto: enhanced}
	var lastError error
	var empty bool
	for _, level := range levels[start:] {
		var reply struct {
			Data []struct {
				URL, Level                   string
				Time, Gain, Peak, ClosedGain json.Number
				ClosedPeak                   json.Number
			}
		}
		format := "flac"
		if level == "exhigh" || level == "higher" || level == "standard" && req.Quality != "" {
			format = "mp3"
		}
		fields := parameters{"ids": string(encoded), "level": level, "encodeType": format}
		if level == "sky" {
			fields["immerseType"] = "c51"
		}
		cookie := req.Cookie
		if slices.Contains(levels[:5], level) {
			cookie = setCookie(cookie, "os", "pc")
			if cookieValue(cookie, "appver") == "" {
				cookie = setCookie(cookie, "appver", "3.0.18.203152")
			}
		}
		if err := c.request(ctx, ep, cookie, fields, &reply); err != nil {
			if errors.Is(err, music.ErrAccountInvalid) || ctx.Err() != nil {
				return music.Result{}, err
			}
			lastError = err
			continue
		}
		if len(reply.Data) == 0 {
			empty = true
			continue
		}
		item := reply.Data[0]
		link := validMediaURL(item.URL)
		if link == "" {
			empty = true
			continue
		}
		loudness := make(map[string]float64)
		for name, raw := range map[string]json.Number{
			"gain": item.Gain, "peak": item.Peak, "closedGain": item.ClosedGain, "closedPeak": item.ClosedPeak,
		} {
			value, err := strconv.ParseFloat(string(raw), 64)
			if err == nil && !math.IsNaN(value) && !math.IsInf(value, 0) {
				loudness[name] = value
			}
		}
		if len(loudness) == 0 {
			loudness = nil
		}
		return music.Result{Link: music.Link{URL: link, Quality: music.NeteaseQualityName(first(item.Level, level)),
			Duration: duration(string(item.Time)), Loudness: loudness}}, nil
	}
	if empty {
		return music.Result{}, music.ErrNoURL
	}
	return music.Result{}, lastError
}

func idsAsNumbers(ids []numericID) []json.Number {
	numbers := make([]json.Number, 0, len(ids))
	for _, id := range ids {
		numbers = append(numbers, json.Number(id))
	}
	return numbers
}
