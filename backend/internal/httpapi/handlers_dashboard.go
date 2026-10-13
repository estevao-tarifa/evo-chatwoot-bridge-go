package httpapi

import (
	"net/http"
)

type statsDTO struct {
	TotalConnections  int      `json:"totalConnections"`
	ActiveConnections int      `json:"activeConnections"`
	TotalMessages     int64    `json:"totalMessages"`
	FailedMessages    int64    `json:"failedMessages"`
	TodayMessages     int64    `json:"todayMessages"`
	RecentLogs        []logDTO `json:"recentLogs"`
}

type hourBucketDTO struct {
	Hour     string `json:"hour"`
	Incoming int64  `json:"incoming"`
	Outgoing int64  `json:"outgoing"`
	Total    int64  `json:"total"`
}

type dayBucketDTO struct {
	Day      string `json:"day"`
	Incoming int64  `json:"incoming"`
	Outgoing int64  `json:"outgoing"`
	Total    int64  `json:"total"`
}

type typeCountDTO struct {
	Type  string `json:"type"`
	Count int64  `json:"count"`
}

func (s *server) dashboardStats(w http.ResponseWriter, r *http.Request) {
	if s.adminUnavailable(w, r) {
		return
	}
	stats, err := s.admin.Dashboard.Stats(r.Context())
	if err != nil {
		internalError(w, s.log(r), err)
		return
	}
	logs := make([]logDTO, 0, len(stats.RecentLogs))
	for _, l := range stats.RecentLogs {
		logs = append(logs, toLogDTO(l))
	}
	writeJSON(s.log(r), w, http.StatusOK, statsDTO{
		TotalConnections:  stats.TotalConnections,
		ActiveConnections: stats.ActiveConnections,
		TotalMessages:     stats.TotalMessages,
		FailedMessages:    stats.FailedMessages,
		TodayMessages:     stats.TodayMessages,
		RecentLogs:        logs,
	})
}

func (s *server) dashboardMessagesPerHour(w http.ResponseWriter, r *http.Request) {
	if s.adminUnavailable(w, r) {
		return
	}
	buckets, err := s.admin.Dashboard.MessagesPerHour(r.Context())
	if err != nil {
		internalError(w, s.log(r), err)
		return
	}
	out := make([]hourBucketDTO, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, hourBucketDTO{Hour: b.Label, Incoming: b.Incoming, Outgoing: b.Outgoing, Total: b.Total})
	}
	writeJSON(s.log(r), w, http.StatusOK, out)
}

func (s *server) dashboardMessagesPerDay(w http.ResponseWriter, r *http.Request) {
	if s.adminUnavailable(w, r) {
		return
	}
	buckets, err := s.admin.Dashboard.MessagesPerDay(r.Context())
	if err != nil {
		internalError(w, s.log(r), err)
		return
	}
	out := make([]dayBucketDTO, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, dayBucketDTO{Day: b.Label, Incoming: b.Incoming, Outgoing: b.Outgoing, Total: b.Total})
	}
	writeJSON(s.log(r), w, http.StatusOK, out)
}

func (s *server) dashboardMessageTypes(w http.ResponseWriter, r *http.Request) {
	if s.adminUnavailable(w, r) {
		return
	}
	counts, err := s.admin.Dashboard.MessageTypes(r.Context())
	if err != nil {
		internalError(w, s.log(r), err)
		return
	}
	out := make([]typeCountDTO, 0, len(counts))
	for _, c := range counts {
		out = append(out, typeCountDTO{Type: c.Type, Count: c.Count})
	}
	writeJSON(s.log(r), w, http.StatusOK, out)
}
