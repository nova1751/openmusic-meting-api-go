package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/auth"
	"github.com/nova1751/openmusic-meting-api-go/internal/legacy"
	"github.com/nova1751/openmusic-meting-api-go/internal/music"
)

const testToken = "any-test-token-at-least-32-characters"

type lookupFunc func(context.Context, music.Request) (music.Result, error)

func (f lookupFunc) Lookup(ctx context.Context, req music.Request) (music.Result, error) {
	return f(ctx, req)
}

func options(lookup lookupFunc) Options {
	base, _ := url.Parse("https://music.example/meting")
	return Options{Verifier: auth.New(testToken, false), Provider: lookup,
		Accounts: music.NewAccountPool(nil), PublicBase: base, Timeout: time.Second,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func request(t *testing.T, server http.Handler, method, target, body string, token bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if token {
		req.Header.Set("Authorization", "Bearer "+testToken)
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)
	return recorder
}

func TestNativeSearchAndLinkContract(t *testing.T) {
	app := New(options(func(_ context.Context, req music.Request) (music.Result, error) {
		if req.ID != "海 阔&天空" || req.Page.Offset != 15 || req.Page.Limit != 5 || !req.Page.Explicit {
			t.Fatalf("unexpected request: %+v", req)
		}
		return music.Result{Items: []music.Item{{
			Title: "海阔天空", URL: json.RawMessage(`123`),
			Pic: json.RawMessage(`"https://p.example/cover"`), Lyric: json.RawMessage(`123`),
		}}}, nil
	}))
	path := "/api?type=search&id=" + url.QueryEscape("海 阔&天空") + "&page=3&offset=15&limit=5&quality=flac"
	got := request(t, app, http.MethodGet, path, "", true)
	if got.Code != 200 || got.Header().Get("Cache-Control") != "no-store, private" {
		t.Fatalf("%d %v %s", got.Code, got.Header(), got.Body.String())
	}
	var items []struct{ URL, Pic, LRC string }
	if err := json.Unmarshal(got.Body.Bytes(), &items); err != nil || len(items) != 1 {
		t.Fatalf("items: %s", got.Body)
	}
	link, err := url.Parse(items[0].URL)
	if err != nil || link.Path != "/meting/api" || link.Host != "music.example" ||
		link.Query().Get("id") != "123" || link.Query().Get("redirect") != "1" || link.Query().Get("quality") != "flac" ||
		items[0].Pic != "https://p.example/cover" || !strings.Contains(items[0].LRC, "type=lrc") {
		t.Fatalf("links: %+v", items[0])
	}
}

func TestRejectionsPrecedeProvider(t *testing.T) {
	app := New(options(func(_ context.Context, req music.Request) (music.Result, error) {
		t.Fatal("provider reached")
		return music.Result{}, nil
	}))
	for _, tc := range []struct {
		method, path, body string
		token              bool
		status             int
		code               string
	}{
		{http.MethodGet, "/api?type=search&limit=0", "", false, 401, "OPENMUSIC_AUTH_REQUIRED"},
		{http.MethodGet, "/api?type=search&limit=0", "", true, 400, "分页参数"},
		{http.MethodGet, "/api?type=dj&limit=100&page=9007199254740991", "", true, 400, "分页参数"},
		{http.MethodGet, "/api?type=dj&limit=100&offset=9007199254740991", "", true, 400, "分页参数"},
		{http.MethodGet, "/api?type=url", "", true, 503, "MUSIC_ACCOUNT_UNAVAILABLE"},
		{http.MethodGet, "/api?server=tencent&type=search", "", true, 501, "PLATFORM_NOT_CONFIGURED"},
		{http.MethodGet, "/admin/openmusic?server=netease&type=song&id=1", "", true, 400, "MUSIC_COOKIE_REQUIRED"},
		{http.MethodPost, "/admin/fm", strings.Repeat(" ", (1<<20)+1), true, 413, "REQUEST_BODY_TOO_LARGE"},
		{http.MethodPost, "/admin/fm", strings.Repeat(" ", (1<<20)+1), false, 401, "OPENMUSIC_AUTH_REQUIRED"},
	} {
		t.Run(tc.method+tc.path+tc.code, func(t *testing.T) {
			got := request(t, app, tc.method, tc.path, tc.body, tc.token)
			if got.Code != tc.status || !strings.Contains(got.Body.String(), tc.code) ||
				got.Header().Get("Cache-Control") != "no-store, private" {
				t.Fatalf("%d %v %s", got.Code, got.Header(), got.Body)
			}
		})
	}
}

func TestCompatibilityKeepsQueryAndMediaRange(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/__meting_go/authorize":
			token := r.Header.Get(legacy.BridgeKeyHeader)
			uri, _ := base64.RawURLEncoding.DecodeString(r.Header.Get(legacy.BridgeTargetHeader))
			if token != testToken || string(uri) != "/api?type=search&id=a%2Fb+%E4%B8%AD" {
				t.Errorf("bridge lost credentials or raw URI: %s", uri)
			}
			_, _ = io.WriteString(w, `{"cookie":"MUSIC_U=private-fixture"}`)
		case "/audio/qishui":
			if r.URL.RawQuery != "token=local%2Ftest&opaque=a;b&mode=source" || r.Header.Get("Range") != "bytes=0-1" ||
				r.Header.Get(legacy.BridgeKeyHeader) != "" || r.Header.Get("X-Forwarded-Prefix") != "" ||
				r.Header.Get("X-Forwarded-Host") != "https://music.example/meting" {
				t.Error("proxy changed the query or mishandled headers")
			}
			w.Header().Set("Content-Range", "bytes 0-1/10")
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Cache-Control", "public")
			w.WriteHeader(206)
			_, _ = io.WriteString(w, "ab")
		default:
			t.Errorf("unexpected upstream path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer backend.Close()
	cfg := options(func(_ context.Context, req music.Request) (music.Result, error) {
		if req.Cookie != "MUSIC_U=private-fixture" || req.ID != "a/b 中" {
			t.Fatalf("unexpected native lookup: %+v", req)
		}
		return music.Result{}, nil
	})
	origin, _ := url.Parse(backend.URL)
	cfg.Legacy = legacy.New(origin, cfg.PublicBase, testToken, backend.Client())
	app := New(cfg)
	got := request(t, app, http.MethodGet, "/api?type=search&id=a%2Fb+%E4%B8%AD", "", true)
	if got.Code != 200 || strings.TrimSpace(got.Body.String()) != "[]" {
		t.Fatalf("%d %s", got.Code, got.Body)
	}
	hidden := request(t, app, http.MethodGet, "/%5f_meting_go/authorize", "", true)
	if hidden.Code != 404 {
		t.Fatal(hidden.Code)
	}

	media := httptest.NewRequest(http.MethodGet, "/audio/qishui?token=local%2Ftest&opaque=a;b&mode=source", nil)
	media.Header.Set("Range", "bytes=0-1")
	media.Header.Set(legacy.BridgeKeyHeader, "spoof")
	media.Header.Set("X-Forwarded-Prefix", "/spoof")
	got = httptest.NewRecorder()
	app.ServeHTTP(got, media)
	if got.Code != 206 || got.Header().Get("Content-Range") != "bytes 0-1/10" ||
		got.Header().Get("Cache-Control") != "no-store, private" ||
		len(got.Header().Values("Access-Control-Allow-Origin")) != 1 || got.Body.String() != "ab" {
		t.Fatalf("%d %v %s", got.Code, got.Header(), got.Body)
	}
}

func TestMergeLRC(t *testing.T) {
	got := mergeLRC("[00:02.05]来\n[00:01.001]归", "[00:02.050]come\n[00:01.005]later")
	if got != "[00:01.001]归\n[00:02.050]来 (come)" {
		t.Fatal(got)
	}
}
