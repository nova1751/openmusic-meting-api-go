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

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
)

type Config struct {
	Addr             string
	PublicBase       *url.URL
	LegacyURL        *url.URL
	BridgeKey        string
	Token            string
	RequireAssertion bool
	Accounts         []music.Account
	RequestTimeout   time.Duration
	DJPageDelay      time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		Addr: os.Getenv("LISTEN_ADDR"), Token: strings.TrimSpace(os.Getenv("METING_API_TOKEN")),
		BridgeKey: os.Getenv("LEGACY_BRIDGE_KEY"), RequireAssertion: true,
		RequestTimeout: 45 * time.Second, DJPageDelay: 400 * time.Millisecond,
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
	if cfg.BridgeKey != "" && (cfg.LegacyURL == nil || len(cfg.BridgeKey) < 32 ||
		strings.ContainsAny(cfg.BridgeKey, "\r\n") || strings.TrimSpace(cfg.BridgeKey) != cfg.BridgeKey) {
		return cfg, errors.New("LEGACY_BRIDGE_KEY requires LEGACY_URL and at least 32 characters without surrounding whitespace")
	}
	if cfg.LegacyURL == nil && (len(cfg.Token) < 32 || strings.ContainsAny(cfg.Token, " \r\n\t")) {
		return cfg, errors.New("METING_API_TOKEN must have at least 32 non-space characters in standalone mode")
	}
	if value := os.Getenv("OPENMUSIC_AUTH_ENABLED"); value != "" {
		cfg.RequireAssertion, err = strconv.ParseBool(value)
		if err != nil {
			return cfg, errors.New("invalid OPENMUSIC_AUTH_ENABLED")
		}
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
	if cfg.LegacyURL == nil {
		cfg.Accounts, err = accounts(os.Getenv("NETEASE_ACCOUNTS_FILE"))
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

func accounts(path string) ([]music.Account, error) {
	if path == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open NETEASE_ACCOUNTS_FILE: %w", err)
	}
	defer file.Close()
	const limit = 1 << 20
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read NETEASE_ACCOUNTS_FILE: %w", err)
	}
	if len(data) > limit {
		return nil, errors.New("NETEASE_ACCOUNTS_FILE exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var result []music.Account
	if decoder.Decode(&result) != nil || decoder.Decode(new(any)) != io.EOF || result == nil {
		return nil, errors.New("NETEASE_ACCOUNTS_FILE must be a JSON array of accounts")
	}
	seen := make(map[string]bool)
	priority := false
	for i, account := range result {
		cookie := strings.TrimSpace(account.Cookie)
		if cookie == "" || strings.ContainsAny(cookie, "\r\n") || seen[cookie] {
			return nil, fmt.Errorf("invalid or duplicate Cookie in NETEASE_ACCOUNTS_FILE entry %d", i+1)
		}
		switch account.Membership {
		case music.Free, music.VIP, music.SVIP:
		default:
			return nil, fmt.Errorf("invalid membership in NETEASE_ACCOUNTS_FILE entry %d", i+1)
		}
		if account.FMPriority && priority {
			return nil, errors.New("NETEASE_ACCOUNTS_FILE permits one FM priority account")
		}
		priority = priority || account.FMPriority
		seen[cookie] = true
		result[i].Cookie = cookie
	}
	return result, nil
}
