package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/admin"
)

type alertsResponse struct {
	Alerts []alertDTO `json:"alerts"`
	Total  int        `json:"total"`
}

// defaultAlertsLimit is the legacy alerts page size (spec §1.1).
const defaultAlertsLimit = 50

func (s *server) notificationsGetSettings(w http.ResponseWriter, r *http.Request) {
	if s.adminUnavailable(w, r) {
		return
	}
	settings, err := s.admin.Notifications.GetSettings(r.Context())
	if err != nil {
		internalError(w, s.log(r), err)
		return
	}
	if settings == nil {
		writeJSON(s.log(r), w, http.StatusOK, nil)
		return
	}
	writeJSON(s.log(r), w, http.StatusOK, toNotificationSettingsDTO(*settings))
}

func (s *server) notificationsUpdateSettings(w http.ResponseWriter, r *http.Request) {
	if s.adminUnavailable(w, r) {
		return
	}
	var raw map[string]json.RawMessage
	if !s.decodeJSON(w, r, &raw) {
		return
	}
	values, err := notificationUpdateValues(raw)
	if err != nil {
		invalidRequest(w, s.log(r), err.Error())
		return
	}
	settings, err := s.admin.Notifications.UpdateSettings(r.Context(), values)
	if err != nil {
		internalError(w, s.log(r), err)
		return
	}
	writeJSON(s.log(r), w, http.StatusOK, toNotificationSettingsDTO(*settings))
}

func (s *server) notificationsAlerts(w http.ResponseWriter, r *http.Request) {
	if s.adminUnavailable(w, r) {
		return
	}
	limit, offset, err := parsePagination(r, defaultAlertsLimit)
	if err != nil {
		invalidRequest(w, s.log(r), err.Error())
		return
	}
	q := admin.AlertQuery{Limit: limit, Offset: offset}
	if raw := r.URL.Query().Get("connectionId"); raw != "" {
		id, convErr := strconv.Atoi(raw)
		if convErr != nil || id <= 0 {
			invalidRequest(w, s.log(r), "connectionId must be a positive integer")
			return
		}
		q.ConnectionID = id
	}
	rows, total, err := s.admin.Notifications.ListAlerts(r.Context(), q)
	if err != nil {
		internalError(w, s.log(r), err)
		return
	}
	alerts := make([]alertDTO, 0, len(rows))
	for _, a := range rows {
		alerts = append(alerts, toAlertDTO(a))
	}
	writeJSON(s.log(r), w, http.StatusOK, alertsResponse{Alerts: alerts, Total: total})
}

// notificationsFailureStates exposes the in-memory failure state. The dispatch
// path that populates it is out of Phase 5 scope, so the map is empty and the
// endpoint is marked [needs live validation] (spec §6.5 R6).
func (s *server) notificationsFailureStates(w http.ResponseWriter, r *http.Request) {
	if s.adminUnavailable(w, r) {
		return
	}
	writeJSON(s.log(r), w, http.StatusOK, map[string]any{})
}

// notificationUpdateValues maps a partial JSON body to whitelisted columns.
// smtpPass is write-only: an empty value is ignored (no clear, R8).
func notificationUpdateValues(raw map[string]json.RawMessage) (map[string]any, error) {
	values := make(map[string]any, len(raw))
	for key, val := range raw {
		if _, ok := admin.NotificationColumns[key]; !ok {
			return nil, errors.New("unknown field " + key)
		}
		switch key {
		case "enabled", "whatsappEnabled", "emailEnabled", "smtpSecure":
			var b bool
			if err := json.Unmarshal(val, &b); err != nil {
				return nil, errors.New("field " + key + " must be a boolean")
			}
			values[key] = b
		case "failureThreshold", "cooldownMinutes", "smtpPort", "whatsappConnectionId":
			var n int
			if err := json.Unmarshal(val, &n); err != nil {
				return nil, errors.New("field " + key + " must be an integer")
			}
			values[key] = n
		case "smtpPass":
			var str string
			if err := json.Unmarshal(val, &str); err != nil {
				return nil, errors.New("field smtpPass must be a string")
			}
			if str != "" {
				values[key] = str
			}
		default:
			var str string
			if err := json.Unmarshal(val, &str); err != nil {
				return nil, errors.New("field " + key + " must be a string")
			}
			values[key] = str
		}
	}
	return values, nil
}
