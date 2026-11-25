package tencent

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/providerhttp"
)

var qrIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_=-]{1,256}$`)

type QRSession struct {
	id      string
	image   []byte
	expires time.Time
	cancel  context.CancelFunc
	mu      sync.Mutex
	status  string
	cookie  string
}

func (q *QRSession) PNG() []byte { return q.image }
func (q *QRSession) Close()      { q.cancel() }
func (q *QRSession) Poll() (status, cookie string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if time.Now().After(q.expires) {
		return "expired", ""
	}
	return q.status, q.cookie
}

func (q *QRSession) set(status, cookie string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.status, q.cookie = status, cookie
}

func qqWebSignature(body []byte) string {
	sum := sha1.Sum(body)
	hash := hex.EncodeToString(sum[:])
	part := func(offsets []int) string {
		var out strings.Builder
		for _, index := range offsets {
			out.WriteByte(hash[index])
		}
		return out.String()
	}
	mask := []byte{89, 39, 179, 150, 218, 82, 58, 252, 177, 52, 186, 123, 120, 64, 242, 133, 143, 161, 121, 179}
	for i := range mask {
		mask[i] ^= sum[i]
	}
	middle := strings.NewReplacer("+", "", "/", "", "=", "").Replace(base64.StdEncoding.EncodeToString(mask))
	return "zzc" + part([]int{23, 14, 6, 36, 16, 7, 19}) + strings.ToLower(middle) + part([]int{16, 1, 32, 12, 19, 27, 8, 5})
}

func (c *Client) CreateQR(ctx context.Context) (*QRSession, error) {
	body, err := json.Marshal(map[string]any{
		"comm": map[string]any{"g_tk": 0, "uin": 0, "format": "json", "inCharset": "utf-8", "outCharset": "utf-8", "notice": 0,
			"platform": "h5", "needNewCode": 1, "ct": 23, "cv": 0},
		"req_0": map[string]any{"module": "music.login.LoginServer", "method": "CreateQRCode",
			"param": map[string]any{"tmeAppID": "qqmusic", "ct": 11, "cv": 20030508}},
	})
	if err != nil {
		return nil, err
	}
	query := url.Values{"_webcgikey": {"CreateQRCode"}, "sign": {qqWebSignature(body)}, "_": {strconv.FormatInt(time.Now().UnixMilli(), 10)}}
	headers := http.Header{"User-Agent": {"QQMusic 20030508(android 14)"}, "Origin": {"https://y.qq.com"}, "Referer": {"https://y.qq.com/"}}
	var reply providerhttp.Object
	if err := providerhttp.JSON(ctx, c.http, http.MethodPost, "https://u.y.qq.com/cgi-bin/musics.fcg?"+query.Encode(), headers, json.RawMessage(body), &reply); err != nil {
		return nil, err
	}
	result := reply.Object("req_0")
	data := result.Object("data")
	id := data.Text("qrcodeID")
	image := data.Text("qrcode")
	image = strings.TrimPrefix(image, "data:image/png;base64,")
	if reply.Int("code") != 0 || result.Int("code") != 0 || !qrIDPattern.MatchString(id) || len(image) > 1<<20 {
		return nil, errors.New("QQ Music QR code is unavailable")
	}
	png, err := base64.StdEncoding.DecodeString(image)
	if err != nil || !strings.HasPrefix(string(png), "\x89PNG\r\n\x1a\n") {
		return nil, errors.New("QQ Music QR code has invalid image data")
	}
	ttl := data.Int("expiresIn")
	if ttl < 60 || ttl > 900 {
		ttl = 900
	}
	expires := time.Now().Add(time.Duration(ttl) * time.Second)
	listenCtx, cancel := context.WithDeadline(context.Background(), expires)
	session := &QRSession{id: id, image: png, expires: expires, cancel: cancel, status: "waiting"}
	go c.listenQR(listenCtx, session)
	return session, nil
}

func eventCookie(cookies providerhttp.Object, name string) string {
	return first(cookies.Text(name), cookies.Object(name).Text("value"))
}

func (c *Client) confirmQR(ctx context.Context, id string, raw []byte) (string, error) {
	payload, err := providerhttp.Decode(raw)
	if err != nil {
		return "", err
	}
	cookies := payload.Object("cookies")
	user := strings.TrimPrefix(eventCookie(cookies, "qqmusic_uin"), "o")
	token := eventCookie(cookies, "qqmusic_key")
	contextID := eventCookie(cookies, "qrcode_id")
	n, parseErr := strconv.ParseUint(user, 10, 64)
	if parseErr != nil || n == 0 || token == "" || len(token) > 2048 || contextID != "" && contextID != id {
		return "", errors.New("invalid QQ Music QR confirmation")
	}
	device, err := c.registerLoginDevice(ctx)
	if err != nil {
		return "", err
	}
	comm := device.comm()
	data, err := c.mobileCall(ctx, comm, "music.getSession.session", "GetSession", map[string]any{"uid": "", "vkey": 0, "caller": 0})
	if err != nil {
		return "", err
	}
	serverSession := data.Object("session")
	uid, sid := serverSession.Text("uid"), serverSession.Text("sid")
	if uid == "" || sid == "" {
		return "", errors.New("QQ Music mobile session was not initialized")
	}
	comm["uid"], comm["sid"], comm["tmeLoginType"] = uid, sid, 6
	data, err = c.mobileCall(ctx, comm, "music.login.LoginServer", "Login", map[string]any{"musicid": n, "qrCodeID": id, "token": token})
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(device)
	if err != nil {
		return "", err
	}
	return mergeRefreshedCookie("login_type=6; tmeLoginMethod=3; tmeLoginType=6; psrf_qqdevice="+base64.RawURLEncoding.EncodeToString(encoded), user, data)
}
