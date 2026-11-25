package kugou

import (
	"context"
	"errors"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
	"github.com/nova1751/openmusic-meting-api-go/internal/providerhttp"
)

type quality struct{ wire, name string }

var qualities = map[string]quality{
	"standard": {"128", "标准"}, "exhigh": {"320", "极高"}, "lossless": {"flac", "无损"},
	"hires": {"high", "Hi-Res音质"}, "atmos": {"viper_atmos", "蝰蛇全景声2.0"}, "master": {"super", "母带"},
	"viper_atmos": {"viper_atmos", "蝰蛇全景声2.0"}, "viper_tape": {"viper_tape", "蝰蛇母带音质"},
	"viper_clear": {"viper_clear", "蝰蛇超清音质"}, "viper_hifi": {"viper_hifi", "蝰蛇HiFi音质"},
	"acappella": {"magic_acappella", "人声伴奏"}, "multitrack": {"multitrack", "多轨音质"},
}

func actualQuality(info providerhttp.Object) quality {
	name := strings.ToLower(info.Text("quality", "quality_name"))
	switch name {
	case "magic_acappella":
		return qualities["acappella"]
	case "super", "high", "flac", "320", "128":
	default:
		if q, ok := qualities[name]; ok {
			return q
		}
	}
	bitrate := info.Int("bitRate", "bitrate", "bit_rate")
	if bitrate > 10000 {
		bitrate /= 1000
	}
	switch {
	case strings.EqualFold(info.Text("extName", "extname", "ext"), "flac") || bitrate >= 700:
		return qualities["lossless"]
	case bitrate >= 280:
		return qualities["exhigh"]
	default:
		return qualities["standard"]
	}
}

func audioLink(value any) string {
	switch value := value.(type) {
	case []any:
		for _, item := range value {
			if target := audioLink(item); target != "" {
				return target
			}
		}
	case string:
		target, err := url.Parse(strings.TrimSpace(value))
		if err == nil && target.Hostname() != "" && target.User == nil &&
			(target.Scheme == "https" || target.Scheme == "http") {
			return target.String()
		}
	}
	return ""
}

func audioInfo(data providerhttp.Object) providerhttp.Object {
	if nested := data.Object("data"); nested != nil {
		return nested
	}
	return data
}

func playbackLink(info providerhttp.Object) music.Link {
	for _, name := range []string{"play_url", "url", "ori_url", "backupUrl", "backup_url"} {
		if address := audioLink(info[name]); address != "" {
			level := actualQuality(info)
			volume := map[string]float64{}
			for _, key := range []struct{ field, name string }{{"volume", "gain"}, {"volume_peak", "peak"}} {
				n, err := strconv.ParseFloat(info.Text(key.field), 64)
				if err == nil && !math.IsNaN(n) && !math.IsInf(n, 0) {
					volume[key.name] = n
				}
			}
			if len(volume) == 0 {
				volume = nil
			}
			return music.Link{URL: address, Quality: level.name,
				Duration: info.Int("timeLength", "duration"), Loudness: volume}
		}
	}
	return music.Link{}
}

func (c *Client) play(ctx context.Context, req music.Request, hash string, albumAudioID uint64) (music.Result, error) {
	q, ok := qualities[music.NormalizeQuality(req.Quality)]
	if !ok {
		return music.Result{}, music.ErrNoURL
	}
	token := providerhttp.CookieValue(req.Cookie, "token")
	user := providerhttp.CookieValue(req.Cookie, "userid")
	if token == "" || user == "" {
		return music.Result{}, music.ErrAccountInvalid
	}
	profile := lite
	if strings.EqualFold(providerhttp.CookieValue(req.Cookie, "KUGOU_API_PLATFORM", "kg_platform"), "standard") {
		profile = standard
	}
	req.Cookie = c.cachedDevice(c.prepareDevice(req.Cookie), profile)
	params := map[string]string{"album_id": "0", "area_code": "1", "hash": hash, "ssa_flag": "is_fromtrack", "version": "11430", "clientver": "11430",
		"page_id": profile.pageID, "quality": q.wire, "album_audio_id": strconv.FormatUint(albumAudioID, 10),
		"behavior": "play", "pid": profile.playerID, "cmd": "26", "pidversion": "3001", "cdnBackup": "1",
		"IsFreePart": "0", "ppage_id": profile.playPages, "module": "", "key": digest(hash + profile.trackSalt + profile.appID + deviceMID(req.Cookie) + user)}
	input := request{address: gateway + "/v5/url", router: "trackercdn.kugou.com", params: params, cookie: req.Cookie, profile: profile}
	res, err := c.call(ctx, input)
	if errors.Is(err, serviceError(20028)) && !validDFID(req.Cookie) {
		if registered, registerErr := c.registerDevice(ctx, req.Cookie, profile); registerErr == nil {
			req.Cookie, input.cookie = registered, registered
			res, err = c.call(ctx, input)
		}
	}
	if err == nil {
		if link := playbackLink(audioInfo(res)); link.URL != "" {
			return music.Result{Link: link}, nil
		}
	}
	if errors.Is(err, music.ErrAccountInvalid) {
		return music.Result{}, err
	}
	if errors.Is(err, serviceError(20028)) {
		return music.Result{}, music.ErrVerificationRequired
	}
	if ctx.Err() != nil {
		return music.Result{}, ctx.Err()
	}
	link, authErr := c.authorizedPlayback(ctx, req.Cookie, hash, profile, params)
	if authErr == nil && link.URL != "" {
		return music.Result{Link: link}, nil
	}
	if authErr != nil {
		return music.Result{}, authErr
	}
	if err != nil && !errors.Is(err, serviceError(20010)) {
		return music.Result{}, err
	}
	return music.Result{}, music.ErrNoURL
}

func (c *Client) authorizedPlayback(ctx context.Context, cookie, hash string, profile profile, params map[string]string) (music.Link, error) {
	auth := providerhttp.CookieValue(cookie, "auth")
	const router = "trackercdngz.kugou.com"
	if auth == "" {
		res, err := c.call(ctx, request{address: gateway + "/v1/user_verify", router: router,
			params: map[string]string{"module_id": "51"}, cookie: cookie, profile: profile})
		if err != nil {
			return music.Link{}, err
		}
		if res.Int("status") != 1 {
			return music.Link{}, nil
		}
		auth = res.Object("data").Text("auth")
	}
	if auth == "" {
		return music.Link{}, nil
	}
	res, err := c.call(ctx, request{address: gateway + "/v1/authorization", router: router,
		params: map[string]string{"authorization": auth, "module_id": "51", "album_audio_id": params["album_audio_id"], "hash": hash, "clientver": "11561"},
		cookie: cookie, profile: profile})
	if err != nil {
		return music.Link{}, err
	}
	if res.Int("status") != 1 {
		return music.Link{}, nil
	}
	data := res.Object("data")
	if data.Text("auth") == "" || data.Int("open_time") <= 0 {
		return music.Link{}, nil
	}
	delete(params, "IsFreePart")
	params["auth"], params["open_time"] = data.Text("auth"), data.Text("open_time")
	params["need_m"], params["ptype"], params["need_ogg"], params["mtype"], params["module_id"], params["clientver"] = "0", "0", "1", "0", "51", "11561"
	res, err = c.call(ctx, request{address: gateway + "/tracker/v5/url", params: params, cookie: cookie, profile: profile})
	if err != nil {
		return music.Link{}, err
	}
	return playbackLink(audioInfo(res)), nil
}
