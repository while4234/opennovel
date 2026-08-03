package web

import (
	"net/http"
	"strconv"
	"strings"

	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func (s *Server) handleProjectAdaptAuditHistory(w http.ResponseWriter, r *http.Request, id, suffix string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	manifest, err := s.store.OpenProject(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	st := storepkg.NewStore(manifest.OutputDir)
	suffix = strings.Trim(strings.TrimSpace(suffix), "/")
	if suffix == "compare" {
		baseID := firstQueryValue(r, "base_run_id", "base")
		candidateID := firstQueryValue(r, "candidate_run_id", "candidate")
		if baseID == "" || candidateID == "" {
			writeError(w, http.StatusBadRequest, "base and candidate audit run ids are required")
			return
		}
		comparison, compareErr := st.Adaptation.CompareAuditRuns(baseID, candidateID)
		if compareErr != nil {
			writeError(w, http.StatusBadRequest, compareErr.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"comparison": comparison})
		return
	}
	if suffix != "" {
		run, loadErr := st.Adaptation.LoadAuditRun(suffix)
		if loadErr != nil {
			writeError(w, http.StatusInternalServerError, loadErr.Error())
			return
		}
		if run == nil {
			writeError(w, http.StatusNotFound, "audit run not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"run": run})
		return
	}
	entries, listErr := st.Adaptation.ListAuditRuns()
	if listErr != nil {
		writeError(w, http.StatusInternalServerError, listErr.Error())
		return
	}
	offset := positiveQueryInt(r, "offset", 0, len(entries))
	limit := positiveQueryInt(r, "limit", 50, 100)
	end := min(offset+limit, len(entries))
	if offset > len(entries) {
		offset = len(entries)
		end = offset
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": entries[offset:end], "total": len(entries), "offset": offset, "limit": limit})
}

func firstQueryValue(r *http.Request, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(r.URL.Query().Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func positiveQueryInt(r *http.Request, key string, fallback, maximum int) int {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return fallback
	}
	if maximum > 0 && value > maximum {
		return maximum
	}
	return value
}
