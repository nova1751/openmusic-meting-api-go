package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const key = "test-token-with-32-characters-minimum"

func signed(t testing.TB, method, target, assertion string, body []byte) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	payload := base64.RawURLEncoding.EncodeToString([]byte(assertion))
	hash := sha256.Sum256(body)
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(strings.Join([]string{payload, method, target, hex.EncodeToString(hash[:])}, "\n")))
	r.Header.Set("Authorization", "Bearer "+key)
	r.Header.Set("X-OpenMusic-Assertion", payload)
	r.Header.Set("X-OpenMusic-Signature", base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
	return r
}

func TestVerifyContract(t *testing.T) {
	now := time.Unix(1_890_000_000, 0)
	v := New(key, true)
	v.now = func() time.Time { return now }
	body := []byte(`{"cookie":"input=测试"}`)
	claims := fmt.Sprintf(`{"v":1,"sub":"acct_abcdefghijklmnop","purpose":"account","ts":%d,"nonce":"000000000000000000000001"}`, now.Unix())
	rawTarget := "/admin/fm?extra=%2f+%E4%B8%AD&order=a&order=b"
	r := signed(t, http.MethodPost, rawTarget, claims, body)

	if actual := v.Verify(r, append([]byte(" "), body...)); actual != Required {
		t.Fatalf("body mutation: %q", actual)
	}
	mutated := r.Clone(r.Context())
	mutated.URL.RawQuery = "order=a&order=b&extra=%2F+%E4%B8%AD"
	if actual := v.Verify(mutated, body); actual != Required {
		t.Fatalf("query mutation: %q", actual)
	}
	if actual := v.Verify(r, body); actual != "" {
		t.Fatalf("valid assertion: %q", actual)
	}
	if actual := v.Verify(r, body); actual != Replay {
		t.Fatalf("same nonce: %q", actual)
	}
	now = now.Add(window + time.Millisecond)
	if actual := v.Verify(r, body); actual != Required {
		t.Fatalf("expired assertion: %q", actual)
	}
}

func TestConcurrentReplayAndExpiry(t *testing.T) {
	now := time.Unix(1_890_000_000, 0)
	v := New(key, true)
	v.now = func() time.Time { return now }
	makeClaims := func() string {
		return fmt.Sprintf(`{"v":1,"sub":"openmusic:system","purpose":"system","ts":%d,"nonce":"000000000000000000000001"}`, now.Unix())
	}
	r := signed(t, http.MethodGet, "/api", makeClaims(), nil)
	var success, replay atomic.Int32
	var group sync.WaitGroup
	for range 64 {
		group.Go(func() {
			switch v.Verify(r, nil) {
			case "":
				success.Add(1)
			case Replay:
				replay.Add(1)
			}
		})
	}
	group.Wait()
	if success.Load() != 1 || replay.Load() != 63 {
		t.Fatalf("success: %d, replay: %d", success.Load(), replay.Load())
	}
	now = now.Add(2 * window)
	if got := v.Verify(signed(t, http.MethodGet, "/api", makeClaims(), nil), nil); got != "" {
		t.Fatalf("nonce expiry: %q", got)
	}
}

func TestBearerOnly(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api", nil)
	v := New(key, false)
	if got := v.Verify(request, nil); got != Required {
		t.Fatal(got)
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("X-OpenMusic-Signature", "invalid")
	if got := v.Verify(request, nil); got != "" {
		t.Fatal(got)
	}
}
