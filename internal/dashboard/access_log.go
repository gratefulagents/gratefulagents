package dashboard

import (
	"net/http"
	"strings"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// AccessLogMiddleware logs every HTTP request that reaches the dashboard
// server, including requests the RPC interceptors never see: JWT middleware
// rejections (401s), CORS preflights, the public config endpoint, and static
// asset serving. API traffic is logged at Info; successful static/preflight
// traffic is demoted to V(1) so it can be enabled without drowning the log.
func AccessLogMiddleware() func(http.Handler) http.Handler {
	log := logf.Log.WithName("dashboard.http")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)

			status := ww.Status()
			if status == 0 {
				// Handlers that never call WriteHeader implicitly return 200.
				status = http.StatusOK
			}
			fields := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"status", status,
				"duration", time.Since(start).Round(time.Millisecond).String(),
				"bytes", ww.BytesWritten(),
				"remote", r.RemoteAddr,
			}
			switch {
			case status >= 400:
				log.Info("http request failed", fields...)
			case isAPIPath(r.URL.Path) && r.Method != http.MethodOptions:
				log.Info("http request", fields...)
			default:
				log.V(1).Info("http request", fields...)
			}
		})
	}
}

// isAPIPath reports whether the path belongs to the API surface (Connect
// services and the public config endpoint) rather than static UI assets.
func isAPIPath(path string) bool {
	return strings.HasPrefix(path, "/api/") ||
		strings.HasPrefix(path, "/platform.") ||
		strings.HasPrefix(path, "/auth.")
}
