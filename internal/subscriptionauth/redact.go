package subscriptionauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode"
)

var (
	bearerRE = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9\-._~+/]+=*`)
	jwtRE    = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*`)
)

func trimLower(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func trimSpace(value string) string {
	return strings.TrimSpace(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func RedactText(value string) string {
	if value == "" {
		return ""
	}
	redacted := bearerRE.ReplaceAllString(value, "Bearer [redacted]")
	redacted = jwtRE.ReplaceAllString(redacted, "[redacted-jwt]")
	for _, key := range []string{"access_token", "refresh_token", "id_token", "poll_token", "device_code", "OPENAI_API_KEY"} {
		redacted = redactJSONField(redacted, key)
	}
	if looksLikeToken(redacted) {
		return "[redacted]"
	}
	return redacted
}

func redactJSONField(value string, key string) string {
	pattern := regexp.MustCompile(`(?i)("` + regexp.QuoteMeta(key) + `"\s*:\s*")([^"]*)(")`)
	return pattern.ReplaceAllString(value, `${1}[redacted]$3`)
}

func looksLikeToken(value string) bool {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) < 24 {
		return false
	}
	if strings.Count(trimmed, ".") == 2 && strings.HasPrefix(trimmed, "eyJ") {
		return true
	}
	letters := 0
	for _, r := range trimmed {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			letters++
		} else if r != '-' && r != '_' && r != '.' && r != '+' && r != '/' && r != '=' {
			return false
		}
	}
	return letters >= 24 && !strings.Contains(trimmed, " ")
}

func RedactError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrAuthRequired) || errors.Is(err, ErrStaticCredential) {
		return err
	}
	message := RedactText(err.Error())
	if message == err.Error() {
		return err
	}
	return errors.New(message)
}

func safeErrorf(format string, args ...any) error {
	safe := make([]any, len(args))
	for i, arg := range args {
		switch value := arg.(type) {
		case string:
			safe[i] = RedactText(value)
		case []byte:
			safe[i] = RedactText(string(value))
		case error:
			safe[i] = RedactError(value)
		default:
			safe[i] = arg
		}
	}
	return errors.New(fmt.Sprintf(format, safe...))
}

func httpStatusError(op string, status int, body []byte) error {
	code := jsonErrorCode(body)
	if code != "" {
		return fmt.Errorf("%s failed (HTTP %d): %s", op, status, RedactText(code))
	}
	if status >= 400 {
		return fmt.Errorf("%s failed (HTTP %d)", op, status)
	}
	return fmt.Errorf("%s failed", op)
}

func jsonErrorCode(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	if code := jsonText(parsed["error"]); code != "" && !looksLikeToken(code) {
		return code
	}
	if nested, ok := parsed["error"].(map[string]any); ok {
		if code := jsonText(nested["code"]); code != "" && !looksLikeToken(code) {
			return code
		}
		if code := jsonText(nested["type"]); code != "" && !looksLikeToken(code) {
			return code
		}
	}
	if desc := jsonText(parsed["error_description"]); desc != "" && !looksLikeToken(desc) {
		return desc
	}
	return ""
}

func jsonText(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func readHTTPBody(resp *http.Response, max int) []byte {
	if resp == nil || resp.Body == nil {
		return nil
	}
	defer resp.Body.Close()
	if max <= 0 {
		max = 4096
	}
	buf := make([]byte, max+1)
	n, _ := resp.Body.Read(buf)
	if n > max {
		return buf[:max]
	}
	return buf[:n]
}
