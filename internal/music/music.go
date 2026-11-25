package music

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
)

var (
	ErrVerificationRequired = errors.New("provider verification required")
	ErrAccountInvalid       = errors.New("music account invalid")
	ErrNoAccount            = errors.New("music account unavailable")
	ErrNoURL                = errors.New("music URL unavailable")
	ErrInvalidID            = errors.New("invalid music ID")
	LyricID                 = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
)

type Request struct {
	Platform      string
	Type          string
	ID            string
	Quality       string
	StrictQuality bool
	Cookie        string
	Format        string
	FM            bool
	Redirect      bool
	Page          Pagination
}

type Pagination struct {
	Limit    int64
	Offset   int64
	Explicit bool
}

// Item holds provider IDs or direct URLs until the HTTP handler builds public links.
type Item struct {
	Name       string          `json:"name,omitempty"`
	Artist     string          `json:"artist,omitempty"`
	Source     string          `json:"source,omitempty"`
	Cover      string          `json:"cover,omitempty"`
	Creator    string          `json:"creator,omitempty"`
	PlayCount  *int64          `json:"playCount,omitempty"`
	ID         string          `json:"id,omitempty"`
	Title      string          `json:"title"`
	Author     string          `json:"author"`
	Album      *string         `json:"album,omitempty"`
	URL        json.RawMessage `json:"url"`
	Pic        json.RawMessage `json:"pic"`
	Lyric      json.RawMessage `json:"lrc,omitempty"`
	Duration   int64           `json:"duration,omitempty"`
	TrackCount *int            `json:"trackCount,omitempty"`
	SongMID    string          `json:"songmid,omitempty"`
}

type Link struct {
	PlayAuth  string             `json:"-"`
	MediaType string             `json:"-"`
	URL       string             `json:"url"`
	Quality   string             `json:"quality"`
	Duration  int64              `json:"duration,omitempty"`
	Loudness  map[string]float64 `json:"loudness"`
}

type Lyrics struct {
	Lyric       string
	Translation string
	Romanized   string
	WordLyric   string
	WordFormat  string
	Source      string
}

type Result struct {
	PlaybackCookie string `json:"-"`
	Items          []Item
	Link           Link
	Lyrics         Lyrics
	Image          string
}

type Provider interface {
	Lookup(context.Context, Request) (Result, error)
}
