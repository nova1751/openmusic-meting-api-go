package kugou

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
	"github.com/nova1751/openmusic-meting-api-go/internal/providerhttp"
)

var (
	hashPattern       = regexp.MustCompile(`^[a-f0-9]{32}$`)
	collectionPattern = regexp.MustCompile(`(?i)^collection_[a-z0-9_]+$`)
	gcidPattern       = regexp.MustCompile(`(?i)^(?:gcid_)?([0-9a-y]+z[0-9a-y]+z[0-9a-y]+z[0-9a-y]+[0-9a-f]{2})$`)
	shareCodePattern  = regexp.MustCompile(`^[0-9]{8,20}$`)
)

func parseID(id string) (string, uint64, error) {
	hash, album, hasAlbum := strings.Cut(strings.TrimSpace(id), ",")
	hash = strings.ToLower(hash)
	if !hashPattern.MatchString(hash) {
		return "", 0, music.ErrInvalidID
	}
	if !hasAlbum || album == "" {
		return hash, 0, nil
	}
	value, err := strconv.ParseUint(album, 10, 64)
	if err != nil {
		return "", 0, music.ErrInvalidID
	}
	return hash, value, nil
}

func image(value string) string {
	return strings.NewReplacer("{size}", "400", "{hash}", "", "http://", "https://").Replace(value)
}

func mapSong(song providerhttp.Object) music.Item {
	info := song.Object("info")
	trans := song.Object("trans_param", "transParam")
	hash := strings.ToLower(song.Text("hash", "Hash", "fileHash", "FileHash"))
	file := song.Text("FileName", "filename", "file_name")
	if _, title, ok := strings.Cut(file, " - "); ok {
		file = strings.TrimSpace(title)
	}
	audio := song.Text("audio_name", "audioName", "name")
	author, title, found := strings.Cut(audio, " - ")
	if !found {
		author, title = "", audio
	}
	var authors []string
	for _, name := range song.List("authors", "singerinfo") {
		if display := providerhttp.AsObject(name).Text("author_name", "authorName", "name"); display != "" {
			authors = append(authors, display)
		}
	}
	album := providerhttp.First(song.Text("album_name", "albumName", "AlbumName", "album", "albumname"), song.Object("albuminfo").Text("name"))
	duration := providerhttp.Number(providerhttp.First(song.Text("duration", "Duration", "timelength", "timelen", "time_length"), info.Text("duration")))
	if duration > 10000 {
		duration /= 1000
	}
	return music.Item{ID: hash, Source: "kugou", Title: providerhttp.First(song.Text("songname", "songName", "SongName"), file, strings.TrimSpace(title)),
		Author: providerhttp.First(song.Text("singername", "singerName", "SingerName", "author_name", "author"),
			strings.Join(authors, " / "), strings.TrimSpace(author)),
		Album: &album, Pic: providerhttp.RawString(image(providerhttp.First(song.Text("img", "image", "Image", "album_img", "albumImage", "cover"),
			trans.Text("union_cover"), info.Text("image")))), Duration: int64(math.Round(duration)),
		URL: providerhttp.RawString(hash), Lyric: providerhttp.RawString(hash)}
}

func songs(list []any) []music.Item {
	items := make([]music.Item, 0, len(list))
	for _, entry := range list {
		if song := mapSong(providerhttp.AsObject(entry)); song.ID != "" {
			items = append(items, song)
		}
	}
	return items
}

func songList(data providerhttp.Object) []any {
	if entries := data.List("data"); entries != nil {
		return entries
	}
	if entries := data.Object("data").List("info", "lists", "songs", "songlist", "song_list"); entries != nil {
		return entries
	}
	return data.List("info")
}

func (c *Client) search(ctx context.Context, req music.Request) (music.Result, error) {
	if req.Type == "search" {
		res, err := c.call(ctx, request{address: "https://songsearch.kugou.com/song_search_v2", cookie: req.Cookie,
			params: map[string]string{"keyword": req.ID, "page": "1", "pagesize": "30", "platform": "WebFilter", "filter": "2"}})
		return music.Result{Items: songs(songList(res))}, err
	}
	res, err := c.call(ctx, request{address: gateway + "/v1/search/special", router: "complexsearch.kugou.com", cookie: req.Cookie, profile: lite,
		params: map[string]string{"keyword": req.ID, "page": "1", "pagesize": "30", "platform": "AndroidFilter"}})
	if err != nil {
		return music.Result{}, err
	}
	list := res.Object("data").List("lists", "info")
	if list == nil {
		list = res.List("lists")
	}
	items := make([]music.Item, 0, len(list))
	for _, row := range list {
		hit := providerhttp.AsObject(row)
		id := hit.Text("global_collection_id", "gid", "specialid", "id")
		if id == "" {
			continue
		}
		count := int(hit.Int("song_count", "trackCount"))
		items = append(items, music.Item{ID: id, Source: "kugou", Title: hit.Text("specialname", "name", "title"),
			Author: hit.Text("nickname", "username", "author_name"), URL: providerhttp.RawString(id),
			Pic: providerhttp.RawString(image(hit.Text("img", "image", "imgurl"))), TrackCount: &count})
	}
	return music.Result{Items: items}, nil
}

func validShare(u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	return (u.Scheme == "https" || u.Scheme == "http") && u.User == nil && u.Port() == "" &&
		(host == "kugou.com" || strings.HasSuffix(host, ".kugou.com"))
}

func decodeGCID(id string) string {
	if len(id) > 200 {
		return ""
	}
	m := gcidPattern.FindStringSubmatch(id)
	if len(m) != 2 {
		return ""
	}
	parts := strings.Split(strings.ToLower(m[1][:len(m[1])-2]), "z")
	for i, p := range parts {
		n, okay := new(big.Int).SetString(p, 35)
		if !okay {
			return ""
		}
		parts[i] = n.String()
	}
	return "collection_" + strings.Join(parts, "_")
}

func (c *Client) resolvePlaylist(ctx context.Context, id, cookie string) (string, error) {
	for redirects := 0; redirects <= 5; redirects++ {
		if decoded := decodeGCID(id); decoded != "" {
			return decoded, nil
		}
		if collectionPattern.MatchString(id) {
			return id, nil
		}
		if shareCodePattern.MatchString(id) {
			return c.resolveShareCode(ctx, id, cookie)
		}
		if _, err := strconv.ParseUint(id, 10, 64); err == nil {
			return id, nil
		}
		parsed, err := url.Parse(id)
		if err != nil || !validShare(parsed) {
			return "", music.ErrInvalidID
		}
		if decoded := decodeGCID(strings.Trim(strings.TrimPrefix(parsed.Path, "/songlist/"), "/")); decoded != "" {
			return decoded, nil
		}
		q := parsed.Query()
		collection := providerhttp.First(q.Get("global_collection_id"), q.Get("global_specialid"))
		if collectionPattern.MatchString(collection) {
			return collection, nil
		}
		if numeric, err := strconv.ParseUint(q.Get("specialid"), 10, 64); err == nil && numeric > 0 {
			return q.Get("specialid"), nil
		}
		if redirects == 5 || strings.HasPrefix(parsed.Path, "/songlist/") {
			break
		}
		parsed.Scheme = "https"
		get, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
		if err != nil {
			return "", err
		}
		get.Header.Set("User-Agent", "Mozilla/5.0")
		res, err := c.http.Do(get)
		if err != nil {
			return "", err
		}
		res.Body.Close()
		loc, err := res.Location()
		if err != nil || res.StatusCode < 300 || res.StatusCode >= 400 {
			break
		}
		id = loc.String()
	}
	return "", music.ErrInvalidID
}

func (c *Client) resolveShareCode(ctx context.Context, code, cookie string) (string, error) {
	const app, ver = "1001", "20141"
	now := time.Now()
	body := map[string]any{"appid": 1001, "clientver": 20141, "mid": deviceMID(cookie),
		"clienttime": now.Unix(), "key": digest(app + standard.signSalt + ver + code), "data": code}
	headers := http.Header{"Accept": {"application/json"}, "Content-Type": {"application/x-www-form-urlencoded"},
		"User-Agent": {"KuGou2012-20141-NetworkSuperCall"}, "KG-Clienttimems": {strconv.FormatInt(now.UnixMilli(), 10)},
		"KG-Rc": {"1"}, "KG-Rf": {"FFFFF787"}, "KG-Rec": {"1"}, "KG-Thash": {"5d816a0"}, "Kg-Rfb": {"0"}}
	if devID := providerhttp.CookieValue(cookie, "KUGOU_API_DEV", "kg_devid"); devID != "" {
		headers.Set("KG-Devid", devID)
	}
	var reply providerhttp.Object
	if err := providerhttp.JSON(ctx, c.http, http.MethodPost, "https://t.kugou.com/command/", headers, body, &reply); err != nil {
		return "", err
	}
	info := reply.Object("data").Object("info")
	if info == nil {
		info = reply.Object("info")
	}
	id := info.Text("global_collection_id", "copy_gcid")
	if reply.Int("status") != 1 || reply.Int("err_code") != 0 || !collectionPattern.MatchString(id) {
		return "", music.ErrInvalidID
	}
	return id, nil
}

func (c *Client) playlist(ctx context.Context, req music.Request) (music.Result, error) {
	id, err := c.resolvePlaylist(ctx, req.ID, req.Cookie)
	if err != nil {
		return music.Result{}, err
	}
	var midBytes [16]byte
	if _, err := rand.Read(midBytes[:]); err != nil {
		return music.Result{}, err
	}
	mid := hex.EncodeToString(midBytes[:])
	items := make([]music.Item, 0)
	for page, received := 1, 0; ; page++ {
		params := map[string]string{"appid": "1058", "srcappid": "2919", "clientver": "20000", "clienttime": strconv.FormatInt(time.Now().UnixMilli(), 10),
			"mid": mid, "uuid": mid, "dfid": "-", "uid": "0", "token": "", "type": "0", "module": "playlist", "page": strconv.Itoa(page), "pagesize": "100"}
		idParam := "specialid"
		if collectionPattern.MatchString(id) {
			idParam = "global_collection_id"
		}
		params[idParam] = id
		params["signature"] = signature(params, "", "NVPh5oo715z5DIWAeQlhMDsWXXQV4hwt")
		reply, err := c.call(ctx, request{address: playlistHost + "/v2/get_other_list_file", params: params})
		if err != nil {
			var upstream providerhttp.StatusError
			if errors.Is(err, serviceError(20010)) || (errors.As(err, &upstream) && upstream.Code >= 500) {
				return music.Result{Items: items}, nil
			}
			return music.Result{}, err
		}
		if reply.Int("status") != 1 {
			return music.Result{}, errors.New("Kugou playlist request failed")
		}
		data := reply.Object("data")
		pageSongs := data.List("info")
		received += len(pageSongs)
		items = append(items, songs(pageSongs)...)
		if len(pageSongs) == 0 || int64(received) >= data.Int("count") {
			return music.Result{Items: items}, nil
		}
	}
}

func (c *Client) song(ctx context.Context, req music.Request, hash string, albumAudioID uint64) (music.Result, error) {
	data, err := json.Marshal([]struct {
		Hash    string `json:"hash"`
		AudioID uint64 `json:"audio_id"`
	}{{hash, albumAudioID}})
	if err != nil {
		return music.Result{}, err
	}
	reply, err := c.call(ctx, request{address: gateway + "/v1/audio/audio", router: "kmr.service.kugou.com", cookie: req.Cookie, profile: lite,
		params: map[string]string{"data": string(data)}})
	if err != nil {
		return music.Result{}, err
	}
	items := songs(songList(reply))
	if req.Type == "pic" {
		var picture string
		if len(items) != 0 && json.Unmarshal(items[0].Pic, &picture) == nil && picture != "" {
			return music.Result{Image: picture}, nil
		}
		return music.Result{}, music.ErrNoURL
	}
	return music.Result{Items: items}, nil
}

func (c *Client) fm(ctx context.Context, cookie string) (music.Result, error) {
	now := time.Now().UnixMilli()
	body := map[string]any{"appid": 3116, "clienttime": now, "mid": deviceMID(cookie), "action": "play",
		"recommend_source_locked": 0, "song_pool_id": 0, "callerid": 0, "m_type": 1, "platform": "ios", "area_code": 1,
		"remain_songcnt": 0, "clientver": 11440, "is_overplay": 0, "mode": "normal", "fakem": "ca981cfc583a4c37f28d2d49000013c16a0a",
		"key": digest(lite.appID + lite.signSalt + lite.version + strconv.FormatInt(now, 10))}
	for _, key := range []string{"userid", "token", "vip_type"} {
		if value := providerhttp.CookieValue(cookie, key); value != "" {
			body[key] = value
			if key == "userid" {
				body["kguid"] = value
			}
		}
	}
	reply, err := c.call(ctx, request{address: gateway + "/v2/personal_recommend", router: "persnfm.service.kugou.com", profile: lite, body: body, cookie: cookie})
	return music.Result{Items: songs(songList(reply))}, err
}
