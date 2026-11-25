package nativeadmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/migrate"
)

type webhookConfig struct {
	URL          string            `json:"url"`
	Enabled      bool              `json:"enabled"`
	ContentType  string            `json:"contentType"`
	Headers      map[string]string `json:"headers"`
	BodyTemplate string            `json:"bodyTemplate"`
}

type notification struct {
	Title, Message, Event string
	Priority              int
}

func (a *Admin) webhookConfig() (webhookConfig, error) {
	var settings struct {
		URL          string            `json:"webhookUrl"`
		Enabled      bool              `json:"webhookEnabled"`
		ContentType  string            `json:"webhookContentType"`
		Headers      map[string]string `json:"webhookHeaders"`
		BodyTemplate string            `json:"webhookBodyTemplate"`
	}
	settings.ContentType = "application/json"
	err := readFileJSON(filepath.Join(a.dataDir, "config.json"), &settings)
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return webhookConfig(settings), err
}

var sharedIPv4 = netip.MustParsePrefix("100.64.0.0/10")
var metadataIPv6 = netip.MustParseAddr("fd00:ec2::254")

func webhookIP(ip netip.Addr, allowPrivate bool) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip == metadataIPv6 {
		return false
	}
	return allowPrivate || ip.IsGlobalUnicast() && !ip.IsPrivate() && !sharedIPv4.Contains(ip)
}

func validateWebhookURL(address string) (*url.URL, error) {
	target, err := url.Parse(address)
	if err != nil || target.Hostname() == "" || target.User != nil || target.Fragment != "" ||
		(target.Scheme != "http" && target.Scheme != "https") {
		return nil, errors.New("invalid webhook URL")
	}
	if ip, err := netip.ParseAddr(target.Hostname()); err == nil && !webhookIP(ip, os.Getenv("ALLOW_PRIVATE_WEBHOOK") == "1") {
		return nil, errors.New("restricted webhook IP")
	}
	return target, nil
}

func dialWebhook(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("could not resolve webhook host")
	}
	for _, ip := range ips {
		if !webhookIP(ip, os.Getenv("ALLOW_PRIVATE_WEBHOOK") == "1") {
			return nil, errors.New("webhook host resolves to a restricted IP")
		}
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	for _, ip := range ips {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, errors.New("failed to connect to webhook")
}

var tokenPlaceholder = regexp.MustCompile(`\{\{\w+\}\}`)

func fillTemplate(value any, replacements map[string]string) any {
	switch item := value.(type) {
	case string:
		return tokenPlaceholder.ReplaceAllStringFunc(item, func(key string) string {
			if value, ok := replacements[key[2:len(key)-2]]; ok {
				return value
			}
			return key
		})
	case []any:
		for i, nested := range item {
			item[i] = fillTemplate(nested, replacements)
		}
	case map[string]any:
		for key, nested := range item {
			item[key] = fillTemplate(nested, replacements)
		}
	}
	return value
}

func sendWebhook(ctx context.Context, config webhookConfig, event notification) error {
	u, err := validateWebhookURL(config.URL)
	if err != nil {
		return err
	}
	var body any = map[string]any{"title": event.Title, "message": event.Message, "priority": event.Priority}
	if config.BodyTemplate != "" {
		var value any
		if json.Unmarshal([]byte(config.BodyTemplate), &value) == nil {
			body = fillTemplate(value, map[string]string{"title": event.Title, "message": event.Message,
				"priority": fmt.Sprint(event.Priority), "event": event.Event, "time": time.Now().Format(time.RFC3339)})
		}
	}
	ct := config.ContentType
	if ct == "" {
		ct = "application/json"
	}
	if value := configuredHeaders(config.Headers).Get("Content-Type"); value != "" {
		ct = value
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	if strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
		fields, ok := body.(map[string]any)
		if !ok {
			return errors.New("webhook form requires an object")
		}
		params := url.Values{}
		for key, value := range fields {
			switch item := value.(type) {
			case string:
				params.Set(key, item)
			default:
				encoded, err := json.Marshal(value)
				if err != nil {
					return err
				}
				params.Set(key, string(encoded))
			}
		}
		data = []byte(params.Encode())
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	request.Header = configuredHeaders(config.Headers)
	request.Header.Set("Content-Type", ct)
	transport := &http.Transport{DialContext: dialWebhook, TLSHandshakeTimeout: 5 * time.Second, DisableKeepAlives: true}
	client := &http.Client{Timeout: 10 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("webhook HTTP %d", response.StatusCode)
	}
	return nil
}

func configuredHeaders(config map[string]string) http.Header {
	values := make(http.Header)
	for key, value := range config {
		switch strings.ToLower(key) {
		case "cookie", "host", "content-length", "transfer-encoding", "connection", "proxy-authorization":
			continue
		}
		if !strings.ContainsAny(value, "\r\n") {
			values.Set(key, value)
		}
	}
	return values
}

func (a *Admin) webhookRoutes(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/admin/webhook":
		config, err := a.webhookConfig()
		if !accountError(w, err) {
			reply(w, 200, map[string]any{"success": true, "data": config})
		}
	case r.Method == http.MethodPost && r.URL.Path == "/admin/webhook/test":
		config, err := a.webhookConfig()
		if accountError(w, err) {
			return
		}
		if err := sendWebhook(r.Context(), config, notification{Title: "Webhook 测试", Message: "OpenMusic 音源监测测试", Event: "test", Priority: 3}); err != nil {
			reject(w, 400, "Webhook 投递失败")
			return
		}
		reply(w, 200, map[string]any{"success": true, "message": "Webhook 测试消息已发送"})
	case r.Method == http.MethodPut && r.URL.Path == "/admin/webhook":
		var input struct {
			URL, ContentType, BodyTemplate *string
			Enabled                        *bool
			Headers                        json.RawMessage
		}
		if decode(r, &input) != nil {
			reject(w, 400, "Webhook 配置无效")
			return
		}
		changes := map[string]any{}
		if input.URL != nil {
			address := strings.TrimSpace(*input.URL)
			if address != "" {
				if _, err := validateWebhookURL(address); err != nil {
					reject(w, 400, "Webhook URL 无效")
					return
				}
			}
			changes["webhookUrl"] = address
		}
		if input.Enabled != nil {
			changes["webhookEnabled"] = *input.Enabled
		}
		if input.ContentType != nil {
			changes["webhookContentType"] = *input.ContentType
		}
		if input.BodyTemplate != nil {
			if *input.BodyTemplate != "" && !json.Valid([]byte(*input.BodyTemplate)) {
				reject(w, 400, "Webhook 模板必须是 JSON")
				return
			}
			changes["webhookBodyTemplate"] = *input.BodyTemplate
		}
		if input.Headers != nil {
			raw := input.Headers
			var encoded string
			if json.Unmarshal(raw, &encoded) == nil && encoded != "" {
				raw = json.RawMessage(encoded)
			}
			var headers map[string]string
			if json.Unmarshal(raw, &headers) != nil {
				reject(w, 400, "Webhook 请求头必须是 JSON 对象")
				return
			}
			changes["webhookHeaders"] = headers
		}
		a.writeMu.Lock()
		err := migrate.WriteSettings(a.dataDir, changes)
		a.writeMu.Unlock()
		if !accountError(w, err) {
			config, err := a.webhookConfig()
			if !accountError(w, err) {
				reply(w, 200, map[string]any{"success": true, "data": config})
			}
		}
	default:
		reject(w, 404, "接口不存在")
	}
}
