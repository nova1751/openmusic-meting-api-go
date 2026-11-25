package tencent

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/providerhttp"
)

const (
	qimeiPublicKey = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDEIxgwoutfwoJxcGQeedgP7FG9qaIuS0qzfR8gWkrkTZKM2iWHn2ajQpBRZjMSoSf6+KJGvar2ORhBfpDXyVtZCKpqLQ+FLkpncClKVIrBwv6PHyUvuCb0rIarmgDnzkfQAqVufEtR64iazGDKatvJ9y6B9NMbHddGSAUmRTCrHQIDAQAB
-----END PUBLIC KEY-----`
	qimeiAppKey = "0AND0HD6FE4HY80F"
	qimeiSecret = "ZdJqM15EeO2zWc08"
)

func randomHex(size int) string {
	data := make([]byte, size)
	rand.Read(data)
	return hex.EncodeToString(data)
}

func randomInt(max int64) int64 {
	number, err := rand.Int(rand.Reader, big.NewInt(max))
	if err != nil {
		panic(err)
	}
	return number.Int64()
}

func md5hex(value string) string {
	sum := md5.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

func beaconID(now time.Time) string {
	monthly := fmt.Sprintf("%s%s.%d", now.Format("2006-01")+"-01", strconv.FormatInt(100000+randomInt(900000), 10), 100000000+randomInt(900000000))
	var text strings.Builder
	for i := 1; i <= 40; i++ {
		value := ""
		switch i {
		case 1, 2, 13, 14, 17, 18, 21, 22, 25, 26, 29, 30, 33, 34, 37, 38:
			value = monthly
		case 3:
			value = "0000000000000000"
		case 4:
			value = strings.ReplaceAll(randomHex(8), "0", "1")
		default:
			value = strconv.FormatInt(randomInt(10000), 10)
		}
		fmt.Fprintf(&text, "k%d:%s;", i, value)
	}
	return text.String()
}

func (c *Client) registerLoginDevice(ctx context.Context) (loginDevice, error) {
	now := time.Now().UTC()
	device := loginDevice{GUID: randomHex(16), AndroidID: randomHex(8), Release: "10", SDK: "29", Model: "MI 6",
		Fingerprint: fmt.Sprintf("xiaomi/iarim/sagit:10/eomam.200122.001/%d:user/release-keys", 1000000+randomInt(9000000))}
	reserved, err := json.Marshal(map[string]string{
		"harmony": "0", "clone": "0", "containe": "", "oz": "UhYmelwouA+V2nPWbOvLTgN2/m8jwGB+yUB5v9tysQg=",
		"oo": "Xecjt+9S1+f8Pz2VLSxgpw==", "kelong": "0", "uptimes": now.Add(-time.Duration(randomInt(14401)) * time.Second).Format("2006-01-02 15:04:05"),
		"multiUser": "0", "bod": "Xiaomi", "dv": "sagit", "firstLevel": "", "manufact": "Xiaomi", "name": device.Model,
		"host": "se.infra", "kernel": "Linux 5.4.0-54-generic-" + randomHex(4) + " (android-build@google.com)",
	})
	if err != nil {
		return loginDevice{}, err
	}
	profile, err := json.Marshal(map[string]any{
		"androidId": device.AndroidID, "platformId": 1, "appKey": qimeiAppKey, "appVersion": "14.9.0.8", "beaconIdSrc": beaconID(now),
		"brand": "Xiaomi", "channelId": "10003505", "cid": "", "imei": fmt.Sprintf("%015d", randomInt(1000000000000000)),
		"imsi": "", "mac": "", "model": device.Model, "networkType": "unknown", "oaid": "", "osVersion": "Android 10,level 29",
		"qimei": "", "qimei36": "", "sdkVersion": "1.2.13.6", "targetSdkVersion": "33", "audit": "", "userId": "{}",
		"packageId": "com.tencent.qqmusic", "deviceType": "Phone", "sdkName": "", "reserved": string(reserved),
	})
	if err != nil {
		return loginDevice{}, err
	}
	key := []byte(randomHex(8))
	public, _ := pem.Decode([]byte(qimeiPublicKey))
	parsed, err := x509.ParsePKIXPublicKey(public.Bytes)
	if err != nil {
		return loginDevice{}, err
	}
	rsaKey, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return loginDevice{}, errors.New("invalid QQ equipment key")
	}
	wrapped, err := rsa.EncryptPKCS1v15(rand.Reader, rsaKey, key)
	if err != nil {
		return loginDevice{}, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return loginDevice{}, err
	}
	padding := aes.BlockSize - len(profile)%aes.BlockSize
	profile = append(profile, bytes.Repeat([]byte{byte(padding)}, padding)...)
	cipher.NewCBCEncrypter(block, key).CryptBlocks(profile, profile)
	sealed := base64.StdEncoding.EncodeToString(profile)
	encryptedKey := base64.StdEncoding.EncodeToString(wrapped)
	nonce := randomHex(8)
	sec := strconv.FormatInt(now.Unix(), 10)
	extra := `{"appKey":"` + qimeiAppKey + `"}`
	payload := map[string]any{"app": 0, "os": 1, "qimeiParams": map[string]string{
		"key": encryptedKey, "params": sealed, "time": sec, "nonce": nonce, "extra": extra,
		"sign": md5hex(encryptedKey + sealed + strconv.FormatInt(now.Unix()*1000, 10) + nonce + qimeiSecret + extra),
	}}
	headers := http.Header{"Method": {"GetQimei"}, "Service": {"trpc.tme_datasvr.qimeiproxy.QimeiProxy"},
		"Appid": {"qimei_qq_android"}, "Timestamp": {sec}, "Sign": {md5hex("qimei_qq_androidpzAuCmaFAaFaHrdakPjLIEqKrGnSOOvH" + sec)},
		"User-Agent": {"QQMusic"}}
	var result struct{ Data string }
	if err := providerhttp.JSON(ctx, c.http, http.MethodPost, "https://api.tencentmusic.com/tme/trpc/proxy", headers, payload, &result); err != nil {
		return loginDevice{}, err
	}
	var response struct{ Data struct{ Q16, Q36 string } }
	if json.Unmarshal([]byte(result.Data), &response) != nil || response.Data.Q16 == "" || response.Data.Q36 == "" {
		return loginDevice{}, errors.New("QQ Music did not provide a device identity")
	}
	device.Q16, device.Q36 = response.Data.Q16, response.Data.Q36
	return device, nil
}
