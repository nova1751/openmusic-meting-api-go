package providerhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const maxBody = 16 << 20

type StatusError struct{ Code int }

func (e StatusError) Error() string { return fmt.Sprintf("provider HTTP status %d", e.Code) }

// JSON makes a bounded HTTP request to an adapter-selected endpoint.
func JSON(ctx context.Context, client *http.Client, method, endpoint string, headers http.Header, input, output any) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	for name, values := range headers {
		if len(values) > 0 {
			req.Header[name] = values
		}
	}
	if input != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return StatusError{Code: resp.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return err
	}
	if len(data) > maxBody {
		return errors.New("provider response exceeds size limit")
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return errors.New("empty provider response")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("unexpected data after provider response")
	}
	return nil
}

func CookieValue(raw string, names ...string) string {
	request := &http.Request{Header: http.Header{"Cookie": {raw}}}
	for _, name := range names {
		if value, err := request.Cookie(name); err == nil && strings.TrimSpace(value.Value) != "" {
			return value.Value
		}
	}
	return ""
}
