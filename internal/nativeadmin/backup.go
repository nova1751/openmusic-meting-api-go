package nativeadmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var backupFiles = []string{
	"cookies.json", "users.json", "api_tokens.json", "config.json", "security.json",
	"logs.json", "api_logs.json", "monitor_logs.json", "go-admin-auth.json",
}

func copyBackupFile(source, destination string, required bool) error {
	info, err := os.Lstat(source)
	if errors.Is(err, os.ErrNotExist) && !required {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 32<<20 {
		return errors.New("invalid backup input")
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(out, io.LimitReader(in, (32<<20)+1))
	if copyErr == nil && n > 32<<20 {
		copyErr = errors.New("backup input grew beyond size limit")
	}
	if copyErr == nil {
		copyErr = out.Sync()
	}
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func (a *Admin) backupEnabled() (bool, error) {
	var config struct {
		BackupEnabled *bool `json:"backupEnabled"`
	}
	err := readFileJSON(filepath.Join(a.dataDir, "config.json"), &config)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return config.BackupEnabled == nil || *config.BackupEnabled, nil
}

func (a *Admin) backup() error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	enabled, err := a.backupEnabled()
	if err != nil || !enabled {
		return err
	}
	// Keep native snapshots outside Node's rotating backup directory so an
	// eventual rollback to Node cannot delete them.
	root := filepath.Join(a.dataDir, "go-native-backups")
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() {
		return errors.New("invalid backup directory")
	}
	stamp := time.Now().Format("2006-01-02T15-04-05.000000000")
	dest := filepath.Join(root, stamp)
	stage, err := os.MkdirTemp(root, ".snapshot-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for _, name := range backupFiles {
		if err := copyBackupFile(filepath.Join(a.dataDir, name), filepath.Join(stage, name),
			name == "cookies.json" || name == "users.json"); err != nil {
			return fmt.Errorf("copy %s: %w", name, err)
		}
	}
	keyFile := a.keyFile
	if keyFile == "" {
		keyFile = filepath.Join(a.dataDir, "cookie-encryption.key")
	}
	if a.keyValue == "" {
		if err := copyBackupFile(keyFile, filepath.Join(stage, "cookie-encryption.key"), true); err != nil {
			return fmt.Errorf("copy cookie encryption key: %w", err)
		}
	}
	manifest, _ := json.Marshal(map[string]any{"createdAt": time.Now().Format(time.RFC3339Nano), "format": "openmusic-go-v1", "keyInEnvironment": a.keyValue != ""})
	if err := os.WriteFile(filepath.Join(stage, "manifest.json"), manifest, 0600); err != nil {
		return err
	}
	if err := os.Rename(stage, dest); err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	var dates []string
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if _, err := time.Parse("2006-01-02T15-04-05.000000000", entry.Name()); err == nil {
			dates = append(dates, entry.Name())
		}
	}
	sort.Strings(dates)
	for len(dates) > 7 {
		if err := os.RemoveAll(filepath.Join(root, dates[0])); err != nil {
			return err
		}
		dates = dates[1:]
	}
	return nil
}

// RunBackups takes a protected snapshot at startup and once per local day.
func (a *Admin) RunBackups(ctx context.Context, logger *slog.Logger) {
	for {
		if err := a.backup(); err != nil {
			logger.Warn("music account backup failed", "error", err)
		}
		now := time.Now()
		next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 5, 0, now.Location())
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
		}
	}
}
