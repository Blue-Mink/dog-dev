package api

import (
	"net/http"
	"strconv"

	"github.com/ghpp/ghpp/internal/trace"
)

// handleTrace 返回结构化明细（请求/换源/流量）。
// GET /api/trace?type=request&limit=200
// type 省略或 all 表示全部；limit 省略默认 300，最大 2000。
func (s *Server) handleTrace(w http.ResponseWriter, r *http.Request) {
	limit := 300
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 2000 {
			limit = n
		}
	}
	kind := trace.Kind(r.URL.Query().Get("type"))
	switch kind {
	case "", "all":
		kind = ""
	case trace.KindRequest, trace.KindFailover, trace.KindTransfer:
	default:
		writeError(w, http.StatusBadRequest, "无效的明细类型: %s", kind)
		return
	}

	rec := s.app.Tracer()
	if rec == nil {
		writeOK(w, []trace.Event{})
		return
	}
	writeOK(w, rec.Recent(limit, kind))
}
