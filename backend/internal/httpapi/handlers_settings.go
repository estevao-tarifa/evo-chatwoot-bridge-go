package httpapi

import "net/http"

type bridgeURLResponse struct {
	URL    *string `json:"url"`
	Source string  `json:"source"`
}

// settingsBridgeURL reports the bridge URL and where it comes from. It never
// invents a value: an unset URL is null with source "none" (legacy parity).
func (s *server) settingsBridgeURL(w http.ResponseWriter, r *http.Request) {
	if s.adminUnavailable(w, r) {
		return
	}
	stored, err := s.admin.Settings.BridgeURL(r.Context())
	if err != nil {
		internalError(w, s.log(r), err)
		return
	}
	switch {
	case stored != "":
		writeJSON(s.log(r), w, http.StatusOK, bridgeURLResponse{URL: &stored, Source: "database"})
	case s.admin.BridgeURL != "":
		env := s.admin.BridgeURL
		writeJSON(s.log(r), w, http.StatusOK, bridgeURLResponse{URL: &env, Source: "env"})
	default:
		writeJSON(s.log(r), w, http.StatusOK, bridgeURLResponse{Source: "none"})
	}
}
