package kugou

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/providerhttp"
)

const qrBase = "https://h5.kugou.com/apps/loginQRCode/html/index.html"

type QRSession struct {
	key, guid, mid, device string
	expires                time.Time
}

func (q *QRSession) URL() string {
	return qrBase + "?" + (url.Values{"qrcode": {q.key}}).Encode()
}

func (q *QRSession) cookie() string {
	return "KUGOU_API_GUID=" + q.guid + "; KUGOU_API_MID=" + q.mid + "; KUGOU_API_DEV=" + q.device + "; KUGOU_API_MAC=02:00:00:00:00:00; dfid=-"
}

func (c *Client) requestQR(ctx context.Context, path string, params map[string]string, cookie string) (providerhttp.Object, error) {
	params["clientver"] = "11440"
	params["clienttime"] = strconv.FormatInt(time.Now().Unix(), 10)
	params["dfid"], params["mid"], params["uuid"] = "-", "0", "-"
	params["signature"] = signature(params, "", "NVPh5oo715z5DIWAeQlhMDsWXXQV4hwt")
	q := url.Values{}
	for key, value := range params {
		q.Set(key, value)
	}
	headers := http.Header{"Accept": {"application/json, text/plain, */*"},
		"User-Agent": {"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/122 Safari/537.36"},
		"Referer":    {"https://h5.kugou.com/"}, "Origin": {"https://h5.kugou.com"}, "Cookie": {cookie}}
	var result providerhttp.Object
	err := providerhttp.JSON(ctx, c.http, http.MethodGet, "https://login-user.kugou.com"+path+"?"+q.Encode(), headers, nil, &result)
	return result, err
}

func (c *Client) CreateQR(ctx context.Context) (*QRSession, error) {
	var identity [21]byte
	if _, err := rand.Read(identity[:]); err != nil {
		return nil, err
	}
	session := &QRSession{guid: digest(hex.EncodeToString(identity[:16])),
		device: strings.ToUpper(hex.EncodeToString(identity[16:])), expires: time.Now().Add(5 * time.Minute)}
	mid, _ := new(big.Int).SetString(digest(session.guid), 16)
	session.mid = mid.String()
	result, err := c.requestQR(ctx, "/v2/qrcode", map[string]string{
		"appid": "1001", "type": "1", "plat": "4", "qrcode_txt": qrBase + "?appid=3116&", "srcappid": "2919",
	}, session.cookie())
	if err != nil {
		return nil, err
	}
	session.key = providerhttp.First(result.Object("data").Text("qrcode", "key"), result.Text("qrcode", "key"))
	if session.key == "" || result.Text("status") == "0" {
		return nil, errors.New("Kugou QR login did not return a QR code")
	}
	return session, nil
}

func (c *Client) PollQR(ctx context.Context, session *QRSession) (status, cookie string, err error) {
	if time.Now().After(session.expires) {
		return "expired", "", nil
	}
	result, err := c.requestQR(ctx, "/v2/get_userinfo_qrcode", map[string]string{
		"appid": "3116", "plat": "4", "srcappid": "2919", "qrcode": session.key, "dev": session.device,
	}, session.cookie())
	if err != nil {
		return "", "", err
	}
	data := result.Object("data")
	switch providerhttp.First(data.Text("status"), result.Text("status")) {
	case "4":
		token, id := data.Text("token"), data.Text("userid", "user_id")
		if token == "" || id == "" || strings.ContainsAny(token+id, ";\r\n") {
			return "", "", errors.New("Kugou QR login did not return valid credentials")
		}
		cookie := "token=" + token + "; userid=" + id + "; " + session.cookie()
		for _, name := range []string{"vip_token", "vip_type"} {
			if value := data.Text(name); value != "" && !strings.ContainsAny(value, ";\r\n") {
				cookie += "; " + name + "=" + value
			}
		}
		return "confirmed", cookie, nil
	case "2":
		return "scanned", "", nil
	case "0", "3":
		return "expired", "", nil
	default:
		return "waiting", "", nil
	}
}
