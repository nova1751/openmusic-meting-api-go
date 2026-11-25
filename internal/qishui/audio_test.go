package qishui

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func words(values ...uint32) []byte {
	out := make([]byte, len(values)*4)
	for i, value := range values {
		binary.BigEndian.PutUint32(out[i*4:], value)
	}
	return out
}

func TestAudioDecryptAndRange(t *testing.T) {
	key := []byte("synthetic-key-16")
	iv := []byte("nonce-01")
	plain := []byte("audio payload")
	encrypted := bytes.Clone(plain)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	initial := make([]byte, aes.BlockSize)
	copy(initial, iv)
	cipher.NewCTR(block, initial).XORKeyStream(encrypted, encrypted)

	ftyp := boxBytes("ftyp", []byte("isom"))
	mdat := boxBytes("mdat", encrypted)
	stbl := boxBytes("stbl",
		boxBytes("stsd", words(0, 1), boxBytes("enca", make([]byte, 28), boxBytes("sinf"))),
		boxBytes("stsz", words(0, uint32(len(plain)), 1)),
		boxBytes("stsc", words(0, 1, 1, 1, 1)),
		boxBytes("stco", words(0, 1, uint32(len(ftyp)+8))),
		boxBytes("senc", words(0, 1), iv))
	source := bytes.Join([][]byte{ftyp, mdat, boxBytes("moov", boxBytes("trak", boxBytes("mdia", boxBytes("minf", stbl))))}, nil)
	calls := make(chan bool, 2)
	audio := NewAudio(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls <- true
		if r.URL.Hostname() != "fixture.douyinvod.com" {
			t.Errorf("unexpected media host %s", r.URL.Host)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(source))}, nil
	})})
	link := music.Link{URL: "https://fixture.douyinvod.com/audio", PlayAuth: hex.EncodeToString(key)}
	params, err := audio.Issue(link)
	if err != nil || len(params.Get("t")) != 32 || len(params.Get("k")) != 40 {
		t.Fatalf("issuing token: %v %v", params, err)
	}
	target := "/audio/qishui?" + params.Encode()
	resp := httptest.NewRecorder()
	audio.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, target+"&mode=source", nil))
	var metadata map[string]string
	if err := json.Unmarshal(resp.Body.Bytes(), &metadata); err != nil || metadata["auth"] != link.PlayAuth || metadata["url"] != link.URL {
		t.Fatalf("incorrect source metadata: %s", resp.Body)
	}
	resp = httptest.NewRecorder()
	audio.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, target, nil))
	actual := resp.Body.Bytes()
	if resp.Code != 200 || !bytes.Equal(findBox(actual, 0, len(actual), "mdat").data, plain) ||
		bytes.Contains(actual, []byte("senc")) || resp.Header().Get("Content-Type") != "audio/mp4" {
		t.Fatalf("unexpected decrypted media: %d %x", resp.Code, actual)
	}
	expectedTag := resp.Header().Get("ETag")
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("Range", "bytes=-7")
	resp = httptest.NewRecorder()
	audio.ServeHTTP(resp, req)
	if resp.Code != 206 || resp.Body.String() != "payload" || resp.Header().Get("ETag") != expectedTag || len(calls) != 1 {
		t.Fatalf("range or reuse failed: %d %q %v calls=%d", resp.Code, resp.Body.String(), resp.Header(), len(calls))
	}
	if _, err := audio.Issue(music.Link{URL: "https://fixture.douyinvod.com.example/audio"}); err == nil {
		t.Fatal("accepted unapproved CDN host")
	}
	resp = httptest.NewRecorder()
	audio.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/audio/qishui?t=expired", nil))
	if resp.Code != 403 || !strings.Contains(resp.Body.String(), "expired") {
		t.Fatalf("expired token: %d", resp.Code)
	}
}
