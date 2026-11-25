package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/auth"
	"github.com/nova1751/openmusic-meting-api-go/internal/config"
	"github.com/nova1751/openmusic-meting-api-go/internal/httpapi"
	"github.com/nova1751/openmusic-meting-api-go/internal/kugou"
	"github.com/nova1751/openmusic-meting-api-go/internal/legacy"
	"github.com/nova1751/openmusic-meting-api-go/internal/migrate"
	"github.com/nova1751/openmusic-meting-api-go/internal/music"
	"github.com/nova1751/openmusic-meting-api-go/internal/nativeadmin"
	"github.com/nova1751/openmusic-meting-api-go/internal/netease"
	"github.com/nova1751/openmusic-meting-api-go/internal/qishui"
	"github.com/nova1751/openmusic-meting-api-go/internal/sitegateway"
	"github.com/nova1751/openmusic-meting-api-go/internal/tencent"
)

var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print the Meting version")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, logger); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	var siteConfig sitegateway.Config
	if cfg.SiteGatewayConfig != "" {
		file, err := os.Open(cfg.SiteGatewayConfig)
		if err != nil {
			return fmt.Errorf("open site gateway config: %w", err)
		}
		decoder := json.NewDecoder(io.LimitReader(file, 64<<10))
		decoder.DisallowUnknownFields()
		err = decoder.Decode(&siteConfig)
		if err == nil && decoder.Decode(new(any)) != io.EOF {
			err = errors.New("trailing site gateway config data")
		}
		_ = file.Close()
		if err != nil {
			return fmt.Errorf("invalid site gateway config: %w", err)
		}
		if cfg.PublicBase == nil || strings.TrimRight(cfg.PublicBase.String(), "/") != strings.TrimRight(siteConfig.PublicBase, "/") {
			return errors.New("PUBLIC_BASE_URL must match the site gateway public base")
		}
	}
	client := upstreamClient(12*time.Second, true)
	defer client.CloseIdleConnections()
	neteaseClient := netease.New(client, cfg.DJPageDelay)
	signer := func() string {
		if cfg.NodeDataDir != "" {
			if value := migrate.QishuiSigner(cfg.NodeDataDir); value != "" {
				return value
			}
		}
		return cfg.QishuiSigner
	}
	audioClient := upstreamClient(60*time.Second, true)
	defer audioClient.CloseIdleConnections()
	loudnessURL := func() string {
		if cfg.NodeDataDir != "" {
			if settings, err := migrate.Config(cfg.NodeDataDir); err == nil && strings.TrimSpace(settings.LoudnessServiceURL) != "" {
				return settings.LoudnessServiceURL
			}
		}
		return strings.TrimSpace(os.Getenv("LOUDNESS_SERVICE_URL"))
	}
	media := qishui.NewAudio(audioClient)
	if cfg.NodeDataDir != "" {
		if err := media.EnableDiskCache(filepath.Join(cfg.NodeDataDir, "go-qishui-audio-cache")); err != nil {
			logger.Warn("Qishui media disk cache could not be enabled")
		}
	}
	options := httpapi.Options{
		Verifier:    auth.New(cfg.Token, cfg.RequireAssertion),
		LoudnessURL: loudnessURL, LoudnessClient: client,
		Sources:    map[string]httpapi.Source{},
		Audio:      media,
		PublicBase: cfg.PublicBase,
		Timeout:    cfg.RequestTimeout, Logger: logger,
	}
	qqClient, kgClient, qsClient := tencent.New(client), kugou.New(client), qishui.New(client, signer)
	for platform, provider := range map[string]music.Provider{
		"netease": neteaseClient,
		"tencent": qqClient,
		"kugou":   kgClient,
		"qishui":  qsClient,
	} {
		options.Sources[platform] = httpapi.Source{Provider: provider, Accounts: music.NewAccountPool(cfg.Accounts[platform])}
	}
	if cfg.NodeTokenName != "" {
		expected := sha256.Sum256([]byte(cfg.Token))
		options.Verifier.SetTokenValidity(func() bool {
			current, err := migrate.Token(cfg.NodeDataDir, cfg.NodeTokenName)
			if err != nil {
				return false
			}
			digest := sha256.Sum256([]byte(current))
			return subtle.ConstantTimeCompare(expected[:], digest[:]) == 1
		})
	}
	if cfg.NodeDataDir != "" && cfg.NodeTokenName == "" {
		options.Verifier.SetStoredTokenLookup(func(token string) bool { return migrate.ValidToken(cfg.NodeDataDir, token) })
	}
	if cfg.StoredAuthSetting {
		options.Verifier.SetAssertionSetting(func() bool {
			settings, err := migrate.Config(cfg.NodeDataDir)
			return err != nil || settings.OpenMusicAuth
		})
	}
	var nativeManager *nativeadmin.Admin
	if cfg.NativeAdmin {
		host, _, err := net.SplitHostPort(cfg.Addr)
		if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
			return errors.New("native admin requires a loopback API listener")
		}
		if err := nativeadmin.Initialize(cfg.NodeDataDir, os.Getenv("ADMIN_USERNAME"), os.Getenv("ADMIN_PASSWORD")); err != nil {
			return fmt.Errorf("initialize native administrator: %w", err)
		}
		if err := migrate.InitializeCookieKey(cfg.NodeDataDir, cfg.CookieKeyFile, cfg.CookieKeyValue); err != nil {
			return fmt.Errorf("initialize account encryption: %w", err)
		}
		var qishuiQR *qishui.QRBrowser
		if cfg.QishuiAssetsDir != "" {
			qishuiQR, err = qishui.NewQRBrowser(ctx, cfg.QishuiAssetsDir, cfg.QishuiChromiumPath)
			if err != nil {
				return fmt.Errorf("initialize Qishui scanner: %w", err)
			}
		}
		nativeManager, err = nativeadmin.New(cfg.NodeDataDir, cfg.CookieKeyFile, cfg.CookieKeyValue, cfg.PublicBase.String(), neteaseClient, kgClient, qqClient, qishuiQR, map[string]music.AccountValidator{
			"tencent": qqClient.Account, "kugou": kgClient.Account, "qishui": qsClient.Account,
		})
		if err != nil {
			return fmt.Errorf("initialize native admin: %w", err)
		}
		nativeManager.SetQQVerification(tencent.NewVerificationBrowser(ctx, cfg.QishuiChromiumPath))
		options.Admin = nativeManager
		options.Observe = nativeManager.Observe
		options.AdminPage = nativeManager.PagePath
		options.Contributions = nativeManager.Contributions
	}
	if cfg.LegacyURL == nil && cfg.NodeDataDir != "" {
		accountLoader := func(platform string) func() ([]music.Account, error) {
			return func() ([]music.Account, error) {
				accounts, err := migrate.Accounts(cfg.NodeDataDir, cfg.CookieKeyFile, cfg.CookieKeyValue)
				if err != nil {
					return nil, err
				}
				return accounts[platform], nil
			}
		}
		for platform, source := range options.Sources {
			source.Accounts.SetLoader(accountLoader(platform))
		}
	}
	mode := "standalone"
	if cfg.LegacyURL != nil {
		compatClient := upstreamClient(0, false)
		defer compatClient.CloseIdleConnections()
		options.Legacy = legacy.New(cfg.LegacyURL, cfg.PublicBase, cfg.BridgeKey, compatClient)
		mode = "compatibility"
	}
	api := httpapi.New(options)
	server := &http.Server{
		Addr: cfg.Addr, Handler: api,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 125 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 << 10,
	}
	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()
	servers := []*http.Server{server}
	listeners := []net.Listener{listener}
	if cfg.SiteGatewayConfig != "" {
		gateway, err := sitegateway.New(api, cfg.Token, cfg.SiteGatewaySecret, siteConfig)
		if err != nil {
			return err
		}
		addr := cfg.SiteGatewayAddr
		if addr == "" {
			addr = "127.0.0.1:3358"
		}
		host, _, err := net.SplitHostPort(addr)
		if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
			return errors.New("site gateway must listen on a loopback IP")
		}
		siteListener, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("listen site gateway: %w", err)
		}
		defer siteListener.Close()
		servers = append(servers, &http.Server{
			Addr: addr, Handler: gateway, ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second,
			IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10,
		})
		listeners = append(listeners, siteListener)
	}
	logger.Info("server listening", "address", listener.Addr().String(), "mode", mode, "version", version)
	if nativeManager != nil {
		go nativeManager.RunBackups(ctx, logger)
		go nativeManager.RunMonitor(ctx, logger)
		go nativeManager.RunLogFlush(ctx)
	}
	result := make(chan error, len(servers))
	for i := range servers {
		go func(server *http.Server, listener net.Listener) { result <- server.Serve(listener) }(servers[i], listeners[i])
	}
	var runErr error
	select {
	case err := <-result:
		if !errors.Is(err, http.ErrServerClosed) {
			runErr = err
		}
	case <-ctx.Done():
	}
	timeout, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, server := range servers {
		if err := server.Shutdown(timeout); err != nil {
			_ = server.Close()
			if runErr == nil {
				runErr = fmt.Errorf("shutdown: %w", err)
			}
		}
	}
	if nativeManager != nil {
		nativeManager.FlushLogs()
	}
	return runErr
}

func upstreamClient(timeout time.Duration, useEnvProxy bool) *http.Client {
	transport := &http.Transport{
		DialContext:       (&net.Dialer{Timeout: 4 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2: true, MaxIdleConns: 64, MaxIdleConnsPerHost: 16, MaxConnsPerHost: 50,
		IdleConnTimeout: 60 * time.Second, TLSHandshakeTimeout: 5 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second, ExpectContinueTimeout: time.Second,
	}
	if useEnvProxy {
		transport.Proxy = http.ProxyFromEnvironment
	}
	return &http.Client{Transport: transport, Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
