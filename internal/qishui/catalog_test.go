package qishui

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
)

func TestSearchToleratesMissingCover(t *testing.T) {
	client := New(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api-vehicle.volcengine.com" || r.URL.Query().Get("keyword") != "测试" || r.Header.Get("Cookie") != "" {
			t.Fatal("invalid public search request")
		}
		body := `{"data":{"list":[{"track":{"id":"7890","name":"测试","author_name":"歌手"}},{"track":{"id":"7891","name":"测试2","cover_url":{"url_list":["https://images.example/cover"]}}}]}}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}, nil)
	result, err := client.Lookup(t.Context(), music.Request{Type: "search", ID: "测试"})
	if err != nil || len(result.Items) != 2 || result.Items[0].ID != "7890" || result.Items[0].Author != "歌手" ||
		!strings.Contains(string(result.Items[1].Pic), "images.example") {
		t.Fatalf("search: %+v %v", result, err)
	}
}
