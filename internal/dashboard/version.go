package dashboard

import (
	"encoding/json"
	"net/http"
)

type versionResponse struct {
	Version string `json:"version"`
}

// VersionHandler reports the server release that native clients must match.
// It is public so an app can check compatibility before authentication.
func VersionHandler(version string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(versionResponse{Version: version})
	}
}
