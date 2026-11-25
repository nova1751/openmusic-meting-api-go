package nativeadmin

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/httpapi"
	"github.com/nova1751/openmusic-meting-api-go/internal/migrate"
)

func (a *Admin) Observe(request httpapi.RequestObservation) {
	entry, err := json.Marshal(map[string]any{
		"timestamp": time.Now().UnixMilli(), "method": request.Method, "path": request.Path,
		"platform": request.Platform, "type": request.Operation, "status": request.Status, "duration": request.Duration.Milliseconds(),
	})
	if err != nil {
		return
	}
	a.logMu.Lock()
	defer a.logMu.Unlock()
	a.requestLogs = append(a.requestLogs, entry)
	if len(a.requestLogs) > 1500 {
		a.requestLogs = slices.Clone(a.requestLogs[500:])
	}
}

func (a *Admin) FlushLogs() {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	a.logMu.Lock()
	batch := a.requestLogs
	a.requestLogs = nil
	a.logMu.Unlock()
	if len(batch) == 0 {
		return
	}
	if err := migrate.AppendLogs(a.dataDir, "api_logs.json", batch); err != nil {
		slog.Warn("unable to persist recent API logs")
		a.logMu.Lock()
		a.requestLogs = append(batch, a.requestLogs...)
		a.requestLogs = a.requestLogs[max(0, len(a.requestLogs)-1500):]
		a.logMu.Unlock()
	}
}

func (a *Admin) RunLogFlush(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.FlushLogs()
		}
	}
}
