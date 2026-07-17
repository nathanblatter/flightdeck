package api

import (
	"net/http"
	"strconv"
)

// usageReport serves the same analytics bundle as the MCP usage_report tool
// (minus unused-tool detection, which needs the MCP tool registry).
func (s *Server) usageReport(w http.ResponseWriter, r *http.Request) {
	days := 7
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "invalid days")
			return
		}
		days = n
	}
	rep, err := s.Svc.UsageReport(r.Context(), days, nil)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}
