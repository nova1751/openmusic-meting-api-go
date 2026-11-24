package netease

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"net/url"
	"slices"
	"strings"
)

// These constants and algorithms are part of NetEase's upstream request format.
const webPublicKey = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDgtQn2JZ34ZC28NWYpAUd98iZ3
7BUrX/aKzmFbt7clFSs6sXqHauqKWqdtLkF2KexO40H1YTX8z2lSgBBOAxLsvakl
V8k4cBFK9snQXE9/DDaFt6Rr7iVZMldczhC0JNgTz+SHXT6CBHuX3e9SdB1Ua44o
ncaTWz7OBGLbCiK45wIDAQAB
-----END PUBLIC KEY-----`

var webRSA = func() *rsa.PublicKey {
	block, _ := pem.Decode([]byte(webPublicKey))
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		panic(err)
	}
	return key.(*rsa.PublicKey)
}()

func pad(data []byte) []byte {
	size := aes.BlockSize - len(data)%aes.BlockSize
	return append(data, bytes.Repeat([]byte{byte(size)}, size)...)
}

func cbc(data, key []byte) []byte {
	block, _ := aes.NewCipher(key)
	data = pad(data)
	cipher.NewCBCEncrypter(block, []byte("0102030405060708")).CryptBlocks(data, data)
	return data
}

func webForm(data []byte) (url.Values, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	key := make([]byte, 16)
	for i := range key {
		index, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return nil, err
		}
		key[i] = alphabet[index.Int64()]
	}
	first := base64.StdEncoding.EncodeToString(cbc(data, []byte("0CoJUm6Qyw8W8jud")))
	params := base64.StdEncoding.EncodeToString(cbc([]byte(first), key))
	slices.Reverse(key)
	// WEAPI uses a raw RSA exponentiation over the reversed per-request key.
	encoded := new(big.Int).Exp(new(big.Int).SetBytes(key), big.NewInt(int64(webRSA.E)), webRSA.N)
	return url.Values{
		"params":    {params},
		"encSecKey": {hex.EncodeToString(encoded.FillBytes(make([]byte, webRSA.Size())))},
	}, nil
}

func enhancedForm(path string, data []byte) url.Values {
	digest := md5.Sum([]byte("nobody" + path + "use" + string(data) + "md5forencrypt"))
	payload := path + "-36cd479b6b5-" + string(data) + "-36cd479b6b5-" + hex.EncodeToString(digest[:])
	block, _ := aes.NewCipher([]byte("e82ckenh8dichen8"))
	buf := pad([]byte(payload))
	for i := 0; i < len(buf); i += aes.BlockSize {
		block.Encrypt(buf[i:i+aes.BlockSize], buf[i:i+aes.BlockSize])
	}
	return url.Values{"params": {strings.ToUpper(hex.EncodeToString(buf))}}
}
