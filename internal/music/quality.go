package music

import "slices"

var qualities = map[string][]string{
	"netease": {"higher", "hires", "jyeffect", "sky", "jymaster", "dolby"},
	"tencent": {"atmos", "master"},
	"qishui":  {"studio", "atmos"},
	"kugou":   {"hires", "atmos", "master", "viper_atmos", "viper_tape", "viper_clear", "viper_hifi", "acappella", "multitrack"},
}

func KnownPlatform(platform string) bool {
	_, ok := qualities[platform]
	return ok
}

func Supports(platform, operation string) bool {
	switch operation {
	case "song", "playlist", "search", "search_playlist", "fm", "url", "lrc", "pic":
		return KnownPlatform(platform)
	case "artist", "dj", "dj_detail", "djprogram", "dj_hot", "search_dj":
		return platform == "netease"
	default:
		return false
	}
}

func ValidQuality(platform, quality string) bool {
	switch quality {
	case "", "standard", "128", "exhigh", "320", "lossless", "flac":
		return true
	default:
		return slices.Contains(qualities[platform], quality)
	}
}

func NormalizeQuality(quality string) string {
	switch quality {
	case "", "128":
		return "standard"
	case "320":
		return "exhigh"
	case "flac":
		return "lossless"
	default:
		return quality
	}
}

func RequiredMembership(neteaseQuality string) Membership {
	switch NormalizeQuality(neteaseQuality) {
	case "dolby", "jymaster", "sky":
		return SVIP
	case "jyeffect", "hires", "lossless", "exhigh":
		return VIP
	default:
		return Free
	}
}

func NeteaseQualityName(quality string) string {
	if name := neteaseNames[quality]; name != "" {
		return name
	}
	return quality
}

var neteaseNames = map[string]string{
	"standard": "标准", "higher": "较高", "exhigh": "极高", "lossless": "无损",
	"hires": "高解析度无损", "jyeffect": "高清臻音", "sky": "沉浸环绕声",
	"jymaster": "超清母带", "dolby": "杜比全景声",
}
