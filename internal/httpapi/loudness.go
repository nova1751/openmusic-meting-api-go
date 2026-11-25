package httpapi

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
	"github.com/nova1751/openmusic-meting-api-go/internal/providerhttp"
)

func (s *Server) wrapMedia(r *http.Request, platform string, link *music.Link) error {
	if platform != "qishui" || link.PlayAuth == "" {
		return nil
	}
	if s.Audio == nil {
		return errors.New("Qishui audio service is unavailable")
	}
	params, err := s.Audio.Issue(*link)
	if err == nil {
		link.URL = strings.TrimSuffix(s.apiBase(r), "/api") + "/audio/qishui?" + params.Encode()
	}
	return err
}

func (s *Server) augmentLoudness(r *http.Request, provider music.Provider, req music.Request, result *music.Result) {
	if s.LoudnessURL == nil || s.LoudnessClient == nil {
		return
	}
	target, err := url.Parse(s.LoudnessURL())
	if err != nil || target.Hostname() == "" || target.User != nil || target.Fragment != "" || (target.Scheme != "https" && target.Scheme != "http") {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	link := result.Link
	if music.NormalizeQuality(req.Quality) != "standard" {
		req.Quality = "standard"
		if req.Cookie == "" {
			req.Cookie = result.PlaybackCookie
		}
		standard, err := provider.Lookup(ctx, req)
		if err != nil || s.wrapMedia(r, req.Platform, &standard.Link) != nil {
			return
		}
		link = standard.Link
	}
	media, err := url.Parse(link.URL)
	if err != nil || media.Hostname() == "" || (media.Scheme != "http" && media.Scheme != "https") {
		return
	}
	query := target.Query()
	query.Set("id", req.ID)
	query.Set("url", link.URL)
	target.RawQuery = query.Encode()
	var payload providerhttp.Object
	if providerhttp.JSON(ctx, s.LoudnessClient, http.MethodGet, target.String(), nil, nil, &payload) != nil {
		return
	}
	info := payload.Object("loudness")
	fields := map[string]float64{}
	for _, key := range []string{"gain", "peak"} {
		if value := info.Text(key); value != "" {
			n, err := strconv.ParseFloat(value, 64)
			if err == nil && !math.IsNaN(n) && !math.IsInf(n, 0) {
				fields[key] = n
			}
		}
	}
	if len(fields) > 0 {
		result.Link.Loudness = fields
	}
}
