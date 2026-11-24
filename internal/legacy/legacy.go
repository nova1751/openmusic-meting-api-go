package legacy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

const (
	BridgePrefix       = "/__meting_go"
	BridgeKeyHeader    = "X-Meting-Bridge-Key"
	BridgeTargetHeader = "X-Meting-Bridge-Target"
)

type Response struct {
	Status int
	Body   json.RawMessage
}

type Backend struct {
	Proxy  *httputil.ReverseProxy
	origin *url.URL
	client *http.Client
	key    string
}

func New(origin, public *url.URL, key string, client *http.Client) *Backend {
	proxy := &httputil.ReverseProxy{
		Transport: client.Transport,
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(origin)
			// OpenMusic signs the wire query, including characters net/url may reject.
			request.Out.URL.RawQuery = request.In.URL.RawQuery
			request.Out.URL.ForceQuery = request.In.URL.ForceQuery
			request.Out.Host = request.In.Host
			request.Out.Header.Del(BridgeKeyHeader)
			request.Out.Header.Del(BridgeTargetHeader)
			request.Out.Header.Del("X-Forwarded-Url")
			request.Out.Header.Del("X-Forwarded-Prefix")
			request.SetXForwarded()
			if public != nil {
				request.Out.Header.Set("X-Forwarded-Host", public.String())
				request.Out.Header.Set("X-Forwarded-Proto", public.Scheme)
			}
		},
		ModifyResponse: func(response *http.Response) error {
			response.Header.Del("Access-Control-Allow-Origin")
			if protected(response.Request) {
				response.Header.Set("Cache-Control", "no-store, private")
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			code, message, status := "UPSTREAM_REQUEST_FAILED", "兼容服务请求失败", http.StatusBadGateway
			if errors.Is(err, context.DeadlineExceeded) {
				code, message, status = "UPSTREAM_TIMEOUT", "兼容服务请求超时", http.StatusGatewayTimeout
			}
			if protected(r) {
				w.Header().Set("Cache-Control", "no-store, private")
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "code": code, "error": message})
		},
	}
	return &Backend{Proxy: proxy, origin: origin, client: client, key: key}
}

func protected(r *http.Request) bool {
	path := r.URL.Path
	return path == "/api" || path == "/test" || path == "/audio/qishui" ||
		path == "/admin/openmusic" || path == "/admin/fm" ||
		(strings.HasPrefix(path, "/admin/") && r.Header.Get("Authorization") != "")
}

func (b *Backend) CanAuthorize() bool { return b.key != "" }

// Authorize asks the legacy process to run its regular auth middleware against
// the original request, preserving one replay store for both runtimes.
func (b *Backend) Authorize(r *http.Request) (string, *Response, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	target := b.origin.ResolveReference(&url.URL{Path: BridgePrefix + "/authorize"})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set(BridgeKeyHeader, b.key)
	req.Header.Set(BridgeTargetHeader, base64.RawURLEncoding.EncodeToString([]byte(r.URL.RequestURI())))
	for _, name := range []string{"Authorization", "X-OpenMusic-Assertion", "X-OpenMusic-Signature", "X-OpenMusic-Cookie"} {
		if value := r.Header.Get(name); value != "" {
			req.Header.Set(name, value)
		}
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	const limit = 64 << 10
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(body) > limit {
		return "", nil, errors.New("invalid authorization response")
	}
	var reply struct {
		Cookie *string `json:"cookie"`
		Code   string  `json:"code"`
	}
	if json.Unmarshal(body, &reply) != nil {
		return "", nil, errors.New("invalid authorization response")
	}
	if resp.StatusCode == http.StatusOK && reply.Cookie != nil {
		return *reply.Cookie, nil, nil
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized && (reply.Code == "OPENMUSIC_AUTH_REQUIRED" || reply.Code == "OPENMUSIC_REPLAY"),
		resp.StatusCode == http.StatusServiceUnavailable && reply.Code == "OPENMUSIC_AUTH_BUSY":
		return "", &Response{Status: resp.StatusCode, Body: body}, nil
	default:
		return "", nil, errors.New("unexpected authorization response")
	}
}

func (b *Backend) Ready(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.origin.String()+"/", nil)
	if err != nil {
		return false
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 256<<10))
	return resp.StatusCode == http.StatusOK
}
