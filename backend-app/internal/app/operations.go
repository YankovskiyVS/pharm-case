package app

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
)

func (s *Server) ops(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.config.OpsToken) < 32 {
			writeError(w, 503, "Операторский доступ не настроен")
			return
		}
		supplied := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(supplied), []byte(s.config.OpsToken)) != 1 {
			writeError(w, 401, "Требуется операторский токен")
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) opsStatus(w http.ResponseWriter, r *http.Request) {
	queues, err := s.jsonQuery(r.Context(), `SELECT jsonb_build_object(
 'unassigned_risks',(SELECT count(*) FROM operational_exceptions WHERE kind='unassigned_risk' AND status='open'),
 'exceptions',(SELECT coalesce(jsonb_agg(jsonb_build_object('id',id,'kind',kind,'code',safe_code,'attempts',attempts,'first_seen_at',first_seen_at,'last_seen_at',last_seen_at) ORDER BY first_seen_at),'[]'::jsonb) FROM (SELECT id,kind,safe_code,attempts,first_seen_at,last_seen_at FROM operational_exceptions WHERE status='open' ORDER BY first_seen_at LIMIT 100) visible),
 'pending_events',(SELECT count(*) FROM outbox_events e WHERE NOT EXISTS(SELECT 1 FROM event_deliveries d WHERE d.event_id=e.id AND d.consumer='in-app-v1')),
 'oldest_pending_event_at',(SELECT min(created_at) FROM outbox_events e WHERE NOT EXISTS(SELECT 1 FROM event_deliveries d WHERE d.event_id=e.id AND d.consumer='in-app-v1')),
 'oldest_pending_email_at',(SELECT min(created_at) FROM email_jobs WHERE status IN ('pending','sending')),
 'retrying_email',(SELECT count(*) FROM email_jobs WHERE status='pending' AND attempts>0),
 'email_jobs',(SELECT coalesce(jsonb_agg(to_jsonb(j)),'[]'::jsonb) FROM (SELECT id,status,attempts,last_error_code,created_at,available_at FROM email_jobs WHERE status IN ('pending','sending') ORDER BY created_at LIMIT 100) j),
 'voicefm_pending',(SELECT count(*) FROM voicefm_jobs WHERE status IN ('queued','processing')),
 'voicefm_failed',(SELECT count(*) FROM voicefm_jobs WHERE status='failed'),
 'voicefm_unavailable',(SELECT count(*) FROM voicefm_jobs WHERE status='model_unavailable'),
 'audio_pending',(SELECT count(*) FROM audio_runs WHERE status IN ('queued','processing')),
 'audio_failed',(SELECT count(*) FROM audio_runs WHERE status='failed'))`)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	email, err := s.EmailQueueStats(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := "ok"
	var q struct {
		Risks int `json:"unassigned_risks"`
		Email int `json:"retrying_email"`
	}
	if err = json.Unmarshal(queues, &q); err != nil {
		s.fail(w, r, err)
		return
	}
	if q.Risks > 0 || q.Email > 0 {
		status = "degraded"
	}
	writeJSON(w, 200, map[string]any{"status": status, "queues": queues, "email": email, "email_enabled": s.config.Email != nil && s.config.Email.Enabled, "email_provider": "gmail", "email_state": s.emailProviderState(), "support_chat": s.supportProvider()})
}
func (s *Server) opsRetryEmail(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs []string `json:"ids"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.IDs) < 1 || len(in.IDs) > 100 {
		writeError(w, 400, "Укажите 1–100 задач")
		return
	}
	for _, id := range in.IDs {
		if !uuid(id) {
			writeError(w, 400, "Некорректная задача")
			return
		}
	}
	tag, err := s.db.Exec(r.Context(), `UPDATE email_jobs SET available_at=now(),leased_until=NULL,lease_token=NULL,status='pending',failure_notified_at=NULL WHERE id=ANY($1::uuid[]) AND (status='pending' OR (status='sending' AND leased_until<now()))`, in.IDs)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]int64{"requeued": tag.RowsAffected()})
}
func (s *Server) noteOperational(ctx context.Context, kind, aggregate, code string) error {
	var id string
	err := s.db.QueryRow(ctx, `INSERT INTO operational_exceptions(kind,aggregate_id,safe_code) VALUES($1,$2,$3) ON CONFLICT(kind,aggregate_id) DO UPDATE SET status='open',attempts=operational_exceptions.attempts+1,last_seen_at=now(),resolved_at=NULL RETURNING id`, kind, aggregate, code).Scan(&id)
	if err != nil {
		return err
	}
	if s.config.OperatorEmail == "" || s.config.Email == nil || !s.config.Email.Enabled {
		return nil
	}
	_, err = s.db.Exec(ctx, `INSERT INTO email_jobs(source_type,source_id,recipient_key,recipient_email,priority) VALUES('operational',$1,'operator',$2,90) ON CONFLICT DO NOTHING`, id, s.config.OperatorEmail)
	return err
}

func (s *Server) emailProviderState() string {
	if s.config.Email == nil || !s.config.Email.Enabled {
		return "disabled"
	}
	return "configured"
}
