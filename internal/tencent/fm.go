package tencent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
)

func (c *Client) fm(ctx context.Context, req music.Request) (music.Result, error) {
	uin := first(cookieField(req.Cookie, "uin"), cookieField(req.Cookie, "wxuin"))
	key := first(cookieField(req.Cookie, "qqmusic_key"), cookieField(req.Cookie, "qm_keyst"))
	if uin == "" || uin == "0" || key == "" {
		return music.Result{}, music.ErrAccountInvalid
	}

	payload, err := json.Marshal(map[string]any{
		"radio": map[string]any{
			"module": "music.radioProxy.MbTrackRadioSvr",
			"method": "get_radio_track",
			"param":  map[string]any{"id": 99, "num": 5, "from": 0, "scene": 0, "song_ids": []string{}},
		},
		"comm": map[string]any{"uin": uin, "format": "json", "ct": 11, "cv": 14090008, "authst": key},
	})
	if err != nil {
		return music.Result{}, err
	}
	q := url.Values{"data": {string(payload)}}
	var result struct {
		Code  int `json:"code"`
		Radio struct {
			Code int `json:"code"`
			Data struct {
				Tracks []track `json:"tracks"`
			} `json:"data"`
		} `json:"radio"`
	}
	if err := c.request(ctx, http.MethodGet, "https://u.y.qq.com/cgi-bin/musicu.fcg?"+q.Encode(), req.Cookie, nil, &result); err != nil {
		return music.Result{}, err
	}
	for _, code := range []int{result.Code, result.Radio.Code} {
		switch code {
		case 0:
		case 1000, 104400, 104401:
			return music.Result{}, music.ErrAccountInvalid
		case 104009:
			return music.Result{}, music.ErrVerificationRequired
		default:
			return music.Result{}, fmt.Errorf("tencent FM response code %d", code)
		}
	}
	return items(result.Radio.Data.Tracks)
}
