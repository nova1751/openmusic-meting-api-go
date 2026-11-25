package nativeadmin

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/migrate"
)

func (a *Admin) tokenRoutes(w http.ResponseWriter, r *http.Request, s *session) {
	id := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/admin/tokens"), "/")
	if strings.Contains(id, "/") {
		reject(w, 404, "接口不存在")
		return
	}
	if r.Method == http.MethodGet {
		tokens, err := migrate.ListTokens(a.dataDir)
		if accountError(w, err) {
			return
		}
		if id != "" {
			if tokens[id] == nil {
				reject(w, 404, "Token 不存在")
				return
			}
			reply(w, 200, map[string]any{"success": true, "data": tokens[id]})
			return
		}
		data := make([]map[string]json.RawMessage, 0, len(tokens))
		for _, token := range tokens {
			data = append(data, token)
		}
		slices.SortFunc(data, func(a, b map[string]json.RawMessage) int {
			return strings.Compare(string(b["createdAt"]), string(a["createdAt"]))
		})
		reply(w, 200, map[string]any{"success": true, "data": data})
		return
	}
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	if r.Method == http.MethodDelete && id != "" {
		_, err := migrate.WriteToken(a.dataDir, id, nil, false)
		if !accountError(w, err) {
			reply(w, 200, map[string]any{"success": true})
		}
		return
	}
	create := r.Method == http.MethodPost && id == ""
	if !create && !(r.Method == http.MethodPut && id != "") {
		reject(w, 404, "接口不存在")
		return
	}
	var input struct {
		Name        *string
		Permissions []string
	}
	if decode(r, &input) != nil || create && input.Name == nil || len(input.Permissions) > 100 {
		reject(w, 400, "Token 信息无效")
		return
	}
	changes := map[string]any{}
	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if name == "" || len(name) > 128 {
			reject(w, 400, "Token 名称无效")
			return
		}
		changes["name"] = name
	}
	if input.Permissions != nil {
		changes["permissions"] = input.Permissions
	}
	if create {
		var err error
		id, err = secureRandom(16)
		if accountError(w, err) {
			return
		}
		key, err := secureRandom(32)
		if accountError(w, err) {
			return
		}
		changes["id"], changes["token"] = id, "mapi_"+key
		changes["createdAt"], changes["createdBy"] = time.Now().UnixMilli(), s.username
		changes["lastUsedAt"], changes["usageCount"] = nil, 0
		if input.Permissions == nil {
			changes["permissions"] = []string{}
		}
	}
	data, err := migrate.WriteToken(a.dataDir, id, changes, create)
	if !accountError(w, err) {
		reply(w, 200, map[string]any{"success": true, "data": data})
	}
}
