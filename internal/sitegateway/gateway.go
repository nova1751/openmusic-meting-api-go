// Package sitegateway restricts an anonymous website to configured collections.
// It is intended to sit behind an independently rate-limited reverse proxy.
package sitegateway

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Collection struct {
	Server string `json:"server"`
	Type   string `json:"type"`
	ID     string `json:"id"`
}

type Config struct {
	PublicBase  string       `json:"publicBase"`
	Origins     []string     `json:"origins"`
	Collections []Collection `json:"collections"`
}

type collectionData struct {
	items     []map[string]any
	permitted map[string]bool
	expires   time.Time
}

type flight struct {
	done chan struct{}
	data collectionData
	err  error
}

type Gateway struct {
	upstream    http.Handler
	token       string
	secret      [32]byte
	base        *url.URL
	origins     map[string]bool
	collections []Collection
	mu          sync.Mutex
	cache       map[string]collectionData
	flights     map[string]*flight
}

func New(upstream http.Handler, token, secret string, cfg Config) (*Gateway, error) {
	base, err := url.Parse(cfg.PublicBase)
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil ||
		base.RawQuery != "" || base.Fragment != "" || (base.Path != "" && base.Path != "/") {
		return nil, errors.New("invalid site gateway public base")
	}
	if upstream == nil || len(token) < 32 || len(secret) < 32 ||
		len(cfg.Origins) == 0 || len(cfg.Collections) == 0 || len(cfg.Collections) > 32 {
		return nil, errors.New("incomplete site gateway configuration")
	}
	g := &Gateway{upstream: upstream, token: token, secret: sha256.Sum256([]byte(secret)), base: base,
		origins: make(map[string]bool), collections: cfg.Collections, cache: make(map[string]collectionData),
		flights: make(map[string]*flight)}
	for _, origin := range cfg.Origins {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil ||
			u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("invalid site gateway origin")
		}
		g.origins[origin] = true
	}
	seen := make(map[string]bool)
	for _, c := range cfg.Collections {
		key := collectionKey(c)
		if (c.Server != "netease" && c.Server != "tencent") ||
			(c.Type != "playlist" && !(c.Server == "netease" && c.Type == "album")) ||
			c.ID == "" || len(c.ID) > 256 || seen[key] {
			return nil, errors.New("invalid site gateway collection")
		}
		seen[key] = true
	}
	return g, nil
}

func collectionKey(c Collection) string { return c.Server + ":" + c.Type + ":" + c.ID }

type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
	err    error
}

func (w *recorder) Header() http.Header { return w.header }
func (w *recorder) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *recorder) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.body.Len()+len(data) > 4<<20 {
		w.err = errors.New("site gateway response exceeds limit")
		return 0, w.err
	}
	return w.body.Write(data)
}

func (g *Gateway) internal(ctx context.Context, target string) (*recorder, error) {
	var nonce [18]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	claims, err := json.Marshal(map[string]any{"v": 1, "sub": "openmusic:system", "purpose": "system",
		"ts": time.Now().Unix(), "nonce": base64.RawURLEncoding.EncodeToString(nonce[:])})
	if err != nil {
		return nil, err
	}
	assertion := base64.RawURLEncoding.EncodeToString(claims)
	bodyHash := sha256.Sum256(nil)
	mac := hmac.New(sha256.New, []byte(g.token))
	_, _ = mac.Write([]byte(strings.Join([]string{assertion, "GET", target, hex.EncodeToString(bodyHash[:])}, "\n")))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1"+target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("X-OpenMusic-Assertion", assertion)
	req.Header.Set("X-OpenMusic-Signature", base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
	rec := &recorder{header: make(http.Header)}
	g.upstream.ServeHTTP(rec, req)
	if rec.err != nil {
		return nil, rec.err
	}
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	return rec, nil
}

func (g *Gateway) apiURL(server, kind, id string) string {
	u := *g.base
	u.Path = "/api"
	u.RawQuery = url.Values{"server": {server}, "type": {kind}, "id": {id}}.Encode()
	return u.String()
}

func (g *Gateway) load(ctx context.Context, c Collection) (collectionData, error) {
	key := collectionKey(c)
	g.mu.Lock()
	if cached, ok := g.cache[key]; ok && cached.expires.After(time.Now()) {
		g.mu.Unlock()
		return cached, nil
	}
	if pending := g.flights[key]; pending != nil {
		g.mu.Unlock()
		select {
		case <-pending.done:
			return pending.data, pending.err
		case <-ctx.Done():
			return collectionData{}, ctx.Err()
		}
	}
	pending := &flight{done: make(chan struct{})}
	g.flights[key] = pending
	g.mu.Unlock()
	pending.data, pending.err = g.loadCollection(ctx, c)
	g.mu.Lock()
	if pending.err == nil {
		g.cache[key] = pending.data
	}
	delete(g.flights, key)
	close(pending.done)
	g.mu.Unlock()
	return pending.data, pending.err
}

func (g *Gateway) loadCollection(ctx context.Context, c Collection) (collectionData, error) {
	q := url.Values{"server": {c.Server}, "type": {c.Type}, "id": {c.ID}}
	response, err := g.internal(ctx, "/api?"+q.Encode())
	if err != nil || response.status != http.StatusOK {
		return collectionData{}, errors.New("music collection unavailable")
	}
	decoder := json.NewDecoder(bytes.NewReader(response.body.Bytes()))
	decoder.UseNumber()
	var tracks []map[string]any
	if decoder.Decode(&tracks) != nil || tracks == nil || len(tracks) > 3000 {
		return collectionData{}, errors.New("invalid music collection")
	}
	allowed := make(map[string]bool)
	filtered := make([]map[string]any, 0, len(tracks))
	for _, track := range tracks {
		if track == nil {
			continue
		}
		for _, kind := range []string{"url", "pic", "lrc"} {
			value := ""
			switch typed := track[kind].(type) {
			case string:
				value = typed
			case json.Number:
				if _, err := strconv.ParseUint(string(typed), 10, 64); err == nil {
					value = g.apiURL(c.Server, kind, string(typed))
				}
			}
			if value == "" || strings.HasPrefix(value, "@") {
				track[kind] = ""
				continue
			}
			parsed, err := url.Parse(value)
			if err != nil {
				track[kind] = ""
				continue
			}
			parsed = g.base.ResolveReference(parsed)
			if parsed.Scheme == g.base.Scheme && parsed.Host == g.base.Host && parsed.Path == "/api" {
				q := parsed.Query()
				id := q.Get("id")
				if q.Get("server") != c.Server || q.Get("type") != kind || id == "" || len(id) > 256 {
					track[kind] = ""
					continue
				}
				allowed[kind+":"+id] = true
				track[kind] = g.apiURL(c.Server, kind, id)
			} else if parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil {
				track[kind] = parsed.String()
			} else {
				track[kind] = ""
			}
		}
		filtered = append(filtered, track)
	}
	result := collectionData{items: filtered, permitted: allowed, expires: time.Now().Add(15 * time.Minute)}
	return result, nil
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Path == "/health" && r.Method == http.MethodGet {
		_, _ = w.Write([]byte("ok"))
		return
	}
	secret := sha256.Sum256([]byte(r.Header.Get("X-Music-Origin-Key")))
	if subtle.ConstantTimeCompare(secret[:], g.secret[:]) != 1 {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	origin := r.Header.Get("Origin")
	if origin != "" {
		if !g.origins[origin] {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != "/api" || len(r.URL.RawQuery) > 1024 {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	for key, values := range q {
		if (key != "server" && key != "type" && key != "id") || len(values) != 1 {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}
	}
	server, kind, id := q.Get("server"), q.Get("type"), q.Get("id")
	if id == "" || len(id) > 256 {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if kind == "album" || kind == "playlist" {
		for _, collection := range g.collections {
			if collection.Server == server && collection.Type == kind && collection.ID == id {
				data, err := g.load(ctx, collection)
				if err != nil {
					http.Error(w, "Music provider unavailable", http.StatusBadGateway)
					return
				}
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				_ = json.NewEncoder(w).Encode(data.items)
				return
			}
		}
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if kind != "url" && kind != "pic" && kind != "lrc" {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	permitted, failed := false, false
	for _, collection := range g.collections {
		if collection.Server != server {
			continue
		}
		data, err := g.load(ctx, collection)
		if err != nil {
			failed = true
			continue
		}
		if data.permitted[kind+":"+id] {
			permitted = true
			break
		}
	}
	if !permitted {
		if failed {
			http.Error(w, "Music provider unavailable", http.StatusBadGateway)
		} else {
			http.Error(w, "Forbidden", http.StatusForbidden)
		}
		return
	}
	query := url.Values{"server": {server}, "type": {kind}, "id": {id}}
	if kind == "url" {
		query.Set("redirect", "1")
	}
	response, err := g.internal(ctx, "/api?"+query.Encode())
	if err != nil {
		http.Error(w, "Music provider unavailable", http.StatusBadGateway)
		return
	}
	if location := response.header.Get("Location"); location != "" {
		u, err := url.Parse(location)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			http.Error(w, "Invalid media address", http.StatusBadGateway)
			return
		}
		w.Header().Set("Location", u.String())
	}
	if contentType := response.header.Get("Content-Type"); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(response.status)
	_, _ = fmt.Fprint(w, response.body.String())
}
