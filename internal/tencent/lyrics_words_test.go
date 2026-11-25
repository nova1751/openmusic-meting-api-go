package tencent

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestStructuredLyricsDecodesQRC(t *testing.T) {
	const cipher = "bb1db48dc814ca5278a2b50c54638a6d528e00fa0c3a4ae612fc23c7755107bd504d97c24b81679c"
	c := New(&http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		payload := `{"lyric":"WzAwOjAwXWFhYQ=="}`
		if r.Method == http.MethodPost {
			var request struct {
				Request struct {
					Method string
					Param  map[string]any
				}
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			switch request.Request.Method {
			case "get_song_detail_yqq":
				payload = `{"code":0,"request":{"code":0,"data":{"track_info":{"id":123,"name":"测试","interval":10}}}}`
			case "GetPlayLyricInfo":
				if request.Request.Param["songID"] != float64(123) {
					t.Fatal("lookup sent the wrong numeric ID")
				}
				payload = `{"code":0,"request":{"code":0,"data":{"lyric":"` + cipher + `"}}}`
			default:
				t.Fatal("unexpected QQ endpoint")
			}
		} else if r.URL.Host != "c.y.qq.com" {
			t.Fatalf("unexpected fallback %s", r.URL.Host)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(payload))}, nil
	})})
	got, err := c.Lookup(t.Context(), music.Request{Type: "lrc", ID: "MockMid", Format: "json"})
	if err != nil || got.Lyrics.WordLyric != "[0,1000]字(0,300)词(300,700)" || got.Lyrics.WordFormat != "qrc" || got.Lyrics.Lyric != "[00:00]aaa" {
		t.Fatalf("unexpected lyrics: %+v %v", got.Lyrics, err)
	}
}

func TestChallengeRestrictsOrigin(t *testing.T) {
	c := New(http.DefaultClient)
	const cookie = "qqmusic_key=synthetic"
	for _, target := range []string{"http://c.y.qq.com/check", "https://c.y.qq.com.example/check", "https://c.y.qq.com:444/check", "https://user@c.y.qq.com/check"} {
		if c.rememberChallenge(cookie, "MID", target) {
			t.Fatal("accepted unofficial challenge origin")
		}
	}
	if !c.rememberChallenge(cookie, "MID", "https://c.y.qq.com/check") {
		t.Fatal("rejected a QQ challenge")
	}
	if _, ok := c.Challenge(cookie); !ok {
		t.Fatal("challenge was not saved")
	}
	if _, ok := c.Challenge("qqmusic_key=other"); ok {
		t.Fatal("challenge was shared across accounts")
	}
}
