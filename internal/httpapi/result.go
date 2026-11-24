package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
)

func (s *Server) writeResult(w http.ResponseWriter, r *http.Request, req music.Request, result music.Result) {
	switch req.Type {
	case "url":
		if result.Link.URL == "" {
			fail(w, 403, "MUSIC_URL_UNAVAILABLE", "未获得可用播放地址")
			return
		}
		if req.Redirect {
			http.Redirect(w, r, result.Link.URL, http.StatusFound)
			return
		}
		jsonResponse(w, 200, result.Link)
	case "pic":
		http.Redirect(w, r, result.Image, http.StatusFound)
	case "lrc":
		if req.Format == "json" {
			l := result.Lyrics
			jsonResponse(w, 200, map[string]any{
				"version": 1, "lyric": l.Lyric, "translation": l.Translation, "romanization": l.Romanized,
				"wordLyric": l.WordLyric, "wordFormat": l.WordFormat, "source": valueOr(l.Source, "lrc"),
			})
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(mergeLRC(result.Lyrics.Lyric, result.Lyrics.Translation)))
	default:
		base := s.apiBase(r)
		list := append([]music.Item{}, result.Items...)
		for i, item := range list {
			list[i].URL = makeLink(base, item.URL, req, "url")
			list[i].Pic = makeLink(base, item.Pic, req, "pic")
			list[i].Lyric = makeLink(base, item.Lyric, req, "lrc")
		}
		jsonResponse(w, 200, list)
	}
}

func writeRoom(w http.ResponseWriter, kind string, result music.Result) {
	switch kind {
	case "url":
		if result.Link.URL == "" {
			fail(w, 403, "MUSIC_URL_UNAVAILABLE", "未获得可用播放地址")
			return
		}
		jsonResponse(w, 200, result.Link)
	case "pic":
		jsonResponse(w, 200, result.Image)
	case "lrc":
		jsonResponse(w, 200, map[string]string{"lyric": result.Lyrics.Lyric, "tlyric": result.Lyrics.Translation})
	default:
		jsonResponse(w, 200, nonNilItems(result.Items))
	}
}

func nonNilItems(items []music.Item) []music.Item {
	if items == nil {
		return []music.Item{}
	}
	return items
}

func (s *Server) apiBase(r *http.Request) string {
	if s.PublicBase != nil {
		return strings.TrimRight(s.PublicBase.String(), "/") + "/api"
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return (&url.URL{Scheme: scheme, Host: r.Host, Path: "/api"}).String()
}

func makeLink(base string, field json.RawMessage, req music.Request, kind string) json.RawMessage {
	if len(field) == 0 || string(field) == "null" {
		return field
	}
	var id string
	if err := json.Unmarshal(field, &id); err != nil {
		id = string(field)
	}
	if id == "" || strings.HasPrefix(id, "@") || strings.HasPrefix(id, "http:") || strings.HasPrefix(id, "https:") {
		return field
	}
	op := kind
	q := url.Values{"server": {req.Platform}, "type": {op}, "id": {id}}
	if kind == "url" {
		switch req.Type {
		case "search_playlist":
			op = "playlist"
		case "dj_hot", "dj_detail", "search_dj":
			op = "dj"
		}
		q.Set("type", op)
		q.Set("redirect", "1")
		if req.Quality != "" {
			q.Set("quality", req.Quality)
		}
		if req.Type == "fm" {
			q.Set("fm", "1")
		}
	}
	data, _ := json.Marshal(base + "?" + q.Encode())
	return data
}

var lrcLine = regexp.MustCompile(`^\[(\d{2}):(\d{2})\.(\d*)\](.*)$`)

type timedLine struct {
	ms   int
	text string
}

func parseLRC(text string) []timedLine {
	var lines []timedLine
	for line := range strings.SplitSeq(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		m := lrcLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		minutes, _ := strconv.Atoi(m[1])
		seconds, _ := strconv.Atoi(m[2])
		ms, _ := strconv.Atoi((m[3] + "000")[:3])
		lines = append(lines, timedLine{ms: minutes*60000 + seconds*1000 + ms, text: m[4]})
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].ms < lines[j].ms })
	return lines
}

func mergeLRC(lyrics, translation string) string {
	translations := parseLRC(translation)
	if len(translations) == 0 {
		return lyrics
	}
	var output []string
	j := 0
	for _, line := range parseLRC(lyrics) {
		for j+1 < len(translations) && translations[j].ms < line.ms {
			j++
		}
		if translations[j].ms == line.ms && translations[j].text != "" {
			line.text += " (" + translations[j].text + ")"
		}
		output = append(output, fmt.Sprintf("[%02d:%02d.%03d]%s", line.ms/60000, line.ms/1000%60, line.ms%1000, line.text))
	}
	return strings.Join(output, "\n")
}
