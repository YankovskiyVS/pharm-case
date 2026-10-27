package app

import (
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
	"time"
)

type scheduleInput struct {
	Kind     string `json:"kind"`
	Start    string `json:"start_date"`
	End      string `json:"end_date"`
	Due      string `json:"due_time"`
	Interval int    `json:"interval_days"`
	Key      string `json:"request_key"`
}

func validateSchedule(in *scheduleInput) bool {
	if in.Interval == 0 {
		in.Interval = 1
	}
	_, err := time.Parse("15:04", in.Due)
	return err == nil && date(in.Start) && date(in.End) && in.End >= in.Start && (in.Kind == "ema" || in.Kind == "csd") && in.Interval >= 1 && in.Interval <= 365
}
func (s *Server) schedules(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.authorized(r, id) {
		writeError(w, 403, "Нет доступа к назначениям")
		return
	}
	if err := s.recordRead(r, "schedules.list", id); err != nil {
		s.fail(w, r, err)
		return
	}
	s.listPage(w, r, `SELECT id,patient_id,kind,start_date::text,end_date::text,due_time::text,status,interval_days,created_at,updated_at,created_by FROM assignments WHERE patient_id=$1`, id)
}
func (s *Server) scheduleHistory(w http.ResponseWriter, r *http.Request) {
	id, schedule := r.PathValue("id"), r.PathValue("schedule")
	if !s.authorized(r, id) || !uuid(schedule) {
		writeError(w, 403, "Нет доступа к истории назначения")
		return
	}
	if err := s.recordRead(r, "schedule.history", schedule); err != nil {
		s.fail(w, r, err)
		return
	}
	s.listPage(w, r, `SELECT h.id,h.assignment_id,h.patient_id,h.actor_id,u.name actor_name,h.operation,h.snapshot,h.created_at FROM assignment_history h JOIN users u ON u.id=h.actor_id WHERE h.patient_id=$1 AND h.assignment_id=$2`, id, schedule)
}
func (s *Server) createSchedule(w http.ResponseWriter, r *http.Request) {
	s.saveSchedule(w, r, false, false)
}
func (s *Server) updateSchedule(w http.ResponseWriter, r *http.Request) {
	s.saveSchedule(w, r, true, false)
}
func (s *Server) saveSchedule(w http.ResponseWriter, r *http.Request, update, legacy bool) {
	patient := r.PathValue("id")
	if !s.authorized(r, patient) {
		writeError(w, 403, "Нет доступа к назначениям")
		return
	}
	var in scheduleInput
	if !decode(w, r, &in) {
		return
	}
	if !validateSchedule(&in) || (!legacy && !uuid(in.Key)) {
		writeError(w, 400, "Проверьте даты, время, период и ключ операции")
		return
	}
	id := r.PathValue("schedule")
	if update && !uuid(id) {
		writeError(w, 400, "Некорректное назначение")
		return
	}
	raw, err := json.Marshal(in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	digest := hash(string(raw) + "|" + patient + "|" + id)
	p := current(r)
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rollback(tx, r.Context())
	if _, err = tx.Exec(r.Context(), `SELECT id FROM users WHERE id=$1 FOR UPDATE`, patient); err != nil {
		s.fail(w, r, err)
		return
	}
	var link string
	err = tx.QueryRow(r.Context(), `SELECT c.id FROM care_links c JOIN users d ON d.id=c.doctor_id WHERE c.patient_id=$1 AND c.doctor_id=$2 AND c.active AND d.active AND d.role='doctor' FOR SHARE OF c,d`, patient, p.ID).Scan(&link)
	if err != nil {
		writeError(w, 403, "Нет активной связи с пациентом")
		return
	}
	if in.Key != "" {
		var previous, oldHash string
		err = tx.QueryRow(r.Context(), `SELECT assignment_id,request_hash FROM assignment_history WHERE actor_id=$1 AND request_key=$2`, p.ID, in.Key).Scan(&previous, &oldHash)
		if err == nil {
			if oldHash != digest {
				writeError(w, 409, "Ключ уже использован для другой операции")
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
	if legacy {
		err = tx.QueryRow(r.Context(), `SELECT id FROM assignments WHERE patient_id=$1 AND kind=$2 AND status!='cancelled' ORDER BY created_at DESC,id DESC LIMIT 1`, patient, in.Kind).Scan(&id)
		if err == nil {
			update = true
		} else if !errors.Is(err, pgx.ErrNoRows) {
			s.fail(w, r, err)
			return
		}
	}
	if update {
		var matches bool
		var oldKind, oldStatus string
		err = tx.QueryRow(r.Context(), `SELECT kind,status,kind=$3 AND start_date=$4::date AND end_date=$5::date AND due_time=$6::time AND interval_days=$7 FROM assignments WHERE id=$1 AND patient_id=$2 FOR UPDATE`, id, patient, in.Kind, in.Start, in.End, in.Due, in.Interval).Scan(&oldKind, &oldStatus, &matches)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 404, "Назначение не найдено")
			return
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if oldKind != in.Kind || oldStatus == "cancelled" {
			writeError(w, 409, "Отменённое назначение и тип дневника нельзя изменить; создайте новое назначение")
			return
		}
		if matches && legacy {
			writeJSON(w, 200, map[string]string{"id": id})
			return
		}
	}
	// Active date ranges of one diary kind are deliberately non-overlapping.
	var overlaps bool
	err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM assignments WHERE patient_id=$1 AND kind=$2 AND status='active' AND id<>coalesce(nullif($3,'')::uuid,'00000000-0000-0000-0000-000000000000') AND start_date<=$5::date AND end_date>=$4::date)`, patient, in.Kind, id, in.Start, in.End).Scan(&overlaps)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if overlaps {
		writeError(w, 409, "Диапазон пересекается с действующим назначением этого дневника")
		return
	}
	operation := "created"
	status := 201
	if update {
		operation = "updated"
		status = 200
		_, err = tx.Exec(r.Context(), `UPDATE assignments SET kind=$3,start_date=$4,end_date=$5,due_time=$6,interval_days=$7,updated_at=now() WHERE id=$1 AND patient_id=$2`, id, patient, in.Kind, in.Start, in.End, in.Due, in.Interval)
	} else {
		var key *string
		if in.Key != "" {
			key = &in.Key
		}
		err = tx.QueryRow(r.Context(), `INSERT INTO assignments(patient_id,kind,start_date,end_date,due_time,created_by,interval_days,request_key,request_hash) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`, patient, in.Kind, in.Start, in.End, in.Due, p.ID, in.Interval, key, digest).Scan(&id)
	}
	var key *string
	if in.Key != "" {
		key = &in.Key
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO assignment_history(assignment_id,patient_id,actor_id,operation,snapshot,request_key,request_hash) SELECT id,patient_id,$2,$3,to_jsonb(a),$4,$5 FROM assignments a WHERE id=$1`, id, p.ID, operation, key, digest)
	}
	if err == nil {
		var event string
		err = tx.QueryRow(r.Context(), `SELECT id FROM assignment_history WHERE assignment_id=$1 ORDER BY created_at DESC,id DESC LIMIT 1`, id).Scan(&event)
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO outbox_events(type,patient_id,aggregate_id) VALUES('assignment.changed',$1,$2)`, patient, event)
		}
	}
	if err == nil {
		err = audit(r.Context(), tx, p, "schedule."+operation, id, r.Context().Value(requestKey{}).(string))
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if legacy {
		status = 200
	}
	writeJSON(w, status, map[string]string{"id": id})
}
func (s *Server) scheduleStatus(w http.ResponseWriter, r *http.Request) {
	patient, id := r.PathValue("id"), r.PathValue("schedule")
	if !s.authorized(r, patient) || !uuid(id) {
		writeError(w, 403, "Нет доступа к назначению")
		return
	}
	var in struct {
		Status string `json:"status"`
		Key    string `json:"request_key"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !uuid(in.Key) || (in.Status != "active" && in.Status != "paused" && in.Status != "cancelled") {
		writeError(w, 400, "Проверьте статус и ключ операции")
		return
	}
	digest := hash(id + "|" + patient + "|" + in.Status)
	p := current(r)
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rollback(tx, r.Context())
	if _, err = tx.Exec(r.Context(), `SELECT id FROM users WHERE id=$1 FOR UPDATE`, patient); err != nil {
		s.fail(w, r, err)
		return
	}
	var link string
	err = tx.QueryRow(r.Context(), `SELECT c.id FROM care_links c JOIN users d ON d.id=c.doctor_id WHERE c.patient_id=$1 AND c.doctor_id=$2 AND c.active AND d.active AND d.role='doctor' FOR SHARE OF c,d`, patient, p.ID).Scan(&link)
	if err != nil {
		writeError(w, 403, "Нет активной связи")
		return
	}
	var old string
	err = tx.QueryRow(r.Context(), `SELECT request_hash FROM assignment_history WHERE actor_id=$1 AND request_key=$2`, p.ID, in.Key).Scan(&old)
	if err == nil {
		if old != digest {
			writeError(w, 409, "Ключ уже использован")
			return
		}
		writeJSON(w, 200, map[string]string{"id": id})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		s.fail(w, r, err)
		return
	}
	var overlaps bool
	err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM assignments a JOIN assignments b ON b.patient_id=a.patient_id AND b.kind=a.kind AND b.id<>a.id AND b.status='active' AND b.start_date<=a.end_date AND b.end_date>=a.start_date WHERE a.id=$1 AND a.patient_id=$2)`, id, patient).Scan(&overlaps)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if in.Status == "active" && overlaps {
		writeError(w, 409, "Период пересекается с другим действующим назначением")
		return
	}
	tag, err := tx.Exec(r.Context(), `UPDATE assignments SET status=$3,updated_at=now() WHERE id=$1 AND patient_id=$2 AND (status!='cancelled' OR $3='cancelled')`, id, patient, in.Status)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, 409, "Назначение не найдено или уже отменено")
		return
	}
	var history string
	err = tx.QueryRow(r.Context(), `INSERT INTO assignment_history(assignment_id,patient_id,actor_id,operation,snapshot,request_key,request_hash) SELECT id,patient_id,$2,$3,to_jsonb(a),$4,$5 FROM assignments a WHERE id=$1 RETURNING id`, id, p.ID, "status."+in.Status, in.Key, digest).Scan(&history)
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO outbox_events(type,patient_id,aggregate_id) VALUES('assignment.changed',$1,$2)`, patient, history)
	}
	if err == nil {
		err = audit(r.Context(), tx, p, "schedule.status", id, r.Context().Value(requestKey{}).(string))
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]string{"id": id})
}
