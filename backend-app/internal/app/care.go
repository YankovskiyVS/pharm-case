package app

import (
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
)

func (s *Server) assign(w http.ResponseWriter, r *http.Request) { s.saveSchedule(w, r, false, true) }
func (s *Server) review(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.authorized(r, id) {
		writeError(w, 403, "Нет доступа к пациенту")
		return
	}
	var in struct {
		Action   string `json:"action"`
		Note     string `json:"note"`
		Snapshot string `json:"snapshot_token"`
		Key      string `json:"request_key"`
	}
	if !decode(w, r, &in) {
		return
	}
	if (in.Key != "" && !uuid(in.Key)) || len(in.Snapshot) != 64 || (in.Action != "reviewed" && in.Action != "contact") || len([]rune(in.Note)) > 2000 {
		writeError(w, 400, "Проверьте действие и комментарий")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rollback(tx, r.Context())
	digest := hash(id + "|" + in.Action + "|" + in.Note + "|" + in.Snapshot)
	var today string
	err = tx.QueryRow(r.Context(), `SELECT (now() AT TIME ZONE timezone)::date::text FROM users WHERE id=$1 FOR UPDATE`, id).Scan(&today)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if in.Key != "" {
		var previous, oldHash string
		err = tx.QueryRow(r.Context(), `SELECT id,request_hash FROM reviews WHERE doctor_id=$1 AND request_key=$2`, current(r).ID, in.Key).Scan(&previous, &oldHash)
		if err == nil {
			if oldHash != digest {
				writeError(w, 409, "Ключ уже использован для другого осмотра")
				return
			}
			writeJSON(w, 200, map[string]string{"id": previous})
			return
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			s.fail(w, r, err)
			return
		}
	}
	var sourceIDs []string
	err = tx.QueryRow(r.Context(), `SELECT coalesce(array_agg(id::text ORDER BY id::text),'{}') FROM current_entries WHERE patient_id=$1 AND local_date BETWEEN $2::date-13 AND $2::date`, id, today).Scan(&sourceIDs)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if snapshotToken(id, today, sourceIDs) != in.Snapshot {
		writeError(w, 409, "Дневники изменились после открытия профиля. Обновите данные перед подтверждением осмотра")
		return
	}
	var reviewID string
	var link string
	err = tx.QueryRow(r.Context(), `SELECT id FROM care_links WHERE doctor_id=$1 AND patient_id=$2 AND active FOR SHARE`, current(r).ID, id).Scan(&link)
	if err == nil {
		var key *string
		if in.Key != "" {
			key = &in.Key
		}
		err = tx.QueryRow(r.Context(), `INSERT INTO reviews(doctor_id,patient_id,action,note,period_start,period_end,source_entry_ids,request_key,request_hash) VALUES($1,$2,$3,$4,$5::date-13,$5::date,$6::uuid[],$7,$8) RETURNING id`, current(r).ID, id, in.Action, in.Note, today, sourceIDs, key, digest).Scan(&reviewID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO outbox_events(type,patient_id,aggregate_id) VALUES('review.recorded',$1,$2)`, id, reviewID)
	}
	if err == nil {
		err = audit(r.Context(), tx, current(r), "review."+in.Action, id, r.Context().Value(requestKey{}).(string))
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 201, map[string]string{"id": reviewID})
}
func (s *Server) alertAction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !uuid(id) {
		writeError(w, 400, "Некорректное уведомление")
		return
	}
	var in struct {
		Action string `json:"action"`
		Note   string `json:"note"`
	}
	if !decode(w, r, &in) {
		return
	}
	if (in.Action != "ack" && in.Action != "resolved") || len([]rune(in.Note)) > 2000 {
		writeError(w, 400, "Проверьте действие")
		return
	}
	if in.Action == "resolved" && strings.TrimSpace(in.Note) == "" {
		writeError(w, 400, "Опишите выполненное действие перед закрытием")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rollback(tx, r.Context())
	var status, patient string
	err = tx.QueryRow(r.Context(), `SELECT a.status,a.patient_id FROM alerts a JOIN care_links c ON c.patient_id=a.patient_id WHERE a.id=$1 AND c.doctor_id=$2 AND c.active FOR UPDATE OF a FOR SHARE OF c`, id, current(r).ID).Scan(&status, &patient)
	if err != nil {
		writeError(w, 403, "Нет доступа к уведомлению")
		return
	}
	if status == in.Action {
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if (in.Action == "ack" && status != "new") || (in.Action == "resolved" && status != "ack") {
		writeError(w, 409, "Обновите список: статус уже изменён или сначала подтвердите ознакомление")
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE alerts SET status=$2,acknowledged_by=CASE WHEN $2='ack' THEN $3::uuid ELSE acknowledged_by END,acknowledged_at=CASE WHEN $2='ack' THEN now() ELSE acknowledged_at END,resolved_at=CASE WHEN $2='resolved' THEN now() ELSE resolved_at END WHERE id=$1`, id, in.Action, current(r).ID)
	var actionID string
	if err == nil {
		err = tx.QueryRow(r.Context(), `INSERT INTO alert_actions(alert_id,actor_id,action,note) VALUES($1,$2,$3,$4) RETURNING id`, id, current(r).ID, in.Action, in.Note).Scan(&actionID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO outbox_events(type,patient_id,aggregate_id) VALUES('alert.updated',$1,$2)`, patient, actionID)
	}
	if err == nil {
		err = audit(r.Context(), tx, current(r), "alert."+in.Action, id, r.Context().Value(requestKey{}).(string))
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) conversationDoctor(r *http.Request, patient string) (string, int, error) {
	doctor := current(r).ID
	if current(r).Role == "patient" {
		doctor = r.URL.Query().Get("doctor_id")
		if doctor == "" {
			var total int
			err := s.db.QueryRow(r.Context(), `SELECT count(*),coalesce(min(c.doctor_id::text),'') FROM care_links c JOIN users d ON d.id=c.doctor_id WHERE c.patient_id=$1 AND c.active AND d.active AND d.role='doctor'`, patient).Scan(&total, &doctor)
			if err != nil {
				return "", 500, err
			}
			if total > 1 {
				return "", 400, nil
			}
		}
	}
	if !uuid(doctor) {
		return "", 200, nil
	}
	var linked bool
	err := s.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM care_links c JOIN users d ON d.id=c.doctor_id WHERE c.patient_id=$1 AND c.doctor_id=$2 AND c.active AND d.active AND d.role='doctor')`, patient, doctor).Scan(&linked)
	if err != nil {
		return "", 500, err
	}
	if !linked {
		return "", 403, nil
	}
	return doctor, 200, nil
}
func (s *Server) messages(w http.ResponseWriter, r *http.Request) {
	patient := r.PathValue("id")
	if !s.authorized(r, patient) {
		writeError(w, 403, "Нет доступа к переписке")
		return
	}
	doctor, status, err := s.conversationDoctor(r, patient)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if status != 200 {
		writeError(w, status, "Выберите доступного врача для переписки")
		return
	}
	if doctor == "" {
		if r.URL.Query().Get("page") == "true" {
			zero := int64(0)
			writeJSON(w, 200, pageResult{Items: []json.RawMessage{}, UnreadCount: &zero})
		} else {
			writeJSON(w, 200, []any{})
		}
		return
	}
	base := `SELECT m.id,m.sender_id,m.body,m.created_at,u.name sender_name,u.role sender_role,c.doctor_id,
 (m.sender_id=$3 OR EXISTS(SELECT 1 FROM message_reads mr WHERE mr.message_id=m.id AND mr.user_id=$3)) is_read,
 (SELECT min(mr.read_at) FROM message_reads mr WHERE mr.message_id=m.id AND mr.user_id!=m.sender_id) read_at
 FROM messages m JOIN care_links c ON c.id=m.care_link_id JOIN users u ON u.id=m.sender_id WHERE c.patient_id=$1 AND c.active AND c.doctor_id=$2`
	if r.URL.Query().Get("page") == "true" {
		limit, cursor, pageErr := pagination(r)
		if pageErr != nil {
			writeError(w, 400, "Проверьте страницу")
			return
		}
		result, pageErr := s.pageQuery(r.Context(), base, []any{patient, doctor, current(r).ID}, limit, cursor)
		if pageErr != nil {
			s.fail(w, r, pageErr)
			return
		}
		var unread int64
		pageErr = s.db.QueryRow(r.Context(), `SELECT count(*) FROM messages m JOIN care_links c ON c.id=m.care_link_id WHERE c.patient_id=$1 AND c.doctor_id=$2 AND m.sender_id!=$3 AND NOT EXISTS(SELECT 1 FROM message_reads mr WHERE mr.message_id=m.id AND mr.user_id=$3)`, patient, doctor, current(r).ID).Scan(&unread)
		if pageErr != nil {
			s.fail(w, r, pageErr)
			return
		}
		result.UnreadCount = &unread
		for i, j := 0, len(result.Items)-1; i < j; i, j = i+1, j-1 {
			result.Items[i], result.Items[j] = result.Items[j], result.Items[i]
		}
		if pageErr = s.recordRead(r, "messages.read", patient); pageErr != nil {
			s.fail(w, r, pageErr)
			return
		}
		writeJSON(w, 200, result)
		return
	}
	data, err := s.jsonQuery(r.Context(), `SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.created_at),'[]'::jsonb) FROM (`+base+` ORDER BY m.created_at DESC,m.id DESC LIMIT 100) x`, patient, doctor, current(r).ID)
	if err == nil {
		err = s.recordRead(r, "messages.read", patient)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, data)
}
func (s *Server) readMessages(w http.ResponseWriter, r *http.Request) {
	patient := r.PathValue("id")
	if !s.authorized(r, patient) {
		writeError(w, 403, "Нет доступа к переписке")
		return
	}
	var in struct {
		Doctor string   `json:"doctor_id"`
		IDs    []string `json:"ids"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.IDs) < 1 || len(in.IDs) > 100 {
		writeError(w, 400, "Укажите 1–100 сообщений")
		return
	}
	for _, id := range in.IDs {
		if !uuid(id) {
			writeError(w, 400, "Некорректное сообщение")
			return
		}
	}
	if current(r).Role == "doctor" {
		in.Doctor = current(r).ID
	}
	if !uuid(in.Doctor) {
		writeError(w, 400, "Выберите врача")
		return
	}
	var linked bool
	err := s.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM care_links c JOIN users d ON d.id=c.doctor_id WHERE c.patient_id=$1 AND c.doctor_id=$2 AND c.active AND d.active AND d.role='doctor')`, patient, in.Doctor).Scan(&linked)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !linked {
		writeError(w, 403, "Нет доступа к диалогу")
		return
	}
	_, err = s.db.Exec(r.Context(), `INSERT INTO message_reads(message_id,user_id) SELECT m.id,$3 FROM messages m JOIN care_links c ON c.id=m.care_link_id WHERE c.patient_id=$1 AND c.doctor_id=$2 AND m.sender_id!=$3 AND m.id=ANY($4::uuid[]) ON CONFLICT DO NOTHING`, patient, in.Doctor, current(r).ID, in.IDs)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.authorized(r, id) {
		writeError(w, 403, "Нет доступа к переписке")
		return
	}
	var in struct {
		Body     string `json:"body"`
		Key      string `json:"request_key"`
		DoctorID string `json:"doctor_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Body = strings.TrimSpace(in.Body)
	if len([]rune(in.Body)) < 1 || len([]rune(in.Body)) > 4000 || !uuid(in.Key) {
		writeError(w, 400, "Сообщение должно содержать 1–4000 символов")
		return
	}
	doctor := current(r).ID
	if current(r).Role == "patient" {
		doctor = in.DoctorID
	}
	if !uuid(doctor) {
		writeError(w, 400, "Выберите врача")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rollback(tx, r.Context())
	var link string
	err = tx.QueryRow(r.Context(), `SELECT c.id FROM care_links c JOIN users d ON d.id=c.doctor_id JOIN users p ON p.id=c.patient_id WHERE c.patient_id=$1 AND c.doctor_id=$2 AND c.active AND d.active AND d.role='doctor' AND p.active AND p.role='patient' FOR SHARE OF c,d,p`, id, doctor).Scan(&link)
	if err != nil {
		writeError(w, 403, "Нет активной связи с врачом")
		return
	}
	var message string
	var same bool
	err = tx.QueryRow(r.Context(), `INSERT INTO messages(care_link_id,sender_id,body,request_key) VALUES($1,$2,$3,$4) ON CONFLICT(sender_id,request_key) DO UPDATE SET request_key=excluded.request_key RETURNING id,body=$3 AND care_link_id=$1`, link, current(r).ID, in.Body, in.Key).Scan(&message, &same)
	if err == nil && !same {
		writeError(w, 409, "Ключ уже использован другим сообщением")
		return
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO outbox_events(type,patient_id,aggregate_id) VALUES('message.sent',$1,$2) ON CONFLICT(type,aggregate_id) DO NOTHING`, id, message)
	}
	if err == nil {
		err = audit(r.Context(), tx, current(r), "message.send", message, r.Context().Value(requestKey{}).(string))
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 201, map[string]string{"id": message})
}
