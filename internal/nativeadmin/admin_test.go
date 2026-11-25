package nativeadmin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nova1751/openmusic-meting-api-go/internal/auth"
	"github.com/nova1751/openmusic-meting-api-go/internal/migrate"
	"github.com/nova1751/openmusic-meting-api-go/internal/netease"
)

func TestMigratedLoginAndTokenLifecycle(t *testing.T) {
	dir := t.TempDir()
	const password = "local-fixture-password"
	const digest = "scrypt:v1:c0ffeeda000102030405060708090a0b:26ef5fa358e08cb2dd3ca904a6c8d9700273dc7f1115eac69e670524bb9633fc"
	if err := os.WriteFile(filepath.Join(dir, "users.json"), []byte(`{"admin":{"password":"`+digest+`","role":"admin"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	admin, err := New(dir, "", "", "https://music.example", netease.New(http.DefaultClient, 0), nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var csrf string
	var session *http.Cookie
	call := func(method, target, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://music.example"+target, strings.NewReader(body))
		req.Header.Set("Origin", "https://music.example")
		req.Header.Set("Content-Type", "application/json")
		if session != nil {
			req.AddCookie(session)
			req.Header.Set("X-CSRF-Token", csrf)
		}
		w := httptest.NewRecorder()
		admin.ServeHTTP(w, req)
		return w
	}
	login := call(http.MethodPost, "/admin/api/login", `{"username":"admin","password":"`+password+`"}`)
	var loginData struct{ CSRF string }
	if login.Code != 200 || json.Unmarshal(login.Body.Bytes(), &loginData) != nil || loginData.CSRF == "" || len(login.Result().Cookies()) != 1 {
		t.Fatalf("login failed: %d %s", login.Code, login.Body)
	}
	csrf, session = loginData.CSRF, login.Result().Cookies()[0]
	created := call(http.MethodPost, "/admin/tokens", `{"name":"local test"}`)
	var token struct{ Data struct{ ID, Token string } }
	if created.Code != 200 || json.Unmarshal(created.Body.Bytes(), &token) != nil || token.Data.Token == "" {
		t.Fatalf("token creation failed: HTTP %d", created.Code)
	}
	verifier := auth.New("", false)
	verifier.SetStoredTokenLookup(func(key string) bool { return migrate.ValidToken(dir, key) })
	req := httptest.NewRequest(http.MethodGet, "https://music.example/test", nil)
	req.Header.Set("Authorization", "Bearer "+token.Data.Token)
	if code := verifier.Verify(req, nil); code != "" {
		t.Fatalf("created token rejected: %s", code)
	}
	list := call(http.MethodGet, "/admin/tokens", "")
	if list.Code != 200 || strings.Contains(list.Body.String(), token.Data.Token) {
		t.Fatal("token listing failed or exposed a credential")
	}
	removed := call(http.MethodDelete, "/admin/tokens/"+token.Data.ID, "")
	if removed.Code != 200 || verifier.Verify(req, nil) != auth.Required {
		t.Fatal("deleted token was not revoked")
	}
}
