package qishui

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
	"github.com/nova1751/openmusic-meting-api-go/internal/providerhttp"
)

type object = providerhttp.Object

func firstObject(items ...object) object {
	for _, item := range items {
		if item != nil {
			return item
		}
	}
	return nil
}

func httpURL(s string) string {
	u, err := url.Parse(s)
	if err == nil && u.Hostname() != "" && u.User == nil && (u.Scheme == "http" || u.Scheme == "https") {
		return u.String()
	}
	return ""
}

func cover(value any) string {
	return coverAt(value, 0)
}

func coverAt(value any, depth int) string {
	if value == nil || depth > 6 {
		return ""
	}
	switch item := value.(type) {
	case string:
		return httpURL(item)
	case []any:
		for _, candidate := range item[:min(len(item), 40)] {
			if addr := coverAt(candidate, depth+1); addr != "" {
				return addr
			}
		}
	default:
		o := providerhttp.AsObject(value)
		if o == nil {
			return ""
		}
		base := coverAt(o.Get("url_list", "urlList", "urls"), depth+1)
		uri := o.Text("uri", "image_uri", "imageUri")
		if base != "" && uri != "" {
			suffix := ""
			if prefix := o.Text("template_prefix", "templatePrefix"); prefix != "" {
				suffix = "~" + prefix + "-crop-center:192:192.jpg"
			}
			return httpURL(strings.TrimRight(base, "/") + "/" + strings.TrimLeft(uri, "/") + suffix)
		}
		return providerhttp.First(base, coverAt(o.Get("uri", "url"), depth+1))
	}
	return ""
}

func walk(value any, depth, limit int, visit func(object)) {
	budget := 12000
	var traverse func(any, int)
	traverse = func(value any, level int) {
		if budget <= 0 || level > depth {
			return
		}
		budget--
		switch v := value.(type) {
		case object:
			visit(v)
			keys := slices.Sorted(maps.Keys(v))
			for _, k := range keys[:min(limit, len(keys))] {
				traverse(v[k], level+1)
			}
		case map[string]any:
			traverse(object(v), level)
		case []any:
			for _, entry := range v[:min(limit, len(v))] {
				traverse(entry, level+1)
			}
		}
	}
	traverse(value, 0)
}

var mediaFields = []string{"media_resources", "media_list", "related_media", "medias", "media", "tracks", "track_list", "songs",
	"items", "list", "result", "song_list", "recommend_media_list"}

func collectMedia(root object) []any {
	if data := root.Object("data"); len(data.List(mediaFields...)) > 0 {
		root = data
	}
	if list := root.List(mediaFields...); len(list) > 0 {
		return list
	}
	var best []any
	walk(root, 5, 100, func(o object) {
		for _, val := range o {
			list, _ := val.([]any)
			if len(list) <= len(best) {
				continue
			}
			valid := make([]any, 0, len(list))
			for _, row := range list {
				entry := providerhttp.AsObject(row)
				if entry.Get("media", "track_entity", "entity", "base_info", "item_id", "id") != nil {
					valid = append(valid, row)
				}
			}
			if len(valid) > len(best) {
				best = valid
			}
		}
	})
	return best
}

func artists(values ...any) string {
	var names []string
	seen := map[string]bool{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	var visit func(any, int)
	visit = func(value any, depth int) {
		if value == nil || depth > 4 {
			return
		}
		switch val := value.(type) {
		case string:
			add(val)
		case []any:
			for _, item := range val[:min(20, len(val))] {
				visit(item, depth+1)
			}
		default:
			o := providerhttp.AsObject(val)
			add(o.Text("name", "artist_name", "author_name", "nickname"))
			for _, field := range []string{"artist", "artists", "artist_info", "artist_links", "author", "authors", "author_info",
				"singer", "singers", "singer_info", "entity", "link"} {
				visit(o[field], depth+1)
			}
		}
	}
	for _, value := range values {
		visit(value, 0)
	}
	return strings.Join(names, " / ")
}

func mapMedia(raw object) (music.Item, bool) {
	entity := firstObject(raw.Object("entity", "data"), raw)
	media := firstObject(entity.Object("media"), raw.Object("media"), entity)
	wrapper := firstObject(entity.Object("track_wrapper"), media.Object("track_wrapper"), raw.Object("track_wrapper"))
	track := firstObject(wrapper.Object("track"), media.Object("track_entity"), raw.Object("track_entity"), media.Object("track"), raw.Object("track"), media)
	base := firstObject(track.Object("base_info"), media.Object("base_info"), raw.Object("base_info"), track)
	display := firstObject(track.Object("display_info"), media.Object("display_info"), raw.Object("display_info"))
	related := firstObject(track.Object("related_info"), media.Object("related_info"), raw.Object("related_info"))
	album := firstObject(raw.Object("album_info", "album"), related.Object("album_link"), base.Object("album"), track.Object("album"))
	id := providerhttp.First(base.Text("id"), track.Text("id"), media.Text("id"), raw.Text("item_id", "song_id", "music_id"))
	name := providerhttp.First(base.Text("name", "title"), track.Text("name", "title"), media.Text("name"), raw.Text("title", "name"))
	if id == "" || name == "" {
		return music.Item{}, false
	}
	var authorFields []any
	for _, o := range []object{raw, related, base, track} {
		for _, k := range []string{"author_info", "author", "authors", "artist", "artists", "artist_links", "singer", "singers", "author_name", "artist_name"} {
			if o[k] != nil {
				authorFields = append(authorFields, o[k])
			}
		}
	}
	author := providerhttp.First(artists(authorFields...), "未知歌手")
	a := providerhttp.First(album.Text("name", "title"), raw.Text("album_name"), base.Text("album_name"))
	var picture string
	for _, o := range []object{display, base, track, raw, album} {
		if picture = cover(o.Get("cover_url", "url_cover", "cover")); picture != "" {
			break
		}
	}
	duration := providerhttp.Number(providerhttp.First(base.Text("duration_ms", "duration"), track.Text("duration_ms", "duration"), raw.Text("duration_ms", "duration")))
	if duration > 10000 {
		duration /= 1000
	}
	return music.Item{ID: id, Name: name, Artist: author, Source: "qishui", Title: name, Author: author, Album: &a,
		URL: providerhttp.RawString(id), Lyric: providerhttp.RawString(id), Pic: providerhttp.RawString(picture),
		Duration: int64(math.Round(duration))}, true
}

func tracks(raw []any) []music.Item {
	found := make([]music.Item, 0, len(raw))
	for _, row := range raw {
		if item, ok := mapMedia(providerhttp.AsObject(row)); ok {
			found = append(found, item)
		}
	}
	return found
}

func (c *Client) search(ctx context.Context, req music.Request) (music.Result, error) {
	if fingerprint(req.Cookie) {
		res, err := c.request(ctx, upstreamRequest{endpoint: pcHost + "/luna/pc/search/track", cookie: req.Cookie,
			query: c.params(false, url.Values{"q": {req.ID}, "cursor": {"0"}, "count": {"30"}, "search_method": {"input"}})})
		if err == nil {
			return music.Result{Items: tracks(collectMedia(res))}, nil
		}
	}
	res, err := c.request(ctx, upstreamRequest{endpoint: webHost + "/v2/search/type",
		query: url.Values{"keyword": {req.ID}, "search_type": {"music"}, "limit": {"30"}, "real_offset": {"0"}, "search_source": {"qishui"}}})
	found := tracks(res.Object("data").List("list"))
	return music.Result{Items: found[:min(30, len(found))]}, err
}

func (c *Client) searchPlaylists(ctx context.Context, req music.Request) (music.Result, error) {
	items := []music.Item{}
	if strings.TrimSpace(req.ID) == "" {
		return music.Result{Items: items}, nil
	}
	q := c.params(false, url.Values{"q": {strings.TrimSpace(req.ID)}, "cursor": {"0"}, "search_method": {"input"}})
	seen := map[string]bool{}
	var lastErr error
	for _, endpoint := range []string{"/luna/pc/search/playlist", "/luna/pc/search/mixed"} {
		res, err := c.request(ctx, upstreamRequest{endpoint: pcHost + endpoint, cookie: req.Cookie, query: q})
		lastErr = err
		if err != nil {
			continue
		}
		walk(res, 8, 200, func(o object) {
			p := firstObject(o.Object("playlist"), o.Object("entity").Object("playlist"))
			id := p.Text("id", "playlist_id")
			if id == "" || seen[id] {
				return
			}
			seen[id] = true
			name := providerhttp.First(p.Text("name", "title"), "未命名歌单")
			creator := providerhttp.First(p.Text("creator_name"), p.Object("creator").Text("nickname", "name"))
			picture := cover(p.Get("cover_url", "cover", "url_cover", "coverURL"))
			count, play := int(p.Int("track_count", "count_tracks", "song_count")), p.Int("play_count", "playcount")
			items = append(items, music.Item{ID: id, Name: name, Title: name, Creator: creator, Author: creator, Cover: picture,
				Pic: providerhttp.RawString(picture), URL: providerhttp.RawString(id), TrackCount: &count, PlayCount: &play})
		})
		if len(items) > 0 {
			break
		}
	}
	return music.Result{Items: items}, lastErr
}

func (c *Client) playlist(ctx context.Context, req music.Request) (music.Result, error) {
	if err := idParam(req.ID); err != nil {
		return music.Result{}, err
	}
	if !fingerprint(req.Cookie) {
		return music.Result{Items: []music.Item{}}, nil
	}
	res, err := c.request(ctx, upstreamRequest{endpoint: pcHost + "/luna/pc/playlist/detail", cookie: req.Cookie,
		agent: "LunaPC/3.6.0(424921879)", query: c.params(false, url.Values{"version_name": {"3.6.0"}, "version_code": {"30060000"},
			"playlist_id": {req.ID}, "cursor": {""}, "count": {"100"}})})
	return music.Result{Items: tracks(collectMedia(res))}, err
}

func (c *Client) detail(ctx context.Context, id, cookie string) (object, error) {
	if err := idParam(id); err != nil {
		return nil, err
	}
	res, err := c.request(ctx, upstreamRequest{endpoint: mobileHost + "/luna/h5/seo_track", cookie: cookie, agent: mobileAgent,
		query: c.params(true, url.Values{"track_id": {id}, "device_platform": {"web"}})})
	if err == nil {
		if track := res.Object("seo_track").Object("track"); track != nil {
			return track, nil
		}
	}
	res, err = c.request(ctx, upstreamRequest{endpoint: webHost + "/v2/custom/contents",
		query: url.Values{"item_ids": {id}, "sources": {"qishui"}, "need_author": {"true"}, "need_album": {"true"}, "need_ugc": {"true"}, "need_stat": {"true"}}})
	if items := res.Object("data").List("list"); len(items) > 0 {
		return providerhttp.AsObject(items[0]), err
	}
	return nil, err
}

func (c *Client) song(ctx context.Context, req music.Request) (music.Result, error) {
	res, err := c.detail(ctx, req.ID, req.Cookie)
	if err != nil {
		return music.Result{}, err
	}
	item, ok := mapMedia(res)
	if req.Type == "pic" {
		var image string
		if !ok || json.Unmarshal(item.Pic, &image) != nil || image == "" {
			return music.Result{}, music.ErrNoURL
		}
		return music.Result{Image: image}, nil
	}
	if !ok {
		return music.Result{Items: []music.Item{}}, nil
	}
	return music.Result{Items: []music.Item{item}}, nil
}

var lyricPrefix = regexp.MustCompile(`^\[(\d+),(\d+)\](.*)$`)
var lyricWord = regexp.MustCompile(`<\d+,\d+,\d+>`)

func (c *Client) lyrics(ctx context.Context, req music.Request) (music.Result, error) {
	res, err := c.detail(ctx, req.ID, req.Cookie)
	if err != nil {
		return music.Result{}, err
	}
	lyric := music.Lyrics{Source: "lrc"}
	walk(res, 7, 120, func(o object) {
		for _, key := range slices.Sorted(maps.Keys(o)) {
			lower := strings.ToLower(key)
			if !strings.Contains(lower, "lyric") && !strings.Contains(lower, "translation") {
				continue
			}
			value := o[key]
			for i := 0; i < 6; i++ {
				child := providerhttp.AsObject(value)
				if child == nil {
					break
				}
				value = child.Get("content", "lyric_text", "lyric", "text", "entity")
			}
			text := providerhttp.Text(value)
			if !strings.Contains(text, "[") {
				continue
			}
			text = normalizeLyric(text)
			if strings.Contains(lower, "trans") || strings.Contains(lower, "tlyric") {
				lyric.Translation = providerhttp.First(lyric.Translation, text)
			} else {
				lyric.Lyric = providerhttp.First(lyric.Lyric, text)
			}
		}
	})
	return music.Result{Lyrics: lyric}, nil
}

func normalizeLyric(raw string) string {
	lines := strings.Split(strings.ReplaceAll(raw, `\n`, "\n"), "\n")
	for i, line := range lines {
		match := lyricPrefix.FindStringSubmatch(strings.TrimSpace(line))
		if len(match) == 0 {
			continue
		}
		ms, _ := strconv.ParseInt(match[1], 10, 64)
		lines[i] = fmt.Sprintf("[%02d:%02d.%03d]%s", ms/60000, ms/1000%60, ms%1000, lyricWord.ReplaceAllString(match[3], ""))
	}
	return strings.Join(lines, "\n")
}
