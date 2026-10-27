package app

import (
	"encoding/json"
	"net/http"
	"strconv"
)

const notificationBase = ` FROM notifications n JOIN users u ON u.id=n.patient_id JOIN users viewer ON viewer.id=n.recipient_id JOIN outbox_events e ON e.id=n.event_id
 WHERE n.recipient_id=$1 AND ($2='patient' OR EXISTS(SELECT 1 FROM care_links c WHERE c.patient_id=n.patient_id AND c.doctor_id=$1 AND c.active))`
const notificationReadSQL = `(n.seq<=viewer.notification_read_seq OR EXISTS(SELECT 1 FROM notification_reads nr WHERE nr.notification_id=n.id AND nr.user_id=$1))`

func (s *Server) notifications(w http.ResponseWriter, r *http.Request) {
	p := current(r)
	fields := `SELECT n.id,n.seq,n.type,n.created_at,n.patient_id,u.name patient_name,` + notificationReadSQL + ` is_read,e.id event_id,
 CASE WHEN e.type='alert.updated' THEN coalesce((SELECT alert_id FROM alert_actions WHERE id=e.aggregate_id),e.aggregate_id)
 WHEN e.type='assignment.changed' THEN coalesce((SELECT assignment_id FROM assignment_history WHERE id=e.aggregate_id),e.aggregate_id)
 WHEN e.type='reminder.due' THEN coalesce((SELECT assignment_id FROM reminder_runs WHERE id=e.aggregate_id),e.aggregate_id)
 WHEN e.type='audio.ready' THEN coalesce((SELECT recording_id FROM audio_runs WHERE id=e.aggregate_id),e.aggregate_id)
 WHEN e.type='invitation.accepted' THEN n.patient_id ELSE e.aggregate_id END target_id,
 CASE WHEN n.type='message.sent' THEN 'conversation' WHEN n.type IN ('risk.raised','deterioration.raised','alert.updated') THEN 'alert' WHEN n.type LIKE 'audio.%' THEN 'audio' WHEN n.type='diary.submitted' THEN 'diary' WHEN n.type='review.recorded' THEN 'review' WHEN n.type='assignment.changed' OR n.type='reminder.due' THEN 'schedule' ELSE 'profile' END target_type,
 (SELECT c.doctor_id FROM messages m JOIN care_links c ON c.id=m.care_link_id WHERE m.id=e.aggregate_id AND e.type='message.sent') doctor_id`
	if r.URL.Query().Get("page") != "true" {
		data, err := s.jsonQuery(r.Context(), `SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.seq DESC),'[]'::jsonb) FROM (`+fields+notificationBase+` ORDER BY n.seq DESC LIMIT 50) x`, p.ID, p.Role)
		if err == nil {
			err = s.recordRead(r, "notifications.read", p.ID)
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		writeJSON(w, 200, data)
		return
	}
	limit := 25
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			writeError(w, 400, "Размер страницы от 1 до 100")
			return
		}
		limit = n
	}
	cursor := int64(9223372036854775807)
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		var err error
		cursor, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || cursor < 1 {
			writeError(w, 400, "Некорректный курсор")
			return
		}
	}
	var total, unread int64
	err := s.db.QueryRow(r.Context(), `SELECT count(*),count(*) FILTER(WHERE NOT `+notificationReadSQL+`)`+notificationBase, p.ID, p.Role).Scan(&total, &unread)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT to_jsonb(x),x.seq FROM (`+fields+notificationBase+` AND n.seq<$3 ORDER BY n.seq DESC LIMIT $4) x`, p.ID, p.Role, cursor, limit+1)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items := []json.RawMessage{}
	seqs := []int64{}
	for rows.Next() {
		var raw []byte
		var seq int64
		if err = rows.Scan(&raw, &seq); err != nil {
			break
		}
		items = append(items, json.RawMessage(raw))
		seqs = append(seqs, seq)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var next *string
	if len(items) > limit {
		v := strconv.FormatInt(seqs[limit-1], 10)
		next = &v
		items = items[:limit]
	}
	if err = s.recordRead(r, "notifications.read", p.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": total, "unread_count": unread, "next_cursor": next})
}
func (s *Server) readNotifications(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Through *int64   `json:"through"`
		IDs     []string `json:"ids"`
		All     bool     `json:"all"`
	}
	if !decode(w, r, &in) {
		return
	}
	modes := 0
	if in.Through != nil {
		modes++
	}
	if len(in.IDs) > 0 {
		modes++
	}
	if in.All {
		modes++
	}
	if modes != 1 || len(in.IDs) > 100 {
		writeError(w, 400, "Выберите уведомления или явное прочтение всех")
		return
	}
	for _, id := range in.IDs {
		if !uuid(id) {
			writeError(w, 400, "Некорректное уведомление")
			return
		}
	}
	if in.Through != nil && *in.Through < 0 {
		writeError(w, 400, "Некорректный курсор")
		return
	}
	var err error
	if len(in.IDs) > 0 {
		_, err = s.db.Exec(r.Context(), `INSERT INTO notification_reads(notification_id,user_id) SELECT id,recipient_id FROM notifications WHERE recipient_id=$1 AND id=ANY($2::uuid[]) ON CONFLICT DO NOTHING`, current(r).ID, in.IDs)
	} else {
		through := int64(9223372036854775807)
		if in.Through != nil {
			through = *in.Through
		}
		_, err = s.db.Exec(r.Context(), `UPDATE users SET notification_read_seq=greatest(notification_read_seq,least($2,(SELECT coalesce(max(seq),0) FROM notifications WHERE recipient_id=$1))) WHERE id=$1`, current(r).ID, through)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) revisions(w http.ResponseWriter, r *http.Request) {
	patient, id := r.PathValue("id"), r.PathValue("entry")
	if !s.authorized(r, patient) || !uuid(id) {
		writeError(w, 403, "Нет доступа к истории записи")
		return
	}
	data, err := s.jsonQuery(r.Context(), `WITH RECURSIVE chain AS (
 SELECT e.* FROM diary_entries e WHERE e.id=$2 AND e.patient_id=$1
 UNION ALL SELECT e.* FROM diary_entries e JOIN chain c ON c.supersedes_id=e.id WHERE e.patient_id=$1
 ) SELECT COALESCE(jsonb_agg(to_jsonb(e) ORDER BY e.created_at DESC),'[]'::jsonb) FROM (
 SELECT id,kind,local_date::text,timezone,version,answers,scores,scoring_version,created_at,supersedes_id FROM chain) e`, patient, id)
	if err == nil {
		err = s.recordRead(r, "diary.revisions", id)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, data)
}
