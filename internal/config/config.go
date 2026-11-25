package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/migrate"
	"github.com/nova1751/openmusic-meting-api-go/internal/music"
)

type Config struct {
	Addr               string
	PublicBase         *url.URL
	LegacyURL          *url.URL
	BridgeKey          string
	Token              string
	RequireAssertion   bool
	StoredAuthSetting  bool
	Accounts           map[string][]music.Account
	QishuiSigner       string
	QishuiAssetsDir    string
	QishuiChromiumPath string
	RequestTimeout     time.Duration
	DJPageDelay        time.Duration
	SiteGatewayConfig  string
	SiteGatewayAddr    string
	SiteGatewaySecret  string
	NodeDataDir        string
	NodeTokenName      string
	CookieKeyFile      string
	CookieKeyValue     string
	NativeAdmin        bool
}

func Load() (Config, error) {
	cfg := Config{
		Addr: os.Getenv("LISTEN_ADDR"), Token: strings.TrimSpace(os.Getenv("METING_API_TOKEN")),
		BridgeKey: os.Getenv("LEGACY_BRIDGE_KEY"), RequireAssertion: true,
		RequestTimeout: 45 * time.Second, DJPageDelay: 400 * time.Millisecond,
		SiteGatewayConfig: os.Getenv("METING_SITE_GATEWAY_CONFIG"),
		SiteGatewayAddr:   os.Getenv("METING_SITE_GATEWAY_ADDR"),
		SiteGatewaySecret: os.Getenv("MUSIC_ORIGIN_SECRET"),
		NodeDataDir:       os.Getenv("METING_NODE_DATA_DIR"), CookieKeyFile: os.Getenv("METING_COOKIE_KEY_FILE"),
		CookieKeyValue:     os.Getenv("METING_COOKIE_ENCRYPTION_KEY"),
		QishuiAssetsDir:    os.Getenv("QISHUI_SECURITY_ASSETS_DIR"),
		QishuiChromiumPath: os.Getenv("QISHUI_CHROMIUM_PATH"),
		QishuiSigner:       strings.TrimSpace(os.Getenv("QISHUI_SIGNER_URL")),
	}
	if cfg.Addr == "" {
		port := os.Getenv("PORT")
		if port == "" {
			port = "3000"
		}
		cfg.Addr = net.JoinHostPort("127.0.0.1", port)
	}
	var err error
	cfg.PublicBase, err = parseURL("PUBLIC_BASE_URL", true)
	if err != nil {
		return cfg, err
	}
	cfg.LegacyURL, err = parseURL("LEGACY_URL", false)
	if err != nil {
		return cfg, err
	}
	if cfg.LegacyURL == nil && cfg.Token == "" && cfg.NodeDataDir != "" && os.Getenv("METING_NODE_TOKEN_NAME") != "" {
		cfg.NodeTokenName = os.Getenv("METING_NODE_TOKEN_NAME")
		cfg.Token, err = migrate.Token(cfg.NodeDataDir, cfg.NodeTokenName)
		if err != nil {
			return cfg, err
		}
	}
	if cfg.BridgeKey != "" && (cfg.LegacyURL == nil || len(cfg.BridgeKey) < 32 ||
		strings.ContainsAny(cfg.BridgeKey, "\r\n") || strings.TrimSpace(cfg.BridgeKey) != cfg.BridgeKey) {
		return cfg, errors.New("LEGACY_BRIDGE_KEY requires LEGACY_URL and at least 32 characters without surrounding whitespace")
	}
	if cfg.LegacyURL == nil {
		validToken := len(cfg.Token) >= 32 && !strings.ContainsAny(cfg.Token, " \r\n\t")
		if !validToken && cfg.Token == "" && cfg.NodeDataDir != "" {
			validToken, err = migrate.HasTokens(cfg.NodeDataDir)
			if err != nil {
				return cfg, err
			}
		}
		if !validToken {
			return cfg, errors.New("standalone mode requires METING_API_TOKEN or an existing stored API token")
		}
	}
	if value := os.Getenv("OPENMUSIC_AUTH_ENABLED"); value != "" {
		cfg.RequireAssertion, err = strconv.ParseBool(value)
		if err != nil {
			return cfg, errors.New("invalid OPENMUSIC_AUTH_ENABLED")
		}
	} else {
		cfg.StoredAuthSetting = cfg.NodeDataDir != ""
	}
	if value := os.Getenv("METING_NATIVE_ADMIN"); value != "" {
		cfg.NativeAdmin, err = strconv.ParseBool(value)
		if err != nil {
			return cfg, errors.New("invalid METING_NATIVE_ADMIN")
		}
	}
	if cfg.NativeAdmin && (cfg.NodeDataDir == "" || cfg.LegacyURL != nil || cfg.PublicBase == nil) {
		return cfg, errors.New("native admin requires METING_NODE_DATA_DIR, PUBLIC_BASE_URL and standalone mode")
	}
	if value := os.Getenv("REQUEST_TIMEOUT"); value != "" {
		cfg.RequestTimeout, err = time.ParseDuration(value)
		if err != nil || cfg.RequestTimeout < time.Second || cfg.RequestTimeout > 2*time.Minute {
			return cfg, errors.New("REQUEST_TIMEOUT must be a Go duration between 1s and 2m")
		}
	}
	if value := os.Getenv("NETEASE_DJ_PAGE_DELAY_MS"); value != "" {
		ms, err := strconv.Atoi(value)
		if err != nil || ms < 0 || ms > 10_000 {
			return cfg, errors.New("NETEASE_DJ_PAGE_DELAY_MS must be an integer between 0 and 10000")
		}
		cfg.DJPageDelay = time.Duration(ms) * time.Millisecond
	}
	if cfg.LegacyURL != nil && cfg.NodeDataDir != "" {
		return cfg, errors.New("METING_NODE_DATA_DIR is only available in standalone mode")
	}
	if cfg.LegacyURL == nil {
		cfg.Accounts = make(map[string][]music.Account)
		for _, platform := range []string{"netease", "tencent", "kugou", "qishui"} {
			name := strings.ToUpper(platform) + "_ACCOUNTS_FILE"
			path := os.Getenv(name)
			if cfg.NodeDataDir != "" && path != "" {
				return cfg, errors.New("METING_NODE_DATA_DIR cannot be combined with separate account files")
			}
			cfg.Accounts[platform], err = accounts(path, name)
			if err != nil {
				return cfg, err
			}
		}
		if cfg.NodeDataDir != "" {
			cfg.Accounts, err = migrate.Accounts(cfg.NodeDataDir, cfg.CookieKeyFile, cfg.CookieKeyValue)
		}
	}
	return cfg, err
}

func parseURL(variable string, allowPrefix bool) (*url.URL, error) {
	raw := strings.TrimSpace(os.Getenv(variable))
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") ||
		(!allowPrefix && u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("invalid %s", variable)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = strings.TrimRight(u.RawPath, "/")
	return u, nil
}

func accounts(path, name string) ([]music.Account, error) {
	if path == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	defer file.Close()
	const limit = 1 << 20
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if len(data) > limit {
		return nil, fmt.Errorf("%s exceeds 1 MiB", name)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var result []music.Account
	if decoder.Decode(&result) != nil || decoder.Decode(new(any)) != io.EOF || result == nil {
		return nil, fmt.Errorf("%s must be a JSON array of accounts", name)
	}
	seen := make(map[string]bool)
	priority := false
	for i, account := range result {
		cookie := strings.TrimSpace(account.Cookie)
		if cookie == "" || strings.ContainsAny(cookie, "\r\n") || seen[cookie] {
			return nil, fmt.Errorf("invalid or duplicate Cookie in %s entry %d", name, i+1)
		}
		switch account.Membership {
		case music.Free, music.VIP, music.SVIP:
		default:
			return nil, fmt.Errorf("invalid membership in %s entry %d", name, i+1)
		}
		if account.FMPriority && priority {
			return nil, fmt.Errorf("%s permits one FM priority account", name)
		}
		priority = priority || account.FMPriority
		seen[cookie] = true
		result[i].Cookie = cookie
	}
	return result, nil
}
