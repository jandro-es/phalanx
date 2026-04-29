// Authentication and webhook signature middleware for the Phalanx API.
package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
)

// AuthConfig configures BearerAuth.
type AuthConfig struct {
	// Tokens is the set of accepted bearer tokens. Empty disables auth.
	Tokens []string
	// SkipPrefixes are URL path prefixes that bypass auth (webhooks, health).
	SkipPrefixes []string
}

// BearerAuth returns a middleware that requires Authorization: Bearer <token>
// against AuthConfig.Tokens. Webhooks and the health check are exempted via
// SkipPrefixes — webhooks have their own HMAC verification.
//
// When AuthConfig.Tokens is empty, the middleware is a no-op so existing local
// dev workflows continue to work; production deployments MUST set tokens.
func BearerAuth(cfg AuthConfig) func(http.Handler) http.Handler {
	tokenSet := make(map[string]struct{}, len(cfg.Tokens))
	for _, t := range cfg.Tokens {
		if t = strings.TrimSpace(t); t != "" {
			tokenSet[t] = struct{}{}
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}
			if len(tokenSet) == 0 {
				next.ServeHTTP(w, r)
				return
			}
			for _, p := range cfg.SkipPrefixes {
				if strings.HasPrefix(r.URL.Path, p) {
					next.ServeHTTP(w, r)
					return
				}
			}

			tok := extractBearerToken(r.Header.Get("Authorization"))
			if tok == "" {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing bearer token"})
				return
			}
			if _, ok := tokenSet[tok]; !ok {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid bearer token"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func extractBearerToken(h string) string {
	const prefix = "Bearer "
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// errMissingSignature is returned when a webhook arrives without the expected
// signature header. Caller maps to HTTP 401.
var errMissingSignature = errors.New("missing webhook signature")

// errInvalidSignature is returned when the supplied signature doesn't verify.
var errInvalidSignature = errors.New("invalid webhook signature")

// VerifyGitHubSignature validates an X-Hub-Signature-256 header against the
// raw request body using the shared secret. Returns nil on success.
//
// When secret is empty the check is skipped (so unauthenticated dev setups
// keep working). Production deployments MUST set GITHUB_WEBHOOK_SECRET.
func VerifyGitHubSignature(secret string, body []byte, headerValue string) error {
	if secret == "" {
		return nil
	}
	const prefix = "sha256="
	if !strings.HasPrefix(headerValue, prefix) {
		return errMissingSignature
	}
	got, err := hex.DecodeString(headerValue[len(prefix):])
	if err != nil || len(got) != sha256.Size {
		return errInvalidSignature
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := mac.Sum(nil)
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return errInvalidSignature
	}
	return nil
}

// VerifyGitLabToken compares the X-Gitlab-Token header against the configured
// secret in constant time. GitLab uses a shared-token model rather than HMAC.
func VerifyGitLabToken(secret, headerValue string) error {
	if secret == "" {
		return nil
	}
	if headerValue == "" {
		return errMissingSignature
	}
	if subtle.ConstantTimeCompare([]byte(headerValue), []byte(secret)) != 1 {
		return errInvalidSignature
	}
	return nil
}

// readAndRestore reads the entire request body and replaces r.Body with a
// fresh reader so downstream handlers (json.Decoder etc.) still see the bytes.
func readAndRestore(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	return body, nil
}
