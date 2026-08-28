package subscriptionauth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

func defaultHTTPClient() HTTPDoer {
	return &http.Client{Timeout: 30 * time.Second}
}

func doJSON(ctx context.Context, client HTTPDoer, method string, endpoint string, headers map[string]string, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	return doRequest(client, req)
}

func doForm(ctx context.Context, client HTTPDoer, endpoint string, headers map[string]string, form url.Values) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	return doRequest(client, req)
}

func doRequest(client HTTPDoer, req *http.Request) (int, []byte, error) {
	if client == nil {
		client = defaultHTTPClient()
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, RedactError(err)
	}
	return resp.StatusCode, readHTTPBody(resp, 8192), nil
}

func parseJSONObject(body []byte) map[string]any {
	if len(body) == 0 {
		return map[string]any{}
	}
	var parsed map[string]any
	if json.Unmarshal(body, &parsed) != nil || parsed == nil {
		return map[string]any{}
	}
	return parsed
}

func jsonString(object map[string]any, keys ...string) string {
	if object == nil {
		return ""
	}
	for _, key := range keys {
		if text := jsonText(object[key]); text != "" {
			return text
		}
	}
	return ""
}

func jsonInt(object map[string]any, fallback int, keys ...string) int {
	if object == nil {
		return fallback
	}
	for _, key := range keys {
		if n, ok := anyInt(object[key]); ok {
			return n
		}
	}
	return fallback
}

func anyInt(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		return int(typed), true
	case json.Number:
		n, err := typed.Int64()
		return int(n), err == nil
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(typed))
		return n, err == nil
	default:
		return 0, false
	}
}

func anyFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case json.Number:
		n, err := typed.Float64()
		return n, err == nil
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case string:
		n, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return n, err == nil
	default:
		return 0, false
	}
}

func nestedObject(object map[string]any, keys ...string) map[string]any {
	current := object
	for _, key := range keys {
		if current == nil {
			return map[string]any{}
		}
		next, ok := current[key].(map[string]any)
		if !ok {
			return map[string]any{}
		}
		current = next
	}
	if current == nil {
		return map[string]any{}
	}
	return current
}

func pointerValue(root map[string]any, path ...string) any {
	if root == nil {
		return nil
	}
	var current any = root
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = object[key]
	}
	return current
}
