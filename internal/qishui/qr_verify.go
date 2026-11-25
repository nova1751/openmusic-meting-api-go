package qishui

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/providerhttp"
)

func (b *QRBrowser) VerificationAsset(name string) []byte {
	return b.assets["/"+name]
}

func (q *QRSession) VerificationStart() (any, error) {
	if q.decision == nil || q.tab.Err() != nil || q.last != "second_verify" {
		return nil, errors.New("no active Qishui verification challenge")
	}
	params := map[string]string{}
	for key, values := range q.generalParams(qrHex(4)) {
		params[key] = values[0]
	}
	return map[string]any{"decision": q.decision, "generalParams": params}, nil
}

type VerificationRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    *string           `json:"body"`
	Timeout int               `json:"timeout"`
}

func (q *QRSession) VerificationProxy(ctx context.Context, input VerificationRequest) (any, error) {
	if _, err := q.VerificationStart(); err != nil {
		return nil, err
	}
	u, err := url.Parse(input.URL)
	if err != nil || u.Scheme != "https" || u.Port() != "" || u.User != nil || u.Fragment != "" ||
		(input.Method != "GET" && input.Method != "POST") || len(input.URL) > 4096 ||
		(input.Body != nil && len(*input.Body) > 16384) {
		return nil, errors.New("invalid Qishui verification request")
	}
	switch u.Hostname() {
	case "api.qishui.com", "auth.zijieapi.com", "bff-pc.qishui.com":
	default:
		return nil, errors.New("unsupported Qishui verification service")
	}
	if path := u.Path; !strings.HasPrefix(path, "/passport/") && !strings.HasPrefix(path, "/verify") && !strings.HasPrefix(path, "/uc/") {
		return nil, errors.New("unsupported Qishui verification path")
	}
	headers := make(map[string]string)
	for key, value := range input.Headers {
		name := strings.ToLower(key)
		if (name == "accept" || name == "content-type" || strings.HasPrefix(name, "x-tt-passport-")) && len(value) <= 2048 && !strings.ContainsAny(value, "\r\n") {
			headers[key] = value
		}
	}
	response, _, err := q.exchange(ctx, passportRequest{Method: input.Method, URL: u.String(), Headers: headers, Body: input.Body, Timeout: 15000}, false)
	if err != nil {
		return nil, err
	}
	return response, nil
}

func (q *QRSession) VerificationComplete(ctx context.Context) (string, error) {
	if _, err := q.VerificationStart(); err != nil {
		return "", err
	}
	biz := q.decision.Object("biz_params")
	if raw := q.decision.Text("biz_params"); biz == nil && raw != "" {
		biz, _ = providerhttp.Decode([]byte(raw))
		if biz == nil {
			if vals, err := url.ParseQuery(raw); err == nil {
				biz = providerhttp.Object{}
				for key := range vals {
					biz[key] = vals.Get(key)
				}
			}
		}
	}
	params := make(url.Values)
	for key, value := range biz {
		if len(key) > 128 || value == nil {
			continue
		}
		if text := providerhttp.Text(value); text != "" {
			params.Set(key, text)
		} else if raw, err := json.Marshal(value); err == nil {
			params.Set(key, string(raw))
		}
	}
	payload, err := q.check(ctx, url.Values{"isResend": {"true"}}, params)
	if err != nil {
		return "", err
	}
	q.next = time.Now().Add(2500 * time.Millisecond)
	status, _, err := q.applyPoll(payload)
	return status, err
}
