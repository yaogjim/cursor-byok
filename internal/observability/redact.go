package observability

import (
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"unicode"
)

const RedactedValue = "[REDACTED]"

var (
	bearerCredentialPattern  = regexp.MustCompile(`(?i)\bbearer\s+[^\s,;"']+`)
	queryCredentialPattern   = regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|auth(?:orization)?|cookie|password|secret|token)=([^&\s"']+)`)
	labeledCredentialPattern = regexp.MustCompile(`(?i)\b(api[_ -]?key|access[_ -]?token|auth(?:orization)?|cookie|password|secret|token)(\s+provided)?(\s*[:=]\s*)([^&\s,;"']+)`)
)

var sensitiveFieldNames = map[string]struct{}{
	"apikey":             {},
	"auth":               {},
	"authorization":      {},
	"bearertoken":        {},
	"clientsecret":       {},
	"cookie":             {},
	"credential":         {},
	"credentials":        {},
	"customheadersjson":  {},
	"idtoken":            {},
	"key":                {},
	"password":           {},
	"passwd":             {},
	"privatekey":         {},
	"proxyauthorization": {},
	"refreshtoken":       {},
	"secret":             {},
	"sessiontoken":       {},
	"setcookie":          {},
	"token":              {},
	"xapikey":            {},
}

func Sanitize(value any) any {
	return sanitizeReflect(reflect.ValueOf(value), "", 0, make(map[visit]bool))
}

// SanitizeText removes credentials from unstructured text before it reaches a sink.
func SanitizeText(value string) string {
	return sanitizeString(value)
}

type visit struct {
	typeName reflect.Type
	pointer  uintptr
}

func sanitizeReflect(value reflect.Value, fieldName string, depth int, visited map[visit]bool) any {
	if !value.IsValid() {
		return nil
	}
	if depth > 64 {
		return map[string]any{"omitted": true, "reason": "max_depth"}
	}
	for value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}
	if isSensitiveField(fieldName) {
		return RedactedValue
	}

	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		key := visit{typeName: value.Type(), pointer: value.Pointer()}
		if visited[key] {
			return map[string]any{"omitted": true, "reason": "cycle"}
		}
		visited[key] = true
		defer delete(visited, key)
		return sanitizeReflect(value.Elem(), fieldName, depth+1, visited)
	case reflect.Map:
		if value.IsNil() {
			return nil
		}
		key := visit{typeName: value.Type(), pointer: value.Pointer()}
		if visited[key] {
			return map[string]any{"omitted": true, "reason": "cycle"}
		}
		visited[key] = true
		defer delete(visited, key)
		result := make(map[string]any, value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			name := fmt.Sprint(iterator.Key().Interface())
			result[name] = sanitizeReflect(iterator.Value(), name, depth+1, visited)
		}
		return result
	case reflect.Struct:
		if raw, ok := value.Interface().(json.RawMessage); ok {
			return sanitizeRawJSON(raw, depth, visited)
		}
		result := make(map[string]any, value.NumField())
		valueType := value.Type()
		for index := 0; index < value.NumField(); index++ {
			field := valueType.Field(index)
			if field.PkgPath != "" {
				continue
			}
			name, include := serializedFieldName(field)
			if !include {
				continue
			}
			result[name] = sanitizeReflect(value.Field(index), name, depth+1, visited)
		}
		return result
	case reflect.Slice:
		if value.IsNil() {
			return nil
		}
		if value.Type().Elem().Kind() == reflect.Uint8 {
			if raw, ok := value.Interface().(json.RawMessage); ok {
				return sanitizeRawJSON(raw, depth, visited)
			}
			return map[string]any{"omitted": true, "reason": "binary_payload", "length": value.Len()}
		}
		fallthrough
	case reflect.Array:
		result := make([]any, value.Len())
		for index := 0; index < value.Len(); index++ {
			result[index] = sanitizeReflect(value.Index(index), fieldName, depth+1, visited)
		}
		return result
	case reflect.String:
		return sanitizeString(value.String())
	case reflect.Bool:
		return value.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return value.Uint()
	case reflect.Float32, reflect.Float64:
		return value.Float()
	default:
		return fmt.Sprint(value.Interface())
	}
}

func sanitizeRawJSON(raw json.RawMessage, depth int, visited map[visit]bool) any {
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return map[string]any{"omitted": true, "reason": "decode_error", "length": len(raw)}
	}
	return sanitizeReflect(reflect.ValueOf(decoded), "", depth+1, visited)
}

func serializedFieldName(field reflect.StructField) (string, bool) {
	name := field.Name
	if tag := field.Tag.Get("json"); tag != "" {
		parts := strings.Split(tag, ",")
		if parts[0] == "-" {
			return "", false
		}
		if parts[0] != "" {
			name = parts[0]
		}
	}
	return name, true
}

func isSensitiveField(value string) bool {
	normalized := normalizeFieldName(value)
	if _, ok := sensitiveFieldNames[normalized]; ok {
		return true
	}
	return strings.HasSuffix(normalized, "apikey") ||
		strings.HasSuffix(normalized, "accesstoken") ||
		strings.HasSuffix(normalized, "authtoken") ||
		strings.HasSuffix(normalized, "refreshtoken") ||
		strings.HasSuffix(normalized, "sessiontoken") ||
		strings.HasSuffix(normalized, "clientsecret") ||
		strings.HasSuffix(normalized, "privatekey")
}

func isSensitiveQueryField(value string) bool {
	switch normalizeFieldName(value) {
	case "code", "oauthcode", "signature", "sig", "signedtoken":
		return true
	default:
		return false
	}
}

func normalizeFieldName(value string) string {
	return strings.Map(func(character rune) rune {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			return unicode.ToLower(character)
		}
		return -1
	}, strings.TrimSpace(value))
}

func sanitizeString(value string) string {
	sanitized := bearerCredentialPattern.ReplaceAllString(value, "Bearer "+RedactedValue)
	sanitized = queryCredentialPattern.ReplaceAllString(sanitized, "$1="+RedactedValue)
	sanitized = labeledCredentialPattern.ReplaceAllString(sanitized, "$1$2$3"+RedactedValue)
	trimmed := strings.TrimSpace(sanitized)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed == nil || parsed.Scheme == "" || parsed.Host == "" {
		return sanitized
	}
	if parsed.User != nil {
		parsed.User = url.User(RedactedValue)
	}
	query := parsed.Query()
	changed := false
	for key := range query {
		if !isSensitiveField(key) && !isSensitiveQueryField(key) {
			continue
		}
		query.Set(key, RedactedValue)
		changed = true
	}
	if changed {
		parsed.RawQuery = query.Encode()
	}
	return parsed.String()
}
