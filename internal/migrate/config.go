package migrate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
)

type Settings struct {
	AdminPath          string            `json:"adminPath"`
	LoudnessServiceURL string            `json:"loudnessServiceUrl"`
	OpenMusicAuth      bool              `json:"openmusicAuthEnabled"`
	BackupEnabled      bool              `json:"backupEnabled"`
	QishuiSignerURL    string            `json:"qishuiSignerUrl"`
	FMPriority         map[string]string `json:"fmPriorityCookieIds"`
}

func Config(dataDir string) (Settings, error) {
	settings := Settings{OpenMusicAuth: true, BackupEnabled: true, FMPriority: map[string]string{}}
	err := readJSON(filepath.Join(dataDir, "config.json"), &settings)
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return settings, err
}

func QishuiSigner(dataDir string) string {
	settings, err := Config(dataDir)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(settings.QishuiSignerURL)
}

func WriteSetting(dataDir, field string, value any) error {
	return WriteSettings(dataDir, map[string]any{field: value})
}

func WriteSettings(dataDir string, updates map[string]any) error {
	path := filepath.Join(dataDir, "config.json")
	settings := make(map[string]json.RawMessage)
	if err := readJSON(path, &settings); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if settings == nil {
		return errors.New("invalid Node settings")
	}
	for field, value := range updates {
		if err := putJSON(settings, field, value); err != nil {
			return err
		}
	}
	return atomicJSON(path, settings)
}

func SetFMPriority(dataDir, platform, id string) error {
	if !music.KnownPlatform(platform) {
		return errors.New("unknown platform")
	}
	settings, err := Config(dataDir)
	if err != nil {
		return err
	}
	if settings.FMPriority == nil {
		settings.FMPriority = make(map[string]string)
	}
	if id == "" {
		delete(settings.FMPriority, platform)
	} else {
		items, err := List(dataDir)
		if err != nil {
			return err
		}
		found := false
		for _, item := range items {
			if item.ID == id && item.Platform == platform && item.IsActive && item.IsValid {
				found = true
				break
			}
		}
		if !found {
			return os.ErrNotExist
		}
		settings.FMPriority[platform] = id
	}
	return WriteSetting(dataDir, "fmPriorityCookieIds", settings.FMPriority)
}
