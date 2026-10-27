package app

import "net/http"

const alertFields = `a.id,a.patient_id,a.type,a.status,a.created_at,a.acknowledged_at,a.resolved_at,
 a.rule_version,a.period_start::text,a.period_end::text,a.source_entry_ids,a.details,a.severity,a.title,a.reason,a.metric,a.baseline_value,a.current_value,a.delta,
 (a.status='new' AND a.created_at<now()-interval '5 minutes') overdue`

func alertStatus(r *http.Request) (string, bool) {
	v := r.URL.Query().Get("status")
	if v == "" {
		v = "open"
	}
	return v, v == "open" || v == "all" || v == "new" || v == "ack" || v == "resolved"
}
func (s *Server) doctorAlerts(w http.ResponseWriter, r *http.Request) {
	status, ok := alertStatus(r)
	if !ok {
		writeError(w, 400, "Неизвестный статус")
		return
	}
	if err := s.recordRead(r, "alerts.list", current(r).ID); err != nil {
		s.fail(w, r, err)
		return
	}
	s.listPage(w, r, `SELECT `+alertFields+`,u.name patient_name FROM alerts a JOIN users u ON u.id=a.patient_id JOIN care_links c ON c.patient_id=a.patient_id WHERE c.doctor_id=$1 AND c.active AND ($2='all' OR ($2='open' AND a.status!='resolved') OR a.status=$2)`, current(r).ID, status)
}
func (s *Server) patientAlerts(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.authorized(r, id) {
		writeError(w, 403, "Нет доступа к сигналам")
		return
	}
	status, ok := alertStatus(r)
	if !ok {
		writeError(w, 400, "Неизвестный статус")
		return
	}
	if err := s.recordRead(r, "alerts.list", id); err != nil {
		s.fail(w, r, err)
		return
	}
	s.listPage(w, r, `SELECT `+alertFields+` FROM alerts a WHERE a.patient_id=$1 AND ($2='all' OR ($2='open' AND a.status!='resolved') OR a.status=$2)`, id, status)
}
func (s *Server) patientDiaries(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.authorized(r, id) {
		writeError(w, 403, "Нет доступа к дневникам")
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "all"
	}
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if (kind != "all" && kind != "ema" && kind != "csd") || (from != "" && !date(from)) || (to != "" && !date(to)) || (from != "" && to != "" && from > to) {
		writeError(w, 400, "Проверьте тип и диапазон дат")
		return
	}
	if err := s.recordRead(r, "diaries.list", id); err != nil {
		s.fail(w, r, err)
		return
	}
	s.listPage(w, r, `SELECT id,patient_id,kind,local_date::text,timezone,version,answers,scores,scoring_version,created_at,supersedes_id FROM current_entries WHERE patient_id=$1 AND ($2='all' OR kind=$2) AND (nullif($3,'')::date IS NULL OR local_date>=nullif($3,'')::date) AND (nullif($4,'')::date IS NULL OR local_date<=nullif($4,'')::date)`, id, kind, from, to)
}
func (s *Server) patientReviews(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.authorized(r, id) {
		writeError(w, 403, "Нет доступа к осмотрам")
		return
	}
	if err := s.recordRead(r, "reviews.list", id); err != nil {
		s.fail(w, r, err)
		return
	}
	s.listPage(w, r, `SELECT r.id,r.patient_id,r.doctor_id,r.action,r.note,r.created_at,r.period_start::text,r.period_end::text,r.source_entry_ids,u.name doctor_name FROM reviews r JOIN users u ON u.id=r.doctor_id WHERE patient_id=$1`, id)
}

func (s *Server) alertDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("alert")
	if !uuid(id) {
		writeError(w, 404, "Сигнал не найден")
		return
	}
	var patient string
	if err := s.db.QueryRow(r.Context(), `SELECT patient_id FROM alerts WHERE id=$1`, id).Scan(&patient); err != nil {
		writeError(w, 404, "Сигнал не найден")
		return
	}
	if !s.authorized(r, patient) {
		writeError(w, 403, "Нет доступа к сигналу")
		return
	}
	data, err := s.jsonQuery(r.Context(), `SELECT to_jsonb(x) FROM (SELECT `+alertFields+`,u.name patient_name,(SELECT coalesce(jsonb_agg(jsonb_build_object('id',aa.id,'action',aa.action,'note',CASE WHEN $2='doctor' THEN aa.note ELSE '' END,'actor_name',d.name,'created_at',aa.created_at) ORDER BY aa.created_at),'[]'::jsonb) FROM alert_actions aa JOIN users d ON d.id=aa.actor_id WHERE aa.alert_id=a.id) actions FROM alerts a JOIN users u ON u.id=a.patient_id WHERE a.id=$1) x`, id, current(r).Role)
	if err == nil {
		err = s.recordRead(r, "alert.read", id)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, data)
}
func (s *Server) reviewDetail(w http.ResponseWriter, r *http.Request) {
	patient, id := r.PathValue("id"), r.PathValue("review")
	if !s.authorized(r, patient) || !uuid(id) {
		writeError(w, 403, "Нет доступа к осмотру")
		return
	}
	data, err := s.jsonQuery(r.Context(), `SELECT to_jsonb(x) FROM (SELECT r.id,r.patient_id,r.doctor_id,r.action,r.note,r.created_at,r.period_start::text,r.period_end::text,r.source_entry_ids,u.name doctor_name FROM reviews r JOIN users u ON u.id=r.doctor_id WHERE r.patient_id=$1 AND r.id=$2) x`, patient, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(data) == 0 {
		writeError(w, 404, "Осмотр не найден")
		return
	}
	if err = s.recordRead(r, "review.read", id); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, data)
}
