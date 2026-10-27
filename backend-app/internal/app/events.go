package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// RunBackground dispatches persisted events and creates due reminders until cancelled.
func (s *Server) RunBackground(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := s.dispatch(ctx); err != nil && ctx.Err() == nil {
				slog.Error("event dispatch failed", "error_type", fmt.Sprintf("%T", err))
			}
			if err := s.reminders(ctx); err != nil && ctx.Err() == nil {
				slog.Error("reminder scan failed", "error_type", fmt.Sprintf("%T", err))
			}
		}
	}
}
func (s *Server) dispatch(ctx context.Context) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx, ctx)
	rows, err := tx.Query(ctx, `WITH candidates AS MATERIALIZED (
 SELECT e.id,e.type,e.patient_id,e.created_at FROM outbox_events e
 WHERE NOT EXISTS(SELECT 1 FROM event_deliveries d WHERE d.event_id=e.id AND d.consumer='in-app-v1')
 AND NOT EXISTS(SELECT 1 FROM operational_exceptions o WHERE o.aggregate_id=e.aggregate_id AND o.kind='unassigned_risk' AND o.status='open' AND o.last_seen_at>now()-interval '30 seconds')
 ORDER BY (e.type IN ('risk.raised','deterioration.raised')) DESC,e.created_at LIMIT 50
 ) SELECT id::text,type,patient_id::text FROM candidates
 WHERE pg_try_advisory_xact_lock(hashtextextended('notification-dispatch:'||id::text,0))`)
	if err != nil {
		return err
	}
	type event struct{ id, kind, patient string }
	events := []event{}
	for rows.Next() {
		var e event
		if err = rows.Scan(&e.id, &e.kind, &e.patient); err != nil {
			break
		}
		events = append(events, e)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, e := range events {
		routed, routeErr := tx.Exec(ctx, `INSERT INTO notifications(recipient_id,patient_id,event_id,type)
 SELECT DISTINCT recipients.id,$2::uuid,$1::uuid,$3 FROM (
   SELECT c.doctor_id AS id FROM care_links c JOIN users d ON d.id=c.doctor_id
   WHERE c.patient_id=$2 AND c.active AND d.active AND d.role='doctor'
     AND $3 NOT IN ('reminder.due','assignment.changed','review.recorded')
 AND ($3!='review.overdue' OR EXISTS(SELECT 1 FROM weekly_review_reminders wr WHERE wr.event_id=(SELECT aggregate_id FROM outbox_events WHERE id=$1) AND wr.doctor_id=c.doctor_id))
     AND ($3!='message.sent' OR EXISTS(
       SELECT 1 FROM messages m WHERE m.id=(SELECT aggregate_id FROM outbox_events WHERE id=$1)
       AND m.care_link_id=c.id AND m.sender_id!=c.doctor_id))
   UNION ALL
   SELECT u.id FROM users u WHERE u.id=$2 AND u.active AND u.role='patient' AND $3 NOT IN ('risk.raised','deterioration.raised','review.overdue')
     AND ($3!='message.sent' OR EXISTS(
       SELECT 1 FROM messages m JOIN care_links c ON c.id=m.care_link_id
       WHERE m.id=(SELECT aggregate_id FROM outbox_events WHERE id=$1)
       AND c.patient_id=$2 AND c.active AND m.sender_id!=u.id))
 ) recipients ON CONFLICT(recipient_id,event_id) DO NOTHING`, e.id, e.patient, e.kind)
		if routeErr != nil {
			return routeErr
		}
		if (e.kind == "risk.raised" || e.kind == "deterioration.raised") && routed.RowsAffected() == 0 {
			var resolved bool
			if err = tx.QueryRow(ctx, `SELECT status='resolved' FROM alerts WHERE id=(SELECT aggregate_id FROM outbox_events WHERE id=$1)`, e.id).Scan(&resolved); err != nil {
				return err
			}
			if !resolved {
				var aggregate string
				if err = tx.QueryRow(ctx, `SELECT aggregate_id FROM outbox_events WHERE id=$1`, e.id).Scan(&aggregate); err != nil {
					return err
				}
				if err = s.noteOperational(ctx, "unassigned_risk", aggregate, "no_active_doctor"); err != nil {
					return err
				}
				continue
			}
		}
		if e.kind == "risk.raised" || e.kind == "deterioration.raised" {
			if _, err = tx.Exec(ctx, `UPDATE operational_exceptions SET status='resolved',resolved_at=now() WHERE kind='unassigned_risk' AND aggregate_id=(SELECT aggregate_id FROM outbox_events WHERE id=$1) AND status='open'`, e.id); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, `INSERT INTO event_deliveries(event_id,consumer) VALUES($1,'in-app-v1') ON CONFLICT DO NOTHING`, e.id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func (s *Server) reminders(ctx context.Context) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx, ctx)
	_, err = tx.Exec(ctx, `WITH due AS (
  INSERT INTO reminder_runs(assignment_id,local_date)
  SELECT a.id,(now() AT TIME ZONE u.timezone)::date FROM assignments a JOIN users u ON u.id=a.patient_id
  WHERE u.active AND a.status='active' AND mod(((now() AT TIME ZONE u.timezone)::date-a.start_date),a.interval_days)=0 AND (now() AT TIME ZONE u.timezone)::date BETWEEN a.start_date AND a.end_date
  AND (now() AT TIME ZONE u.timezone)::time>=a.due_time
  AND NOT EXISTS(SELECT 1 FROM current_entries e WHERE e.patient_id=a.patient_id AND e.kind=a.kind AND e.local_date=(now() AT TIME ZONE u.timezone)::date)
  ON CONFLICT DO NOTHING RETURNING id,assignment_id
 ) INSERT INTO outbox_events(type,patient_id,aggregate_id) SELECT 'reminder.due',a.patient_id,due.id FROM due JOIN assignments a ON a.id=due.assignment_id`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `WITH due AS (
 SELECT c.doctor_id,c.patient_id,((coalesce((SELECT max(created_at) FROM reviews r WHERE r.patient_id=c.patient_id AND r.doctor_id=c.doctor_id),c.created_at) AT TIME ZONE p.timezone)::date + 7 + (greatest(0,((now() AT TIME ZONE p.timezone)::date-(coalesce((SELECT max(created_at) FROM reviews r WHERE r.patient_id=c.patient_id AND r.doctor_id=c.doctor_id),c.created_at) AT TIME ZONE p.timezone)::date)-7)/7)*7) due_date
 FROM care_links c JOIN users p ON p.id=c.patient_id JOIN users d ON d.id=c.doctor_id
 WHERE c.active AND p.active AND d.active AND d.role='doctor'
 AND coalesce((SELECT max(created_at) FROM reviews r WHERE r.patient_id=c.patient_id AND r.doctor_id=c.doctor_id),c.created_at)<now()-interval '7 days'
 ), created AS (INSERT INTO weekly_review_reminders(doctor_id,patient_id,due_date) SELECT doctor_id,patient_id,due_date FROM due ON CONFLICT DO NOTHING RETURNING patient_id,event_id)
 INSERT INTO outbox_events(type,patient_id,aggregate_id) SELECT 'review.overdue',patient_id,event_id FROM created`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM sessions WHERE expires_at<now(); DELETE FROM login_attempts WHERE expires_at<now()`)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, "Поток недоступен")
		return
	}
	cursor, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	if cursor < 0 {
		cursor = 0
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil {
		return
	}
	flusher.Flush()
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	p := current(r)
	// Bounded stream duration ensures session expiry and revoked access get rechecked.
	end := time.NewTimer(2 * time.Minute)
	defer end.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-end.C:
			return
		case <-tick.C:
			var valid bool
			if err := s.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires_at>now() AND u.active AND u.role=$2 AND s.auth_role=u.role AND s.auth_version=u.auth_version)`, p.Session, p.Role).Scan(&valid); err != nil || !valid {
				return
			}
			var latest int64
			err := s.db.QueryRow(r.Context(), `SELECT COALESCE(max(n.seq),$2) FROM notifications n WHERE n.recipient_id=$1 AND n.seq>$2 AND ($3='patient' OR EXISTS(SELECT 1 FROM care_links c WHERE c.patient_id=n.patient_id AND c.doctor_id=$1 AND c.active))`, p.ID, cursor, p.Role).Scan(&latest)
			if err != nil {
				return
			}
			if latest > cursor {
				cursor = latest
				if _, err = fmt.Fprintf(w, "id: %d\nevent: refresh\ndata: {}\n\n", cursor); err != nil {
					return
				}
			} else {
				if _, err = fmt.Fprint(w, ": keepalive\n\n"); err != nil {
					return
				}
			}
			flusher.Flush()
		}
	}
}
