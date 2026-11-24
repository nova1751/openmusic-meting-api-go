package netease

import (
	"crypto/aes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func reply(r *http.Request, status int, payload string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(payload)), Request: r}
}

func TestEnhancedFormMatchesOriginal(t *testing.T) {
	// Generated with crypto.eapi in the pinned JavaScript repository.
	const expected = "2B5D64177AA6460FBAA3DCB1285E28954BBB4F7556E09B0FB25750F12398BB50A8D4C3BD4D840A55F632B959EE063421BEE316873AA94B1A16E26ABB4C447A029DF9B83A056A44B126204239570C52CDF2D45F29F6640DB4E4E30F357D52E112B6B1839C5B0BF5FF6F8711B5258C0C65"
	if got := enhancedForm("/api/cloudsearch/pc", []byte(`{"s":"测试","type":1}`)).Get("params"); got != expected {
		t.Fatal(got)
	}
}

func decodeEnhanced(t *testing.T, req *http.Request) map[string]json.RawMessage {
	t.Helper()
	if err := req.ParseForm(); err != nil {
		t.Fatal(err)
	}
	buf, err := hex.DecodeString(req.PostForm.Get("params"))
	if err != nil || len(buf) == 0 || len(buf)%aes.BlockSize != 0 {
		t.Fatalf("invalid encrypted payload: %v", err)
	}
	block, _ := aes.NewCipher([]byte("e82ckenh8dichen8"))
	for i := 0; i < len(buf); i += aes.BlockSize {
		block.Decrypt(buf[i:i+aes.BlockSize], buf[i:i+aes.BlockSize])
	}
	parts := strings.Split(string(buf), "-36cd479b6b5-")
	if len(parts) != 3 {
		t.Fatal("upstream envelope markers lost")
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal([]byte(parts[1]), &data); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSearchPaginationAndLargeIDs(t *testing.T) {
	client := New(&http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != interfaceOrigin+"/eapi/cloudsearch" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		data := decodeEnhanced(t, r)
		if string(data["limit"]) != "10" || string(data["offset"]) != "20" || string(data["s"]) != `"爱与海"` {
			t.Errorf("search request: %s", data)
		}
		return reply(r, 200, `{"code":"200","result":{"songs":[{"id":19007199254740992,"name":"title","ar":[{"name":"artist"}],"al":{"name":"album"},"dt":209501}]}}`), nil
	})}, 0)
	result, err := client.Lookup(t.Context(), music.Request{Type: "search", ID: "爱与海", Page: music.Pagination{Explicit: true, Limit: 10, Offset: 20}})
	if err != nil || len(result.Items) != 1 {
		t.Fatalf("result %+v error %v", result, err)
	}
	item := result.Items[0]
	if string(item.URL) != "19007199254740992" || item.Duration != 209501 || item.Author != "artist" {
		t.Fatalf("mapped %+v", item)
	}
}

func TestPlaylistBatchesIDs(t *testing.T) {
	var entries []string
	for i := 1; i <= 201; i++ {
		entries = append(entries, fmt.Sprintf(`{"id":%d}`, i))
	}
	batches := 0
	client := New(&http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/api/v6/playlist/detail":
			return reply(r, 200, `{"code":200,"playlist":{"trackIds":[`+strings.Join(entries, ",")+`]}}`), nil
		case "/weapi/v3/song/detail":
			batches++
			if err := r.ParseForm(); err != nil || len(r.PostForm.Get("encSecKey")) != 256 || r.PostForm.Get("params") == "" {
				t.Errorf("invalid WEAPI form")
			}
			return reply(r, 200, fmt.Sprintf(`{"songs":[{"id":%d,"name":"song"}]}`, batches)), nil
		default:
			return nil, fmt.Errorf("unexpected path %q", r.URL.Path)
		}
	})}, 0)
	result, err := client.Lookup(t.Context(), music.Request{Type: "playlist", ID: "901"})
	if err != nil || batches != 2 || len(result.Items) != 2 {
		t.Fatalf("batches %d result %+v error %v", batches, result, err)
	}
}

func TestActualPlaybackQualityAndDuration(t *testing.T) {
	client := New(&http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		data := decodeEnhanced(t, r)
		if string(data["level"]) != `"jymaster"` || string(data["ids"]) != `"[123]"` {
			t.Errorf("playback request: %s", data)
		}
		return reply(r, 200, `{"code":200,"data":[{"url":"http://media.example/track.flac","level":"lossless","time":210123,"gain":-8.25,"peak":0.91}]}`), nil
	})}, 0)
	result, err := client.Lookup(t.Context(), music.Request{Type: "url", ID: "123", Quality: "jymaster", Cookie: "MUSIC_U=fixture"})
	if err != nil || result.Link.Quality != "无损" || result.Link.Duration != 210123 ||
		result.Link.URL != "https://media.example/track.flac" || result.Link.Loudness["gain"] != -8.25 {
		t.Fatalf("result %+v error %v", result, err)
	}
}

func TestLyricsFallbackAndInvalidID(t *testing.T) {
	client := New(&http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Host {
		case "api.amll.dev":
			if r.Header.Get("Cookie") != "" {
				t.Error("music cookie sent to the lyric fallback")
			}
			return reply(r, 200, `{"status":200,"data":{"format":"ttml","lyrics":"<tt/>"}}`), nil
		case "music.163.com":
			if r.URL.Path == "/api/song/lyric" {
				return reply(r, 200, `{"lrc":{"lyric":"[00:01.000]line"}}`), nil
			}
			return reply(r, 503, "{}"), nil
		}
		return nil, errors.New("unexpected origin")
	})}, 0)
	result, err := client.Lookup(t.Context(), music.Request{Type: "lrc", Format: "json", ID: "123", Cookie: "MUSIC_U=fixture"})
	if err != nil || result.Lyrics.Lyric != "[00:01.000]line" || result.Lyrics.WordFormat != "ttml" || result.Lyrics.Source != "amll" {
		t.Fatalf("result %+v error %v", result, err)
	}
	_, err = client.Lookup(t.Context(), music.Request{Type: "song", ID: "1,2,{}"})
	if !errors.Is(err, music.ErrInvalidID) {
		t.Fatal(err)
	}
}
