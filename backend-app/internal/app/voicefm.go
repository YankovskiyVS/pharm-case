package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func ValidateVoiceFMConfig(endpoint, token, mode string) error {
	if endpoint == "" {
		return nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || len(token) < 32 || (mode != "mock" && mode != "real" && mode != "disabled") {
		return errors.New("invalid private VoiceFM configuration")
	}
	return nil
}
func (s *Server) voiceFMMode() string {
	if s.config.VoiceFMURL == "" {
		return "disabled"
	}
	if s.config.VoiceFMMode == "" {
		return "mock"
	}
	return s.config.VoiceFMMode
}
func (s *Server) voiceFMCall(ctx context.Context, method, path string, data []byte, contentType, task string, budget time.Duration) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(s.config.VoiceFMURL, "/")+path, bytes.NewReader(data))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+s.config.VoiceFMToken)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if task != "" {
		if task == "free" {
			task = "free_speech"
		}
		req.Header.Set("X-Voice-Task", task)
	}
	client := &http.Client{Timeout: budget, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if len(raw) > 2<<20 {
		return nil, response.StatusCode, errors.New("VoiceFM response exceeds limit")
	}
	return raw, response.StatusCode, err
}
func (s *Server) voiceFMStatus(w http.ResponseWriter, r *http.Request) {
	if s.config.VoiceFMURL == "" {
		writeJSON(w, 200, map[string]any{"status": "model_unavailable", "ready": false, "mode": "disabled", "is_mock": false, "clinical_use": false, "reason_code": "not_configured", "limitations": []string{"VoiceFM не настроен. Клинический контекст и описательные отчёты доступны без модели."}})
		return
	}
	raw, status, err := s.voiceFMCall(r.Context(), http.MethodGet, "/status", nil, "", "", 5*time.Second)
	if err != nil || (status != 200 && status != 503) || !json.Valid(raw) {
		writeJSON(w, 200, map[string]any{"status": "model_unavailable", "ready": false, "mode": s.voiceFMMode(), "is_mock": s.voiceFMMode() == "mock", "clinical_use": false, "reason_code": "service_unavailable", "limitations": []string{"Сервис VoiceFM недоступен. Описательный отчёт и заключение врача остаются доступны."}})
		return
	}
	var metadata struct {
		Mode   string `json:"mode"`
		Status string `json:"status"`
		Ready  *bool  `json:"ready"`
		IsMock *bool  `json:"is_mock"`
	}
	if json.Unmarshal(raw, &metadata) != nil || metadata.Ready == nil || metadata.Status == "" || metadata.Mode != s.voiceFMMode() || metadata.IsMock == nil || *metadata.IsMock != (s.voiceFMMode() == "mock") {
		writeJSON(w, 200, map[string]any{"status": "model_unavailable", "ready": false, "mode": s.voiceFMMode(), "is_mock": s.voiceFMMode() == "mock", "clinical_use": false, "reason_code": "mode_mismatch", "limitations": []string{"Режим приватного сервиса не совпадает с настройкой приложения; результат не будет опубликован."}})
		return
	}
	writeJSON(w, 200, json.RawMessage(raw))
}

// RunVoiceFM is independent of audio/diary/risk workers. A database advisory
// claim bounds inference to one active job across API replicas.
func (s *Server) RunVoiceFM(ctx context.Context) {
	if s.config.VoiceFMURL == "" {
		return
	}
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := s.processVoiceFM(ctx); err != nil && ctx.Err() == nil {
				slog.Error("VoiceFM report attempt failed", "error_type", fmt.Sprintf("%T", err))
			}
		}
	}
}

var voiceFMReasonPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,79}$`)
var voiceFMLabels = map[string]bool{"gsd_control": true, "cat_voice": true, "cat_neuro": true, "cat_mood": true, "cat_respiratory": true, "gsd_parkinsons": true, "gsd_alz_dementia_mci": true, "gsd_mtd": true, "gsd_copd_asthma": true, "gsd_depression": true, "gsd_airway_stenosis": true, "gsd_benign_lesion": true, "gsd_anxiety": true, "gsd_laryngeal_dystonia": true}

func safeVoiceFMReason(reason string) string {
	if !voiceFMReasonPattern.MatchString(reason) {
		return "model_unavailable"
	}
	return reason
}
func validateVoiceFMResult(raw []byte, expectedMode string) bool {
	var v struct {
		Status       string   `json:"status"`
		Mode         string   `json:"mode"`
		IsMock       *bool    `json:"is_mock"`
		ClinicalUse  *bool    `json:"clinical_use"`
		InputUsed    *bool    `json:"input_used_for_prediction"`
		ResearchOnly *bool    `json:"research_only"`
		Limitations  []string `json:"limitations"`
		Model        struct {
			Name     string `json:"name"`
			Revision string `json:"revision"`
		} `json:"model"`
		Outputs struct {
			Interpretation string `json:"interpretation"`
			Scores         []struct {
				Label string   `json:"label"`
				Score *float64 `json:"score"`
			} `json:"scores"`
		} `json:"outputs"`
	}
	if json.Unmarshal(raw, &v) != nil || v.Status != "ready" || v.Mode != expectedMode || (v.Mode != "mock" && v.Mode != "real") || v.IsMock == nil || *v.IsMock != (v.Mode == "mock") || v.ClinicalUse == nil || *v.ClinicalUse || v.InputUsed == nil || *v.InputUsed == (v.Mode == "mock") || v.Model.Name == "" || v.Model.Revision == "" || len(v.Outputs.Scores) == 0 || len(v.Outputs.Scores) > len(voiceFMLabels) || v.ResearchOnly == nil || !*v.ResearchOnly || len(v.Limitations) == 0 || (v.Mode == "mock" && strings.TrimSpace(v.Outputs.Interpretation) == "") {
		return false
	}
	for _, limitation := range v.Limitations {
		if strings.TrimSpace(limitation) == "" {
			return false
		}
	}
	seen := map[string]bool{}
	for _, score := range v.Outputs.Scores {
		if !voiceFMLabels[score.Label] || seen[score.Label] || score.Score == nil || math.IsNaN(*score.Score) || math.IsInf(*score.Score, 0) || *score.Score < 0 || *score.Score > 1 {
			return false
		}
		seen[score.Label] = true
	}
	return true
}
func (s *Server) processVoiceFM(ctx context.Context) error {
	connection, err := s.db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer connection.Release()
	var locked bool
	if err = connection.QueryRow(ctx, `SELECT pg_try_advisory_lock(7310627,8)`).Scan(&locked); err != nil || !locked {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, unlockErr := connection.Exec(cleanup, `SELECT pg_advisory_unlock(7310627,8)`); unlockErr != nil {
			_ = connection.Conn().Close(cleanup)
		}
	}()
	if _, err = connection.Exec(ctx, `UPDATE voicefm_jobs SET status='failed',reason_code='processing_timeout',finished_at=now(),lease_token=NULL,lease_until=NULL WHERE status='processing' AND lease_until<now() AND attempts>=3`); err != nil {
		return err
	}
	lease := token()
	var job, report, object, contentType, task, expectedMode, expectedHash, patient, doctor string
	err = connection.QueryRow(ctx, `WITH candidate AS(SELECT id FROM voicefm_jobs WHERE attempts<3 AND ((status='queued' AND available_at<=now()) OR(status='processing' AND lease_until<now())) ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED),claimed AS(UPDATE voicefm_jobs j SET status='processing',attempts=attempts+1,lease_token=$1,lease_until=now()+interval '2 minutes' FROM candidate c WHERE j.id=c.id RETURNING j.id,j.report_id) SELECT c.id::text,cr.id::text,a.object_key,a.mime,a.task,cr.source_snapshot->'model_request'->>'mode',cr.source_snapshot->'audio'->>'sha256',cr.patient_id::text,cr.author_id::text FROM claimed c JOIN clinical_audio_reports cr ON cr.id=c.report_id JOIN audio_recordings a ON a.id=cr.recording_id`, lease).Scan(&job, &report, &object, &contentType, &task, &expectedMode, &expectedHash, &patient, &doctor)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if expectedMode != s.voiceFMMode() {
		_, err = connection.Exec(ctx, `UPDATE voicefm_jobs SET status='model_unavailable',reason_code='mode_mismatch',finished_at=now(),lease_token=NULL,lease_until=NULL WHERE id=$1 AND lease_token=$2 AND status='processing'`, job, lease)
		return err
	}
	var active bool
	if err = connection.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM clinical_audio_reports cr JOIN users d ON d.id=cr.author_id JOIN users p ON p.id=cr.patient_id JOIN care_links c ON c.doctor_id=d.id AND c.patient_id=p.id WHERE cr.id=$1 AND d.active AND d.role='doctor' AND p.active AND p.role='patient' AND c.active)`, report).Scan(&active); err != nil {
		return err
	}
	if !active {
		_, err = connection.Exec(ctx, `UPDATE voicefm_jobs SET status='model_unavailable',reason_code='access_revoked',finished_at=now(),lease_token=NULL,lease_until=NULL WHERE id=$1 AND lease_token=$2`, job, lease)
		return err
	}
	data, readErr := os.ReadFile(filepath.Join(s.config.AudioDir, object))
	if readErr == nil && hash(string(data)) != expectedHash {
		readErr = errors.New("source audio integrity mismatch")
	}
	var raw []byte
	var status int
	callErr := readErr
	if callErr == nil {
		raw, status, callErr = s.voiceFMCall(ctx, http.MethodPost, "/analyze", data, contentType, task, 90*time.Second)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if callErr != nil || status != 200 || !validateVoiceFMResult(raw, expectedMode) {
		reason := "service_unavailable"
		permanent := readErr != nil
		unavailable := false
		if readErr != nil {
			reason = "source_audio_unavailable"
		}
		if callErr == nil && status == 503 {
			var body struct {
				Status string `json:"status"`
				Reason string `json:"reason_code"`
			}
			if json.Unmarshal(raw, &body) == nil && body.Status == "model_unavailable" {
				reason = safeVoiceFMReason(body.Reason)
				if reason != "busy" && reason != "loading" && reason != "warming" && reason != "service_unavailable" && reason != "timeout" && reason != "inference_busy" {
					permanent = true
					unavailable = true
				}
			}
		}
		if status == 401 || status == 403 {
			reason = "model_access_denied"
			permanent = true
			unavailable = true
		}
		if status == 422 || status == 400 {
			reason = "unsupported_audio"
			permanent = true
		}
		if callErr == nil && status == 200 {
			reason = "invalid_model_result"
			permanent = true
			var received struct {
				Mode string `json:"mode"`
			}
			if json.Unmarshal(raw, &received) == nil && received.Mode != "" && received.Mode != expectedMode {
				reason = "mode_mismatch"
				unavailable = true
			}
		}
		_, err = connection.Exec(ctx, `UPDATE voicefm_jobs SET status=CASE WHEN $5 THEN 'model_unavailable' WHEN attempts>=3 OR $4 THEN 'failed' ELSE 'queued' END,reason_code=$3,available_at=now()+interval '30 seconds',lease_token=NULL,lease_until=NULL,finished_at=CASE WHEN attempts>=3 OR $4 THEN now() ELSE NULL END WHERE id=$1 AND lease_token=$2 AND status='processing'`, job, lease, reason, permanent, unavailable)
		return err
	}
	tx, err := connection.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx, ctx)

	if accessErr := lockClinicalAccess(ctx, tx, patient, doctor); accessErr != nil {
		if !errors.Is(accessErr, pgx.ErrNoRows) {
			return accessErr
		}
		if _, err = tx.Exec(ctx, `UPDATE voicefm_jobs SET status='model_unavailable',reason_code='access_revoked',finished_at=now(),lease_token=NULL,lease_until=NULL WHERE id=$1 AND lease_token=$2 AND status='processing'`, job, lease); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	tag, err := tx.Exec(ctx, `UPDATE voicefm_jobs SET status='ready',reason_code='',finished_at=now(),lease_token=NULL,lease_until=NULL WHERE id=$1 AND lease_token=$2 AND status='processing' AND lease_until>now()`, job, lease)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	if _, err = tx.Exec(ctx, `INSERT INTO voicefm_results(report_id,result) VALUES($1,$2)`, report, raw); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
