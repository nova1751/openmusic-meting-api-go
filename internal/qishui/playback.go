package qishui

import (
	"context"
	"encoding/json"
	"maps"
	"math"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
	"github.com/nova1751/openmusic-meting-api-go/internal/providerhttp"
)

type stream struct {
	url, auth, quality, mime, format string
	duration                         int64
	bitrate                          float64
	isVideo, mp4                     bool
}

func contains(text string, terms ...string) bool {
	for _, term := range terms {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}

func streamRank(entry stream) int {
	label := strings.ToLower(entry.quality + " " + entry.format)
	bitrate := entry.bitrate
	if bitrate > 10000 {
		bitrate /= 1000
	}
	switch {
	case contains(label, "studio", "recording", "录音室", "spatial"):
		return 130
	case contains(label, "atmos", "dolby", "全景", "hires", "hi_res", "hi-res"):
		return 120
	case contains(label, "lossless", "flac", "sq", "highest") || bitrate >= 900:
		return 100
	case contains(label, "excellent", "superhigh", "hq"):
		return 80
	case contains(label, "higher", "high", "320") || bitrate >= 320:
		return 70
	default:
		return 50
	}
}

func playbackStream(root object, quality string) (stream, int) {
	ranks := map[string]int{"studio": 130, "atmos": 120, "lossless": 100, "exhigh": 70, "standard": 50}
	target := ranks[music.NormalizeQuality(quality)]
	if target == 0 {
		target = 50
	}
	var signals []stream
	seen := map[string]bool{}
	budget := 12000
	var visit func(any, stream, int)
	visit = func(value any, parent stream, depth int) {
		if value == nil || depth > 8 || budget <= 0 {
			return
		}
		budget--
		switch v := value.(type) {
		case []any:
			for _, item := range v[:min(250, len(v))] {
				visit(item, parent, depth+1)
			}
			return
		case string:
			v = strings.TrimSpace(v)
			if len(v) > 0 && len(v) < 2_000_000 && (v[0] == '{' || v[0] == '[') {
				var parsed any
				dec := json.NewDecoder(strings.NewReader(v))
				dec.UseNumber()
				if dec.Decode(&parsed) == nil {
					visit(parsed, parent, depth+1)
				}
			}
			return
		case map[string]any:
			value = object(v)
		}
		o, ok := value.(object)
		if !ok {
			return
		}
		meta := o.Object("video_meta", "VideoMeta", "meta")
		crypt := o.Object("encrypt_info", "EncryptInfo", "encryptInfo")
		entry := stream{auth: providerhttp.First(o.Text("play_auth", "PlayAuth", "spade_a"), crypt.Text("spade_a"), parent.auth),
			quality:  providerhttp.First(o.Text("quality", "Quality", "definition"), meta.Text("quality"), o.Text("gear_des_key"), parent.quality),
			duration: parent.duration}
		duration := providerhttp.Number(o.Get("duration", "video_duration"))
		if duration > 0 {
			if duration > 10000 {
				duration /= 1000
			}
			entry.duration = int64(math.Round(duration)) * 1000
		}
		for _, field := range []string{"main_play_url", "MainPlayUrl", "main_url", "url", "URL", "play_url", "PlayURL", "backup_play_url", "backup_urls"} {
			if entry.url = cover(o[field]); entry.url != "" {
				break
			}
		}
		if entry.url != "" && !seen[entry.url] {
			entry.format = providerhttp.First(o.Text("format", "Format", "vtype"), meta.Text("format", "vtype"))
			u, _ := url.Parse(entry.url)
			entry.mime = u.Query().Get("mime_type")
			codec := providerhttp.First(o.Text("codec_type", "codecType"), meta.Text("codec_type", "codecType"))
			audio := contains(strings.ToLower(entry.format+" "+codec), "aac", "flac", "mp3", "m4a", "opus", "audio")
			entry.isVideo = !audio && contains(strings.ToLower(entry.format+" "+entry.mime), "video")
			entry.mp4 = contains(strings.ToLower(entry.format+" "+entry.mime+" "+entry.url), "mp4")
			entry.bitrate = providerhttp.Number(providerhttp.First(o.Text("bitrate", "Bitrate", "real_bitrate", "br"), meta.Text("bitrate")))
			seen[entry.url] = true
			signals = append(signals, entry)
		}
		keys := slices.Sorted(maps.Keys(o))
		for _, key := range keys[:min(250, len(keys))] {
			visit(o[key], entry, depth+1)
		}
	}
	visit(root, stream{}, 0)
	audio := make([]stream, 0, len(signals))
	for _, entry := range signals {
		if !entry.isVideo {
			audio = append(audio, entry)
		}
	}
	if len(audio) == 0 {
		for _, entry := range signals {
			if entry.mp4 {
				audio = append(audio, entry)
			}
		}
	}
	if len(audio) == 0 {
		return stream{}, 0
	}
	sort.SliceStable(audio, func(i, j int) bool { return streamRank(audio[i]) > streamRank(audio[j]) })
	for _, entry := range audio {
		if rank := streamRank(entry); rank <= target {
			return entry, rank
		}
	}
	entry := audio[len(audio)-1]
	return entry, streamRank(entry)
}

func loudness(payload object) map[string]float64 {
	var out map[string]float64
	walk(payload, 8, 250, func(o object) {
		if out != nil {
			return
		}
		gain, err1 := strconv.ParseFloat(o.Text("gain", "loudness"), 64)
		peak, err2 := strconv.ParseFloat(o.Text("peak", "volume_peak"), 64)
		if err1 == nil && err2 == nil && !math.IsNaN(gain) && !math.IsNaN(peak) && !math.IsInf(gain, 0) && !math.IsInf(peak, 0) {
			out = map[string]float64{"gain": gain, "peak": peak}
		}
	})
	return out
}

func (c *Client) play(ctx context.Context, req music.Request) (music.Result, error) {
	if err := idParam(req.ID); err != nil {
		return music.Result{}, err
	}
	if !fingerprint(req.Cookie) {
		return music.Result{}, music.ErrAccountInvalid
	}
	queue, scene := "favorite_track_playlist", "library"
	if req.FM {
		queue, scene = "daily_mix", "track_reco"
	}
	res, err := c.request(ctx, upstreamRequest{endpoint: pcHost + "/luna/pc/track_v2", cookie: req.Cookie,
		agent: "LunaPC/3.0.0(290101097)", signed: true, query: c.params(false, url.Values{"version_name": {"3.0.0"}, "version_code": {"30000000"}}),
		body: map[string]string{"track_id": req.ID, "media_type": "track", "queue_type": queue, "scene_name": scene}})
	if err != nil {
		return music.Result{}, err
	}
	entry, rank := playbackStream(res, req.Quality)
	if entry.url == "" {
		return music.Result{}, music.ErrNoURL
	}
	name := "标准"
	switch {
	case rank >= 130:
		name = "录音室音质"
	case rank >= 120:
		name = "全景声"
	case rank >= 100:
		name = "无损"
	case rank >= 70:
		name = "极高"
	}
	return music.Result{Link: music.Link{URL: entry.url, PlayAuth: entry.auth, MediaType: entry.mime, Quality: name,
		Duration: entry.duration, Loudness: loudness(res)}}, nil
}
