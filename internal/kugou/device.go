package kugou

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/providerhttp"
)

const standardPublicKey = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDIAG7QOELSYoIJvTFJhMpe1s/gbjDJX51HBNnEl5HXqTW6lQ7LC8jr9fWZTwusknp+sVGzwd40MwP6U5yDE27M/X1+UR4tvOGOqp94TJtQ1EPnWGWXngpeIW5GxoQGao1rmYWAu6oi1z9XkChrsUdC6DJE5E221wf/4WLFxwAtRQIDAQAB
-----END PUBLIC KEY-----`
const litePublicKey = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDECi0Np2UR87scwrvTr72L6oO01rBbbBPriSDFPxr3Z5syug0O24QyQO8bg27+0+4kBzTBTBOZ/WWU0WryL1JSXRTXLgFVxtzIY41Pe7lPOgsfTCn5kZcvKhYKJesKnnJDNr5/abvTGf+rHG3YRwsCHcQ08/q6ifSioBszvb3QiwIDAQAB
-----END PUBLIC KEY-----`

type deviceEntry struct {
	dfid    string
	expires time.Time
}

func setCookieField(cookie, name, value string) string {
	fields := strings.Split(cookie, ";")
	out := make([]string, 0, len(fields)+1)
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if key, _, ok := strings.Cut(field, "="); ok && !strings.EqualFold(strings.TrimSpace(key), name) {
			out = append(out, field)
		}
	}
	out = append(out, name+"="+value)
	return strings.Join(out, "; ")
}

func (c *Client) prepareDevice(cookie string) string {
	guid := providerhttp.First(providerhttp.CookieValue(cookie, "KUGOU_API_GUID"), c.guid)
	cookie = setCookieField(cookie, "KUGOU_API_GUID", guid)
	if deviceMID(cookie) == "0" {
		mid, _ := new(big.Int).SetString(digest(guid), 16)
		cookie = setCookieField(cookie, "KUGOU_API_MID", mid.String())
	}
	return cookie
}

func prepareRegistration(cookie string, profile profile) ([32]byte, string, string, error) {
	var entropy [6]byte
	rand.Read(entropy[:])
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	for i := range entropy {
		entropy[i] = alphabet[int(entropy[i])%len(alphabet)]
	}
	seed := string(entropy[:])
	salt := [32]byte([]byte(digest(seed)))
	guid := providerhttp.CookieValue(cookie, "KUGOU_API_GUID")
	portrait, err := json.Marshal(map[string]any{"aes": seed, "uid": providerhttp.First(providerhttp.CookieValue(cookie, "userid"), "0"),
		"token": providerhttp.CookieValue(cookie, "token")})
	if err != nil {
		return salt, "", "", err
	}
	key := litePublicKey
	if profile.appID == standard.appID {
		key = standardPublicKey
	}
	pemKey, _ := pem.Decode([]byte(key))
	decoded, err := x509.ParsePKIXPublicKey(pemKey.Bytes)
	if err != nil {
		return salt, "", "", err
	}
	rsaKey, ok := decoded.(*rsa.PublicKey)
	if !ok || len(portrait) > rsaKey.Size()-11 {
		return salt, "", "", errors.New("Kugou credentials cannot be used for device registration")
	}
	sealed, err := rsa.EncryptPKCS1v15(rand.Reader, rsaKey, portrait)
	if err != nil {
		return salt, "", "", err
	}
	data := map[string]any{"availableRamSize": 4983533568, "availableRomSize": 48114719, "availableSDSize": 48114717,
		"basebandVer": "", "batteryLevel": 100, "batteryStatus": 3, "brand": "Redmi", "buildSerial": "unknown", "device": "marble",
		"imei": guid, "imsi": "", "manufacturer": "Xiaomi", "uuid": guid}
	for _, name := range []string{"accelerometer", "gravity", "gyroscope", "light", "magnetic", "orientation", "pressure", "step_counter", "temperature"} {
		data[name], data[name+"Value"] = false, ""
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return salt, "", "", err
	}
	padding := aes.BlockSize - len(payload)%aes.BlockSize
	payload = append(payload, bytes.Repeat([]byte{byte(padding)}, padding)...)
	block, err := aes.NewCipher(salt[:16])
	if err != nil {
		return salt, "", "", err
	}
	cipher.NewCBCEncrypter(block, salt[16:]).CryptBlocks(payload, payload)
	return salt, hex.EncodeToString(sealed), base64.StdEncoding.EncodeToString(payload), nil
}

func (c *Client) cachedDevice(cookie string, profile profile) string {
	key := sha256.Sum256([]byte(profile.appID + "\x00" + deviceMID(cookie) + "\x00" + providerhttp.CookieValue(cookie, "userid") + "\x00" + providerhttp.CookieValue(cookie, "token")))
	c.deviceMu.Lock()
	entry := c.devices[key]
	c.deviceMu.Unlock()
	if validDFID(cookie) || !entry.expires.After(time.Now()) {
		return cookie
	}
	return setCookieField(cookie, "dfid", entry.dfid)
}

func validDFID(cookie string) bool {
	id := providerhttp.CookieValue(cookie, "dfid", "kg_dfid")
	return id != "" && id != "-"
}

func (c *Client) registerDevice(ctx context.Context, cookie string, profile profile) (string, error) {
	if cached := c.cachedDevice(cookie, profile); validDFID(cached) {
		return cached, nil
	}
	salt, portrait, payload, err := prepareRegistration(cookie, profile)
	if err != nil {
		return "", err
	}
	mid := deviceMID(cookie)
	params := map[string]string{"part": "1", "platid": "1", "p": portrait, "dfid": "-", "mid": mid, "uuid": "-",
		"appid": profile.appID, "clientver": profile.version, "clienttime": strconv.FormatInt(time.Now().Unix(), 10)}
	for _, name := range []string{"token", "userid"} {
		if value := providerhttp.CookieValue(cookie, name); value != "" {
			params[name] = value
		}
	}
	params["signature"] = signature(params, payload, profile.signSalt)
	query := url.Values{}
	for name, value := range params {
		query.Set(name, value)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://userservice.kugou.com/risk/v3/r_register_dev?"+query.Encode(), strings.NewReader(payload))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("User-Agent", "Android15-1070-11083-46-0-DiscoveryDRADProtocol-wifi")
	request.Header.Set("Cookie", cookie)
	request.Header.Set("Dfid", "-")
	request.Header.Set("Mid", mid)
	request.Header.Set("Clienttime", params["clienttime"])
	response, err := c.http.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", errors.New("Kugou device registration returned a non-success status")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil || len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return "", errors.New("invalid Kugou device response")
	}
	block, err := aes.NewCipher(salt[:16])
	if err != nil {
		return "", err
	}
	cipher.NewCBCDecrypter(block, salt[16:]).CryptBlocks(data, data)
	pad := int(data[len(data)-1])
	if pad == 0 || pad > aes.BlockSize || !bytes.Equal(data[len(data)-pad:], bytes.Repeat([]byte{byte(pad)}, pad)) {
		return "", errors.New("invalid Kugou device response padding")
	}
	reply, err := providerhttp.Decode(data[:len(data)-pad])
	dfid := reply.Object("data").Text("dfid")
	if err != nil || reply.Int("status") != 1 || dfid == "" || dfid == "-" || strings.ContainsAny(dfid, ";\r\n") {
		return "", errors.New("Kugou did not provide a device identity")
	}
	key := sha256.Sum256([]byte(profile.appID + "\x00" + mid + "\x00" + providerhttp.CookieValue(cookie, "userid") + "\x00" + providerhttp.CookieValue(cookie, "token")))
	c.deviceMu.Lock()
	if len(c.devices) >= 128 {
		for candidate := range c.devices {
			delete(c.devices, candidate)
			break
		}
	}
	c.devices[key] = deviceEntry{dfid: dfid, expires: time.Now().Add(6 * time.Hour)}
	c.deviceMu.Unlock()
	return setCookieField(cookie, "dfid", dfid), nil
}
