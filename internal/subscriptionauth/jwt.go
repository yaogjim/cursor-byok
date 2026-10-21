package subscriptionauth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

type jwtClaims map[string]any

func decodeJWTPayload(token string) jwtClaims {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) < 2 {
		return nil
	}
	payload := parts[1]
	decoded, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		decoded, err = base64.URLEncoding.DecodeString(payload)
	}
	if err != nil {
		decoded, err = base64.StdEncoding.DecodeString(payload)
	}
	if err != nil {
		return nil
	}
	var claims jwtClaims
	if json.Unmarshal(decoded, &claims) != nil {
		return nil
	}
	return claims
}

func (claims jwtClaims) stringValue(keys ...string) string {
	if claims == nil {
		return ""
	}
	for _, key := range keys {
		if text := jsonText(claims[key]); text != "" {
			return text
		}
	}
	return ""
}

func (claims jwtClaims) nestedString(path ...string) string {
	if claims == nil || len(path) == 0 {
		return ""
	}
	var current any = map[string]any(claims)
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = object[key]
	}
	return jsonText(current)
}

func (claims jwtClaims) expiresAt() time.Time {
	if claims == nil {
		return time.Time{}
	}
	switch value := claims["exp"].(type) {
	case float64:
		if value <= 0 {
			return time.Time{}
		}
		return time.Unix(int64(value), 0).UTC()
	case json.Number:
		n, err := value.Int64()
		if err != nil || n <= 0 {
			return time.Time{}
		}
		return time.Unix(n, 0).UTC()
	default:
		return time.Time{}
	}
}

func chatgptAccountIDFromToken(accessToken string) string {
	claims := decodeJWTPayload(accessToken)
	return firstNonEmpty(
		claims.nestedString("https://api.openai.com/auth", "chatgpt_account_id"),
		claims.stringValue("chatgpt_account_id"),
	)
}

func emailFromToken(tokens ...string) string {
	for _, token := range tokens {
		claims := decodeJWTPayload(token)
		if email := claims.stringValue("email", "preferred_username"); email != "" {
			return email
		}
	}
	return ""
}

func accountIdentity(kind ProviderKind, accessToken string, idToken string) (accountID string, displayName string, chatgptAccountID string) {
	accountID, displayName, chatgptAccountID, _ = accountIdentityWithStability(kind, accessToken, idToken)
	return accountID, displayName, chatgptAccountID
}

func accountIdentityWithStability(kind ProviderKind, accessToken string, idToken string) (accountID string, displayName string, chatgptAccountID string, stable bool) {
	claims := decodeJWTPayload(accessToken)
	idClaims := decodeJWTPayload(idToken)
	chatgptAccountID = firstNonEmpty(
		chatgptAccountIDFromToken(accessToken),
		chatgptAccountIDFromToken(idToken),
	)
	email := firstNonEmpty(
		emailFromToken(idToken, accessToken),
		claims.stringValue("email", "preferred_username", "name"),
		idClaims.stringValue("email", "preferred_username", "name"),
	)
	var subject string
	switch kind {
	case ProviderCodex:
		subject = firstNonEmpty(chatgptAccountID, claims.stringValue("sub"), email)
	default:
		subject = firstNonEmpty(claims.stringValue("sub"), email, claims.stringValue("preferred_username"))
	}
	stable = subject != ""
	if !stable {
		subject = tokenFingerprint(accessToken)
	}
	displayName = firstNonEmpty(email, claims.stringValue("name"), subject, string(kind))
	accountID = string(kind) + ":" + subject
	return accountID, displayName, chatgptAccountID, stable
}

func tokenFingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:8])
}

func jwtExpiresAt(accessToken string) time.Time {
	return decodeJWTPayload(accessToken).expiresAt()
}
