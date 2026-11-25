package qishui

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
)

const (
	maxAudio  = 128 << 20
	maxMemory = 512 << 20
	tokenTTL  = 30 * time.Minute
	cacheTTL  = 6 * time.Hour
)

type playToken struct {
	target, auth, mime string
	expires            time.Time
}

type audioEntry struct {
	cachedAt    string
	data        []byte
	contentType string
	last        time.Time
}

type loading struct {
	done  chan struct{}
	entry audioEntry
	err   error
}

type Audio struct {
	disk       *diskCache
	client     *http.Client
	mu         sync.Mutex
	tokens     map[string]playToken
	cache      map[[sha1.Size]byte]audioEntry
	inflight   map[[sha1.Size]byte]*loading
	cacheBytes int
	slots      chan struct{}
}

func NewAudio(httpClient *http.Client) *Audio {
	client := *httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Audio{client: &client, tokens: make(map[string]playToken), cache: make(map[[sha1.Size]byte]audioEntry),
		inflight: make(map[[sha1.Size]byte]*loading), slots: make(chan struct{}, 2)}
}

func validMediaURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && len(value) < 8192 && u.Scheme == "https" && u.User == nil && u.Port() == "" && u.Fragment == "" &&
		strings.HasSuffix(strings.ToLower(u.Hostname()), ".douyinvod.com")
}

func contentKey(p playToken) [sha1.Size]byte {
	return sha1.Sum([]byte(p.target + "\n" + p.auth))
}

func (a *Audio) Issue(link music.Link) (url.Values, error) {
	if !validMediaURL(link.URL) || len(link.PlayAuth) > 4096 {
		return nil, errors.New("unsupported Qishui audio source")
	}
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(bytes[:])
	now := time.Now()
	entry := playToken{target: link.URL, auth: link.PlayAuth, mime: link.MediaType, expires: now.Add(tokenTTL)}
	a.mu.Lock()
	defer a.mu.Unlock()
	var oldestName string
	for name, current := range a.tokens {
		if !current.expires.After(now) {
			delete(a.tokens, name)
			continue
		}
		if oldestName == "" || current.expires.Before(a.tokens[oldestName].expires) {
			oldestName = name
		}
	}
	if len(a.tokens) >= 1000 {
		delete(a.tokens, oldestName)
	}
	a.tokens[token] = entry
	key := contentKey(entry)
	params := url.Values{"t": {token}, "k": {hex.EncodeToString(key[:])}}
	if link.MediaType != "" {
		params.Set("mime_type", link.MediaType)
	}
	return params, nil
}

func (a *Audio) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("t"))
	if token == "" {
		audioError(w, 400, "missing qishui play token")
		return
	}
	a.mu.Lock()
	entry, found := a.tokens[token]
	if found && !entry.expires.After(time.Now()) {
		delete(a.tokens, token)
		found = false
	}
	a.mu.Unlock()
	if !found || !validMediaURL(entry.target) {
		audioError(w, 403, "qishui play token expired")
		return
	}
	if strings.EqualFold(r.URL.Query().Get("mode"), "source") {
		kind := entry.mime
		if kind == "" {
			kind = "audio/mp4"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"url": entry.target, "auth": entry.auth, "mimeType": kind})
		return
	}
	key := contentKey(entry)
	w.Header().Set("ETag", `"`+hex.EncodeToString(key[:])+`"`)
	if r.Header.Get("If-None-Match") == w.Header().Get("ETag") {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	audio, err := a.load(r.Context(), key, entry)
	if err != nil {
		w.Header().Del("ETag")
		audioError(w, 502, "qishui audio unavailable")
		return
	}
	w.Header().Set("Content-Type", audio.contentType)
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Qishui-Cache", audio.cachedAt)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(audio.data))
}

func audioError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func (a *Audio) load(ctx context.Context, key [sha1.Size]byte, entry playToken) (audioEntry, error) {
	a.mu.Lock()
	now := time.Now()
	if cached, ok := a.cache[key]; ok {
		if now.Sub(cached.last) < cacheTTL {
			cached.last = now
			a.cache[key] = cached
			cached.cachedAt = "memory"
			a.mu.Unlock()
			return cached, nil
		}
		a.cacheBytes -= len(cached.data)
		delete(a.cache, key)
	}
	work, found := a.inflight[key]
	if !found {
		select {
		case a.slots <- struct{}{}:
			work = &loading{done: make(chan struct{})}
			a.inflight[key] = work
			go a.fetch(key, entry, work)
		default:
			a.mu.Unlock()
			return audioEntry{}, errors.New("audio proxy is busy")
		}
	}
	a.mu.Unlock()
	select {
	case <-work.done:
		return work.entry, work.err
	case <-ctx.Done():
		return audioEntry{}, ctx.Err()
	}
}

func (a *Audio) fetch(key [sha1.Size]byte, entry playToken, work *loading) {
	defer func() { <-a.slots }()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	data, mime, hit := a.disk.read(key)
	source := "disk"
	var err error
	if !hit {
		source = "miss"
		data, mime, err = a.download(ctx, entry)
		if err == nil {
			a.disk.write(key, data)
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	defer close(work.done)
	delete(a.inflight, key)
	work.err = err
	if err != nil {
		return
	}
	work.entry = audioEntry{data: data, contentType: mime, last: time.Now(), cachedAt: source}
	for a.cacheBytes+len(data) > maxMemory && len(a.cache) > 0 {
		var oldest [sha1.Size]byte
		var before time.Time
		for candidate, value := range a.cache {
			if before.IsZero() || value.last.Before(before) {
				oldest, before = candidate, value.last
			}
		}
		a.cacheBytes -= len(a.cache[oldest].data)
		delete(a.cache, oldest)
	}
	a.cache[key] = work.entry
	a.cacheBytes += len(data)
}

func (a *Audio) download(ctx context.Context, entry playToken) ([]byte, string, error) {
	get, err := http.NewRequestWithContext(ctx, http.MethodGet, entry.target, nil)
	if err != nil {
		return nil, "", err
	}
	get.Header.Set("Referer", "https://www.qishui.com/")
	get.Header.Set("User-Agent", webAgent)
	res, err := a.client.Do(get)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || res.ContentLength > maxAudio {
		return nil, "", errors.New("qishui source request failed")
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxAudio+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxAudio {
		return nil, "", errors.New("qishui audio exceeds size limit")
	}
	return decryptAudio(data, entry.auth)
}
