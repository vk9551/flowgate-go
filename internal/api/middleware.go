package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// authMiddleware wraps next with JWT verification.
// When cfg.Server.Auth.Type is "none", all requests pass through.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := s.getConfig()

		// Health and dashboard are always public.
		if r.URL.Path == "/v1/health" ||
			r.URL.Path == "/dashboard" ||
			len(r.URL.Path) >= 10 && r.URL.Path[:10] == "/dashboard" {
			next.ServeHTTP(w, r)
			return
		}

		if cfg.Server.Auth.Type == "none" || cfg.Server.Auth.Type == "" {
			next.ServeHTTP(w, r)
			return
		}

		tokenStr := bearerToken(r)
		if tokenStr == "" {
			writeError(w, http.StatusUnauthorized, "missing or malformed Authorization header")
			return
		}

		secret := []byte(cfg.Server.Auth.Secret)
		_, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return secret, nil
		}, jwt.WithValidMethods([]string{"HS256"}))

		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid token: "+err.Error())
			return
		}

		next.ServeHTTP(w, r)
	})
}

// bearerToken extracts the token string from "Authorization: Bearer <token>".
func bearerToken(r *http.Request) string {
	hdr := r.Header.Get("Authorization")
	if !strings.HasPrefix(hdr, "Bearer ") {
		return ""
	}
	t := strings.TrimPrefix(hdr, "Bearer ")
	if t == "" {
		return ""
	}
	return t
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg}) //nolint:errcheck
}

// writeJSON writes v as JSON with the given status code.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}
