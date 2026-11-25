package nativeadmin

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/migrate"
	"github.com/nova1751/openmusic-meting-api-go/internal/music"
)

type monitorConfig struct {
	Enabled  bool `json:"enabled"`
	Interval int  `json:"interval"`
}

type monitorStatus struct {
	IsChecking    bool   `json:"isChecking"`
	LastCheckTime *int64 `json:"lastCheckTime"`
	CheckCount    int    `json:"checkCount"`
	ErrorCount    int    `json:"errorCount"`
}

func (a *Admin) monitorConfig() (monitorConfig, error) {
	var settings struct {
		Enabled  bool `json:"monitorEnabled"`
		Interval int  `json:"monitorInterval"`
	}
	err := readFileJSON(filepath.Join(a.dataDir, "config.json"), &settings)
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	if settings.Interval < 5 || settings.Interval > 1440 {
		settings.Interval = 60
	}
	return monitorConfig(settings), err
}

func (a *Admin) RunMonitor(ctx context.Context, logger *slog.Logger) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		config, err := a.monitorConfig()
		if err != nil {
			logger.Warn("cannot read account monitor settings")
			continue
		}
		a.monitorMu.Lock()
		last := a.monitor.LastCheckTime
		a.monitorMu.Unlock()
		if config.Enabled && (last == nil || time.Since(time.UnixMilli(*last)) >= time.Duration(config.Interval)*time.Minute) {
			if err := a.runCheck(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Warn("account monitor check could not complete")
			}
		}
	}
}

func (a *Admin) runCheck(ctx context.Context) error {
	a.monitorMu.Lock()
	if a.monitor.IsChecking {
		a.monitorMu.Unlock()
		return errors.New("a check is already running")
	}
	now := time.Now().UnixMilli()
	a.monitor.IsChecking, a.monitor.LastCheckTime = true, &now
	a.monitor.CheckCount++
	count := a.monitor.CheckCount
	a.monitorMu.Unlock()
	defer func() { a.monitorMu.Lock(); a.monitor.IsChecking = false; a.monitorMu.Unlock() }()

	settings, err := migrate.Config(a.dataDir)
	if err != nil {
		return err
	}
	accounts, err := migrate.List(a.dataDir)
	if err != nil {
		return err
	}
	checked := 0
	failures := 0
	for _, account := range accounts {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !account.IsActive {
			continue
		}
		checked++
		event, err := a.checkStoredAccount(ctx, account)
		if err != nil {
			failures++
			continue
		}
		if event != "" && settings.FMPriority[account.Platform] != account.ID {
			a.monitorLog(map[string]any{"type": event, "cookieId": account.ID, "platform": account.Platform, "note": account.Note})
			config, err := a.webhookConfig()
			if err == nil && config.Enabled && config.URL != "" {
				result := sendWebhook(ctx, config, notification{Title: "音源账号检查", Message: "平台 " + account.Platform + "，状态 " + event, Event: event, Priority: 4})
				a.monitorLog(map[string]any{"type": "webhook_sent", "success": result == nil})
			}
		}
	}
	a.monitorMu.Lock()
	a.monitor.ErrorCount += failures
	a.monitorMu.Unlock()
	a.monitorLog(map[string]any{"type": "check_complete", "totalChecked": checked, "checkCount": count, "errors": failures})
	return nil
}

func (a *Admin) checkStoredAccount(ctx context.Context, account migrate.AccountSummary) (string, error) {
	platform, original, err := migrate.AccountCredential(a.dataDir, a.keyFile, a.keyValue, account.ID)
	if err != nil {
		return "", err
	}
	cookie := original
	profile, err := a.checkAccount(ctx, platform, cookie)
	if platform == "tencent" && a.tencent != nil && (errors.Is(err, music.ErrAccountInvalid) || err == nil && account.IsVIP && !profile.CanPlayVIP) {
		refreshCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		refreshed, refreshErr := a.tencent.Refresh(refreshCtx, cookie)
		cancel()
		if refreshErr == nil {
			cookie = refreshed
			profile, err = a.checkAccount(ctx, platform, cookie)
		}
	}
	change := migrate.AccountChange{}
	event := ""
	if cookie != original {
		change.Cookie = cookie
		event = "cookie_refreshed"
	}
	switch {
	case err == nil:
		change.Profile = &profile
		if event == "" && account.IsVIP && !profile.CanPlayVIP {
			event = "vip_lost"
		}
	case errors.Is(err, music.ErrAccountInvalid):
		change.Invalid = true
		if account.IsValid {
			event = "cookie_invalid"
		}
	case cookie == original:
		return "", err
	}
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	_, current, err := migrate.AccountCredential(a.dataDir, a.keyFile, a.keyValue, account.ID)
	if err != nil || original != current {
		return "", err
	}
	_, err = migrate.SaveAccount(a.dataDir, a.keyFile, a.keyValue, account.ID, change)
	return event, err
}

func (a *Admin) monitorLog(entry map[string]any) {
	id, err := secureRandom(16)
	if err != nil {
		return
	}
	entry["id"], entry["timestamp"] = id, time.Now().UnixMilli()
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	if err := migrate.AppendMonitorLog(a.dataDir, entry); err != nil {
		slog.Warn("cannot persist account monitor result")
	}
}

func (a *Admin) monitorRoutes(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/admin/monitor":
		config, err := a.monitorConfig()
		if !accountError(w, err) {
			reply(w, 200, map[string]any{"success": true, "data": config})
		}
	case r.Method == http.MethodPut && r.URL.Path == "/admin/monitor":
		var input struct {
			Enabled  *bool
			Interval *int
		}
		if decode(r, &input) != nil || input.Interval != nil && (*input.Interval < 5 || *input.Interval > 1440) {
			reject(w, 400, "监测间隔必须在 5 到 1440 分钟之间")
			return
		}
		values := map[string]any{}
		if input.Enabled != nil {
			values["monitorEnabled"] = *input.Enabled
		}
		if input.Interval != nil {
			values["monitorInterval"] = *input.Interval
		}
		a.writeMu.Lock()
		err := migrate.WriteSettings(a.dataDir, values)
		a.writeMu.Unlock()
		if !accountError(w, err) {
			config, err := a.monitorConfig()
			if !accountError(w, err) {
				reply(w, 200, map[string]any{"success": true, "data": config})
			}
		}
	case r.Method == http.MethodGet && r.URL.Path == "/admin/monitor/status":
		a.monitorMu.Lock()
		status := a.monitor
		a.monitorMu.Unlock()
		config, err := a.monitorConfig()
		if !accountError(w, err) {
			reply(w, 200, map[string]any{"success": true, "data": struct {
				monitorStatus
				IsRunning bool `json:"isRunning"`
			}{status, config.Enabled}})
		}
	case r.Method == http.MethodPost && r.URL.Path == "/admin/monitor/check":
		ctx, cancel := context.WithTimeout(r.Context(), 100*time.Second)
		defer cancel()
		if err := a.runCheck(ctx); err != nil {
			reject(w, 409, "监测请求未完成")
			return
		}
		reply(w, 200, map[string]any{"success": true, "message": "检查已完成"})
	case r.Method == http.MethodGet && r.URL.Path == "/admin/monitor/logs":
		a.showLogs(w, r, "monitor_logs.json")
	default:
		reject(w, 404, "接口不存在")
	}
}

func (a *Admin) showLogs(w http.ResponseWriter, r *http.Request, name string) {
	items, err := migrate.ReadLogs(a.dataDir, name)
	if accountError(w, err) {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 {
		limit = 100
	}
	offset = min(max(offset, 0), len(items))
	slices.Reverse(items)
	page := append([]json.RawMessage{}, items[offset:offset+min(len(items)-offset, limit, 500)]...)
	reply(w, 200, map[string]any{"success": true, "data": page})
}
