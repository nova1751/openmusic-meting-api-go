package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/auth"
	"github.com/nova1751/openmusic-meting-api-go/internal/config"
	"github.com/nova1751/openmusic-meting-api-go/internal/httpapi"
	"github.com/nova1751/openmusic-meting-api-go/internal/legacy"
	"github.com/nova1751/openmusic-meting-api-go/internal/music"
	"github.com/nova1751/openmusic-meting-api-go/internal/netease"
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
	client := upstreamClient(12*time.Second, true)
	defer client.CloseIdleConnections()
	options := httpapi.Options{
		Verifier: auth.New(cfg.Token, cfg.RequireAssertion),
		Provider: netease.New(client, cfg.DJPageDelay),
		Accounts: music.NewAccountPool(cfg.Accounts), PublicBase: cfg.PublicBase,
		Timeout: cfg.RequestTimeout, Logger: logger,
	}
	mode := "standalone"
	if cfg.LegacyURL != nil {
		compatClient := upstreamClient(0, false)
		defer compatClient.CloseIdleConnections()
		options.Legacy = legacy.New(cfg.LegacyURL, cfg.PublicBase, cfg.BridgeKey, compatClient)
		mode = "compatibility"
	}
	server := &http.Server{
		Addr: cfg.Addr, Handler: httpapi.New(options),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 125 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 << 10,
	}
	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	logger.Info("server listening", "address", listener.Addr().String(), "mode", mode, "version", version)
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	select {
	case err := <-result:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		timeout, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(timeout); err != nil {
			_ = server.Close()
			return fmt.Errorf("shutdown: %w", err)
		}
		if err := <-result; !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	return nil
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
