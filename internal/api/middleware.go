package api

import (
	"net/http"
)

// AuthMiddleware returns a middleware that checks X-API-Key header.
// If apiKeys is empty, all requests are allowed.
// The /health endpoint is always allowed.
func AuthMiddleware(apiKeys []string) func(http.Handler) http.Handler {
	if len(apiKeys) == 0 {
		// Auth disabled
		return func(next http.Handler) http.Handler {
			return next
		}
	}

	keySet := make(map[string]struct{}, len(apiKeys))
	for _, k := range apiKeys {
		keySet[k] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Always allow health check
			if r.URL.Path == "/health" {
				next.ServeHTTP(w, r)
				return
			}

			key := r.Header.Get("X-API-Key")
			if key == "" {
				writeJSON(w, http.StatusUnauthorized, map[string]string{
					"error": "missing X-API-Key header",
				})
				return
			}

			if _, ok := keySet[key]; !ok {
				writeJSON(w, http.StatusUnauthorized, map[string]string{
					"error": "invalid API key",
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
