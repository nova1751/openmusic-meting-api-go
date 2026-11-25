package tencent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
	"github.com/nova1751/openmusic-meting-api-go/internal/providerhttp"
)

type loginDevice struct {
	Q16         string `json:"q16"`
	Q36         string `json:"q36"`
	GUID        string `json:"guid"`
	AndroidID   string `json:"androidId"`
	Release     string `json:"release"`
	SDK         string `json:"sdk"`
	Model       string `json:"model"`
	Fingerprint string `json:"fingerprint"`
}

func parseLoginDevice(cookie string) (loginDevice, bool) {
	var device loginDevice
	value := cookieField(cookie, "psrf_qqdevice")
	if len(value) > 2048 {
		return device, false
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(value, "="))
	ok := err == nil && json.Unmarshal(data, &device) == nil &&
		device.Q16 != "" && device.Q36 != "" && device.GUID != "" && device.AndroidID != ""
	return device, ok
}

func (d loginDevice) comm() map[string]any {
	return map[string]any{
		"ct": 11, "cv": 14090008, "v": 14090008, "chid": "10003505", "tmeAppID": "qqmusic",
		"QIMEI": d.Q16, "QIMEI36": d.Q36, "OpenUDID": d.GUID, "udid": d.GUID, "OpenUDID2": d.GUID,
		"uid": "", "sid": "", "aid": d.AndroidID, "os_ver": d.Release, "phonetype": d.Model,
		"devicelevel": d.SDK, "newdevicelevel": d.SDK, "rom": d.Fingerprint,
	}
}

func (c *Client) mobileCall(ctx context.Context, comm map[string]any, module, method string, params map[string]any) (providerhttp.Object, error) {
	payload := map[string]any{"comm": comm, "req_0": map[string]any{"module": module, "method": method, "param": params}}
	headers := http.Header{"User-Agent": {"QQMusic 14090008(android 10)"}}
	var reply providerhttp.Object
	if err := providerhttp.JSON(ctx, c.http, http.MethodPost, "https://u.y.qq.com/cgi-bin/musicu.fcg", headers, payload, &reply); err != nil {
		return nil, err
	}
	result := reply.Object("req_0")
	if result == nil {
		return nil, errors.New("missing QQ Music mobile response")
	}
	for _, code := range []int64{reply.Int("code"), result.Int("code")} {
		switch code {
		case 0:
		case 1000, 104400, 104401:
			return nil, music.ErrAccountInvalid
		case 104009:
			return nil, music.ErrVerificationRequired
		default:
			return nil, fmt.Errorf("QQ Music mobile returned code %d", code)
		}
	}
	return result.Object("data"), nil
}
