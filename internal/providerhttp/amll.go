package providerhttp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"
)

const LyricsLimit = 1 << 20

// AMLL returns the TTML associated with a platform's validated track IDs.
func AMLL(ctx context.Context, client *http.Client, idField string, ids ...string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, id := range ids {
		if lyrics := fetchTTML(ctx, client, idField, id); lyrics != "" {
			return lyrics
		}
	}
	return ""
}

func fetchTTML(ctx context.Context, client *http.Client, field, id string) string {
	target := "https://api.amll.dev/v1/lyrics/get?" + url.Values{field: {id}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return ""
	}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, LyricsLimit+1))
	if err != nil || len(body) > LyricsLimit {
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
