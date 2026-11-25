package kugou

import (
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
)

func TestMemberPlaybackAuthChainAndAnonymousFallback(t *testing.T) {
	for _, alternate := range []bool{false, true} {
		name := "authorized"
		if alternate {
			name = "credential-free fallback"
		}
		t.Run(name, func(t *testing.T) {
			var paths []string
			client := New(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				paths = append(paths, r.URL.Path)
				q := r.URL.Query()
				body := ""
				if q.Get("appid") == "1005" {
					if !alternate || q.Has("token") || q.Has("userid") || r.Header.Get("Cookie") != "" || q.Get("dfid") != "fixture-dfid" || q.Get("mid") != "fixture-mid" {
						t.Fatal("alternate profile leaked credentials or lost device identity")
					}
					body = `{"url":["https://audio.example/fallback.mp3"],"bitRate":320000}`
				} else {
					if q.Get("token") != "fixture-token" || q.Get("userid") != "42" || q.Get("appid") != "3116" || q.Get("signature") == "" {
						t.Fatal("missing signed member identity")
					}
					switch r.URL.Path {
					case "/v5/url":
						if q.Get("IsFreePart") != "0" || q.Get("album_audio_id") != "123" || q.Get("clientver") != "11430" {
							t.Fatal("full playback parameters differ from Node")
						}
						body = `{"status":3,"url":[]}`
					case "/v1/user_verify":
						body = `{"status":1,"data":{"auth":"fixture-account-auth"}}`
					case "/v1/authorization":
						if q.Get("authorization") != "fixture-account-auth" {
							t.Fatal("missing playback grant")
						}
						body = `{"status":1,"data":{"auth":"fixture-track-auth","open_time":1700000000}}`
						if alternate {
							body = `{"status":0}`
						}
					case "/tracker/v5/url":
						if q.Get("auth") != "fixture-track-auth" || q.Get("clientver") != "11561" || q.Has("IsFreePart") {
							t.Fatal("wrong authorized track request")
						}
						body = `{"url":["https://audio.example/auth.flac"],"extName":"flac","timeLength":240}`
					default:
						t.Fatal("unexpected playback path")
					}
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
			})})
			got, err := client.Lookup(t.Context(), music.Request{Platform: "kugou", Type: "url", ID: "0123456789abcdef0123456789abcdef,123", Quality: "lossless", Cookie: "token=fixture-token; userid=42; dfid=fixture-dfid; KUGOU_API_MID=fixture-mid"})
			last, address, quality := "/tracker/v5/url", "https://audio.example/auth.flac", "无损"
			if alternate {
				last, address, quality = "/v5/url", "https://audio.example/fallback.mp3", "极高"
			}
			if err != nil || got.Link.URL != address || got.Link.Quality != quality || !slices.Equal(paths, []string{"/v5/url", "/v1/user_verify", "/v1/authorization", last}) {
				t.Fatalf("playback chain: %v %+v %v", paths, got.Link, err)
			}
		})
	}
}
