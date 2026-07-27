package auth

import (
	"context"
	"net/http"
	"strings"
)

type ctxKey int

const userIDKey ctxKey = 1

// UserIDFromContext returns the authenticated user id, or 0.
func UserIDFromContext(ctx context.Context) int64 {
	v, _ := ctx.Value(userIDKey).(int64)
	return v
}

// Middleware validates Authorization: Bearer <jwt> and injects user_id.
func Middleware(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			if h == "" {
				http.Error(w, `{"error":"missing authorization"}`, http.StatusUnauthorized)
				return
			}
			const prefix = "Bearer "
			if !strings.HasPrefix(h, prefix) {
				http.Error(w, `{"error":"invalid authorization scheme"}`, http.StatusUnauthorized)
				return
			}
			token := strings.TrimSpace(strings.TrimPrefix(h, prefix))
			claims, err := ParseToken(secret, token)
			if err != nil {
				http.Error(w, `{"error":"invalid or expired token"}`, http.StatusUnauthorized)
				return
			}
			ctx := context.WithValue(r.Context(), userIDKey, claims.UserID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
