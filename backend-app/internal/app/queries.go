package app

import (
	"context"
	"encoding/json"
	"net/http"
	"psitech/internal/scoring"
	"sort"
	"strings"
	"time"
)

func (s *Server) jsonQuery(ctx context.Context, sql string, args ...any) (json.RawMessage, error) {
	var raw []byte
	err := s.db.QueryRow(ctx, sql, args...).Scan(&raw)
	return json.RawMessage(raw), err
}

const patientListSQL = `SELECT COALESCE(jsonb_agg(to_jsonb(p) ORDER BY p.open_alerts DESC,p.name),'[]'::jsonb) FROM (
 SELECT u.id,u.name,u.email,u.timezone,
 (SELECT count(*) FROM alerts a WHERE a.patient_id=u.id AND a.status!='resolved') open_alerts,
 (SELECT max(e.created_at) FROM current_entries e WHERE e.patient_id=u.id) last_entry,
 (SELECT max(rv.created_at) FROM reviews rv WHERE rv.patient_id=u.id AND rv.doctor_id=$1) reviewed_at,
 (SELECT count(*) FROM current_entries e WHERE e.patient_id=u.id AND e.kind='ema' AND e.local_date>=((now() AT TIME ZONE u.timezone)::date-2)) ema_count
 FROM users u JOIN care_links c ON c.patient_id=u.id WHERE c.doctor_id=$1 AND c.active AND u.active
 ) p`

func (s *Server) patients(w http.ResponseWriter, r *http.Request) {
	data, err := s.jsonQuery(r.Context(), patientListSQL, current(r).ID)
	if err == nil {
		err = s.recordRead(r, "patients.list", current(r).ID)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, data)
}
func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	p := current(r)
	if p.Role == "patient" {
		s.detail(w, r, p.ID)
		return
	}
	patients, err := s.jsonQuery(r.Context(), patientListSQL, p.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	alerts, err := s.jsonQuery(r.Context(), `SELECT COALESCE(jsonb_agg(to_jsonb(a) ORDER BY a.created_at DESC),'[]'::jsonb) FROM (SELECT a.*,u.name patient_name, (a.status='new' AND a.created_at<now()-interval '5 minutes') overdue FROM alerts a JOIN users u ON u.id=a.patient_id JOIN care_links c ON c.patient_id=a.patient_id WHERE c.doctor_id=$1 AND c.active AND a.status!='resolved' ORDER BY a.created_at DESC LIMIT 100) a`, p.ID)
	if err == nil {
		err = s.recordRead(r, "dashboard.read", p.ID)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var alertsTotal, patientsTotal, pendingReviewsTotal int64
	err = s.db.QueryRow(r.Context(), `SELECT count(*) FROM alerts a JOIN care_links c ON c.patient_id=a.patient_id WHERE c.doctor_id=$1 AND c.active AND a.status!='resolved'`, p.ID).Scan(&alertsTotal)
	if err == nil {
		err = s.db.QueryRow(r.Context(), `SELECT count(*),count(*) FILTER(WHERE NOT EXISTS(SELECT 1 FROM reviews r WHERE r.patient_id=u.id AND r.doctor_id=$1 AND r.created_at>=now()-interval '7 days')) FROM care_links c JOIN users u ON u.id=c.patient_id WHERE c.doctor_id=$1 AND c.active AND u.active`, p.ID).Scan(&patientsTotal, &pendingReviewsTotal)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"patients": patients, "alerts": alerts, "alerts_total": alertsTotal, "patients_total": patientsTotal, "pending_reviews_total": pendingReviewsTotal})
}
func (s *Server) patientDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.authorized(r, id) {
		writeError(w, 403, "Нет доступа к этому профилю")
		return
	}
	s.detail(w, r, id)
}
func (s *Server) detail(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()
	var user User
	err := s.db.QueryRow(ctx, `SELECT id,email,name,role,timezone FROM users WHERE id=$1`, id).Scan(&user.ID, &user.Email, &user.Name, &user.Role, &user.Timezone)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	entries, err := s.jsonQuery(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(e) ORDER BY e.local_date DESC,e.created_at DESC),'[]'::jsonb) FROM (SELECT id,kind,local_date::text,timezone,version,answers,scores,scoring_version,created_at,supersedes_id FROM current_entries WHERE patient_id=$1 ORDER BY local_date DESC,created_at DESC LIMIT 90) e`, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	alerts, err := s.jsonQuery(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(a) ORDER BY a.created_at DESC),'[]'::jsonb) FROM (SELECT id,type,status,created_at,acknowledged_at,resolved_at,rule_version,period_start::text,period_end::text,source_entry_ids,details,severity,title,reason,metric,baseline_value,current_value,delta,(status='new' AND created_at<now()-interval '5 minutes') overdue FROM alerts WHERE patient_id=$1 ORDER BY created_at DESC LIMIT 50) a`, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	assignments, err := s.jsonQuery(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(a)),'[]'::jsonb) FROM (SELECT id,kind,start_date::text,end_date::text,due_time::text,status,interval_days,created_at,updated_at FROM assignments WHERE patient_id=$1 ORDER BY kind) a`, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	reviews, err := s.jsonQuery(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(a) ORDER BY a.created_at DESC),'[]'::jsonb) FROM (SELECT r.id,r.action,r.note,r.created_at,r.period_start::text,r.period_end::text,r.source_entry_ids,u.name doctor_name FROM reviews r JOIN users u ON u.id=r.doctor_id WHERE patient_id=$1 ORDER BY created_at DESC LIMIT 20) a`, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	team, err := s.jsonQuery(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_object('id',u.id,'name',u.name,'care_link_id',c.id)),'[]'::jsonb) FROM care_links c JOIN users u ON u.id=c.doctor_id WHERE c.patient_id=$1 AND c.active AND u.active AND u.role='doctor'`, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	zone, err := time.LoadLocation(user.Timezone)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	end := time.Now().In(zone)
	today := end.Format("2006-01-02")
	var visible []struct {
		ID      string          `json:"id"`
		Kind    string          `json:"kind"`
		Date    string          `json:"local_date"`
		Answers json.RawMessage `json:"answers"`
	}
	if err = json.Unmarshal(entries, &visible); err != nil {
		s.fail(w, r, err)
		return
	}
	days := []scoring.DailyEMA{}
	nights := []scoring.DailyCSD{}
	sourceIDs := []string{}
	for _, entry := range visible {
		if entry.Date >= end.AddDate(0, 0, -13).Format("2006-01-02") && entry.Date <= today {
			sourceIDs = append(sourceIDs, entry.ID)
		}
		switch entry.Kind {
		case "ema":
			var e scoring.EMA
			if err = json.Unmarshal(entry.Answers, &e); err != nil {
				s.fail(w, r, err)
				return
			}
			days = append(days, scoring.DailyEMA{Date: entry.Date, Answers: e})
		case "csd":
			var c scoring.CSD
			if err = json.Unmarshal(entry.Answers, &c); err != nil {
				s.fail(w, r, err)
				return
			}
			nights = append(nights, scoring.DailyCSD{Date: entry.Date, Answers: c})
		}
	}
	if err = s.recordRead(r, "patient.read", id); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"patient": user, "entries": entries, "alerts": alerts, "assignments": assignments, "reviews": reviews, "care_team": team, "summary": scoring.SummarizeEMA(days, end), "sleep_summary": scoring.SummarizeCSD(nights, end), "today": today, "review_snapshot": snapshotToken(id, today, sourceIDs)})
}

func snapshotToken(patient, day string, ids []string) string {
	ordered := append([]string{}, ids...)
	sort.Strings(ordered)
	return hash(patient + "|" + day + "|" + strings.Join(ordered, ","))
}
