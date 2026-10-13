package httpapi

import (
	"net/http"
	"strconv"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/admin"
)

// defaultLogsLimit is the legacy logs page size (spec §1.1).
const defaultLogsLimit = 50

type logsResponse struct {
	Logs  []logDTO `json:"logs"`
	Total int      `json:"total"`
}

// logsList returns a filtered, paginated page of message_logs.
func (s *server) logsList(w http.ResponseWriter, r *http.Request) {
	if s.adminUnavailable(w, r) {
		return
	}
	limit, offset, err := parsePagination(r, defaultLogsLimit)
	if err != nil {
		invalidRequest(w, s.log(r), err.Error())
		return
	}
	q := admin.LogQuery{
		Direction: r.URL.Query().Get("direction"),
		Status:    r.URL.Query().Get("status"),
		Search:    r.URL.Query().Get("search"),
		Limit:     limit,
		Offset:    offset,
	}
	if raw := r.URL.Query().Get("connectionId"); raw != "" {
		id, convErr := strconv.Atoi(raw)
		if convErr != nil || id <= 0 {
			invalidRequest(w, s.log(r), "connectionId must be a positive integer")
			return
		}
		q.ConnectionID = id
	}
	rows, total, err := s.admin.Logs.List(r.Context(), q)
	if err != nil {
		internalError(w, s.log(r), err)
		return
	}
	logs := make([]logDTO, 0, len(rows))
	for _, row := range rows {
		logs = append(logs, toLogDTO(row))
	}
	writeJSON(s.log(r), w, http.StatusOK, logsResponse{Logs: logs, Total: total})
}
