package qishui

import (
	"context"
	"crypto/sha256"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
)

type played struct {
	Duration int64  `json:"duration"`
	ID       string `json:"media_id"`
	Millis   int    `json:"played_mills"`
	Type     string `json:"type"`
}

type feedState struct {
	mu      sync.Mutex
	created time.Time
	first   bool
	recent  []played
}

var preference = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

func feedPreference(mode string) map[string]any {
	lower := strings.ToLower(strings.TrimSpace(mode))
	for _, prefix := range []string{"scene_mode_id:", "scene_mode_id_"} {
		lower = strings.TrimPrefix(lower, prefix)
	}
	if id, err := strconv.ParseUint(lower, 10, 32); err == nil && id > 0 {
		return map[string]any{"scene_mode_id": id}
	}
	for _, prefix := range []string{"preference_mode:", "preference_mode_"} {
		lower = strings.TrimPrefix(lower, prefix)
	}
	if preference.MatchString(lower) {
		return map[string]any{"preference_mode": lower}
	}
	return map[string]any{}
}

func (c *Client) feed(cookie, mode string) *feedState {
	key := sha256.Sum256([]byte(strings.TrimSpace(cookie) + "\x00" + strings.ToUpper(mode)))
	c.feedsMu.Lock()
	defer c.feedsMu.Unlock()
	if old := c.feeds[key]; old != nil {
		return old
	}
	if len(c.feeds) >= 200 {
		var evict [32]byte
		now := time.Now()
		for candidate, state := range c.feeds {
			if !state.created.After(now) {
				evict, now = candidate, state.created
			}
		}
		delete(c.feeds, evict)
	}
	state := &feedState{created: time.Now(), first: true, recent: []played{}}
	c.feeds[key] = state
	return state
}

func (c *Client) fm(ctx context.Context, req music.Request) (music.Result, error) {
	if !fingerprint(req.Cookie) {
		return music.Result{}, music.ErrAccountInvalid
	}
	mode := strings.TrimSpace(req.ID)
	state := c.feed(req.Cookie, mode)
	state.mu.Lock()
	defer state.mu.Unlock()
	body := map[string]any{"feed_counts": map[string]int{"mix_session_count": 1},
		"is_did_first_request": false, "is_first_request": state.first, "played_media": state.recent}
	if mode == "" || strings.EqualFold(mode, "default") {
		body["did_first_use_time"] = state.created.Unix()
	} else {
		body["feed_preference"] = feedPreference(mode)
	}
	res, err := c.request(ctx, upstreamRequest{endpoint: pcHost + "/luna/pc/feed/song-tab", cookie: req.Cookie,
		query: c.params(false, nil), body: body})
	if err != nil {
		return music.Result{}, err
	}
	songs := tracks(collectMedia(res))
	next := make(map[string]played, len(state.recent)+len(songs))
	for _, song := range state.recent {
		next[song.ID] = song
	}
	for _, song := range songs {
		next[song.ID] = played{ID: song.ID, Duration: song.Duration * 1000, Millis: -1, Type: "track"}
	}
	history := make([]played, 0, min(80, len(next)))
	for _, song := range state.recent {
		history = append(history, next[song.ID])
		delete(next, song.ID)
	}
	for _, song := range songs {
		if value, ok := next[song.ID]; ok {
			history = append(history, value)
			delete(next, song.ID)
		}
	}
	state.recent = history[max(0, len(history)-80):]
	state.first = false
	return music.Result{Items: songs}, nil
}
