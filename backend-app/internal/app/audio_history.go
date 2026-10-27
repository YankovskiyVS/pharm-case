package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

func (s *Server) audioRuns(w http.ResponseWriter, r *http.Request) {
	patient, id := r.PathValue("id"), r.PathValue("audio")
	if !s.authorized(r, patient) || !uuid(id) {
		writeError(w, 403, "Нет доступа к истории аудио")
		return
	}
	if err := s.recordRead(r, "audio.runs", id); err != nil {
		s.fail(w, r, err)
		return
	}
	s.listPage(w, r, `SELECT j.id,j.recording_id,j.status,j.attempts,j.error_code,j.created_at,j.finished_at,j.result,j.result->>'pipeline' pipeline FROM audio_runs j JOIN audio_recordings a ON a.id=j.recording_id WHERE a.id=$1 AND a.patient_id=$2`, id, patient)
}
func (s *Server) audioStatus(w http.ResponseWriter, r *http.Request) {
	if s.config.AudioURL == "" {
		writeJSON(w, 200, map[string]any{"configured": false, "status": "not_ready", "models": map[string]any{}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.config.AudioURL, "/")+"/status", nil)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+s.config.AudioToken)
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		writeError(w, 503, "Аудиосервис недоступен")
		return
	}
	defer func() {
		if e := response.Body.Close(); e != nil {
			slog.Debug("audio status response close failed")
		}
	}()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil || response.StatusCode != 200 || !json.Valid(raw) {
		writeError(w, 503, "Статус моделей недоступен")
		return
	}
	// /status is a dedicated metadata-only service contract, never a clinical endpoint.
	var status struct {
		Status   string          `json:"status"`
		Models   json.RawMessage `json:"models"`
		Active   json.RawMessage `json:"active"`
		Cache    json.RawMessage `json:"cache"`
		Pipeline string          `json:"pipeline"`
	}
	if err = json.Unmarshal(raw, &status); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, status)
}
