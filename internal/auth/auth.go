package auth

import (
	"container/list"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	Required = "OPENMUSIC_AUTH_REQUIRED"
	Replay   = "OPENMUSIC_REPLAY"
	Busy     = "OPENMUSIC_AUTH_BUSY"

	window    = 60 * time.Second
	maxNonces = 100_000
)

var (
	accountID = regexp.MustCompile(`^acct_[A-Za-z0-9_-]{16,64}$`)
	nonce     = regexp.MustCompile(`^[A-Za-z0-9_-]{24}$`)
)

type claims struct {
	Version int    `json:"v"`
	Subject string `json:"sub"`
	Purpose string `json:"purpose"`
	Time    int64  `json:"ts"`
	Nonce   string `json:"nonce"`
}

type entry struct {
	key     [sha256.Size]byte
	expires time.Time
}

// Verifier implements the OpenMusic server-to-server contract for one server process.
type Verifier struct {
	requireAssertion bool
	tokenHash        [sha256.Size]byte
	now              func() time.Time

	mu    sync.Mutex
	seen  map[[sha256.Size]byte]struct{}
	order list.List
}

func New(token string, requireAssertion bool) *Verifier {
	return &Verifier{
		requireAssertion: requireAssertion,
		tokenHash:        sha256.Sum256([]byte(token)),
		now:              time.Now,
		seen:             make(map[[sha256.Size]byte]struct{}),
	}
}

func bearer(r *http.Request) string {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return ""
	}
	return strings.TrimSpace(token)
}

// Preflight checks the bearer and required headers before a handler reads its body.
func (v *Verifier) Preflight(r *http.Request) bool {
	token := bearer(r)
	digest := sha256.Sum256([]byte(token))
	return token != "" && subtle.ConstantTimeCompare(digest[:], v.tokenHash[:]) == 1 &&
		(!v.requireAssertion || (r.Header.Get("X-OpenMusic-Assertion") != "" &&
			r.Header.Get("X-OpenMusic-Signature") != ""))
}

func (v *Verifier) Verify(r *http.Request, body []byte) string {
	if !v.Preflight(r) {
		return Required
	}
	if !v.requireAssertion {
		return ""
	}
	encoded := r.Header.Get("X-OpenMusic-Assertion")
	if len(encoded) > 768 || strings.ContainsAny(encoded, "\r\n") {
		return Required
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return Required
	}
	var c claims
	if json.Unmarshal(data, &c) != nil || c.Version != 1 || !nonce.MatchString(c.Nonce) {
		return Required
	}
	if !(c.Purpose == "account" && accountID.MatchString(c.Subject)) &&
		!(c.Purpose == "system" && c.Subject == "openmusic:system") {
		return Required
	}
	if c.Time < -9007199254740991 || c.Time > 9007199254740991 {
		return Required
	}
	signature := r.Header.Get("X-OpenMusic-Signature")
	if len(signature) != 43 {
		return Required
	}
	actual, err := base64.RawURLEncoding.Strict().DecodeString(signature)
	if err != nil {
		return Required
	}
	target := r.URL.EscapedPath()
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	digest := sha256.Sum256(body)
	token := bearer(r)
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte(strings.Join([]string{
		encoded, strings.ToUpper(r.Method), target, hex.EncodeToString(digest[:]),
	}, "\n")))
	if !hmac.Equal(actual, mac.Sum(nil)) {
		return Required
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	now := v.now()
	timestamp := time.Unix(c.Time, 0)
	if timestamp.Before(now.Add(-window)) || timestamp.After(now.Add(window)) {
		return Required
	}
	for front := v.order.Front(); front != nil; front = v.order.Front() {
		old := front.Value.(entry)
		if old.expires.After(now) {
			break
		}
		delete(v.seen, old.key)
		v.order.Remove(front)
	}
	key := sha256.Sum256([]byte(token + ":" + c.Nonce))
	if _, found := v.seen[key]; found {
		return Replay
	}
	if len(v.seen) >= maxNonces {
		return Busy
	}
	v.seen[key] = struct{}{}
	v.order.PushBack(entry{key: key, expires: now.Add(window * 2)})
	return ""
}
