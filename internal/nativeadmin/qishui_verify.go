package nativeadmin

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/qishui"
)

const qishuiVerifyRoot = "/admin/qr/qishui/verify/"
const qishuiAssetsRoot = "/admin/qr/qishui/security/"

func (a *Admin) qishuiAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, qishuiAssetsRoot)
	switch name {
	case "security_host.html", "security_seed.html", "bdms.js", "react.js", "react-dom.js", "sdk-glue.js":
	default:
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet || a.qishui == nil {
		http.NotFound(w, r)
		return
	}
	data := a.qishui.VerificationAsset(name)
	if len(data) == 0 {
		http.NotFound(w, r)
		return
	}
	if strings.HasSuffix(name, ".html") {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "sandbox allow-scripts; default-src 'none'; script-src 'self' 'unsafe-inline' 'unsafe-eval' https:; connect-src 'self' https:; style-src 'unsafe-inline' https:; img-src https: data:; frame-src https:; frame-ancestors 'self'; base-uri 'none'")
	} else {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Access-Control-Allow-Origin", "*")
	}
	_, _ = w.Write(data)
}

func (a *Admin) qishuiVerify(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != "null" {
		reject(w, 403, "验证窗口来源无效")
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "null")
	w.Header().Set("Access-Control-Allow-Methods", "POST")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		reject(w, 405, "请求方法无效")
		return
	}
	var input struct {
		Key     string
		Request qishui.VerificationRequest
	}
	if decode(r, &input) != nil || len(input.Key) != 43 {
		reject(w, 400, "验证请求无效")
		return
	}
	s := a.lockQishuiChallenge(input.Key)
	if s == nil {
		reject(w, 401, "验证会话已过期")
		return
	}
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	var data any
	var err error
	switch strings.TrimPrefix(r.URL.Path, qishuiVerifyRoot) {
	case "start":
		data, err = s.qishuiQR.VerificationStart()
	case "request":
		data, err = s.qishuiQR.VerificationProxy(ctx, input.Request)
	case "complete":
		var status string
		status, err = s.qishuiQR.VerificationComplete(ctx)
		data = map[string]string{"status": status}
	default:
		reject(w, 404, "接口不存在")
		return
	}
	if err != nil {
		reject(w, 502, "汽水二次验证失败，请重新扫码")
		return
	}
	reply(w, 200, map[string]any{"success": true, "data": data})
}

// lockQishuiChallenge returns a locked session. The management cookie stays
// outside the sandbox. Its verification key is scoped to that QR context.
func (a *Admin) lockQishuiChallenge(value string) *session {
	a.mu.Lock()
	candidates := make([]*session, 0, len(a.sessions))
	for _, s := range a.sessions {
		if time.Now().Before(s.expires) {
			candidates = append(candidates, s)
		}
	}
	a.mu.Unlock()
	for _, s := range candidates {
		s.mu.Lock()
		if s.qishuiQR != nil && equal(s.qishuiVerifyKey, value) {
			return s
		}
		s.mu.Unlock()
	}
	return nil
}
