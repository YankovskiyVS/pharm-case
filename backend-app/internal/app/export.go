package app

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"
)

func (s *Server) exportPatient(w http.ResponseWriter, r *http.Request) {
	patient := r.PathValue("id")
	if !s.authorized(r, patient) {
		writeError(w, 403, "Нет доступа к отчёту")
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "csv" {
		writeError(w, 400, "Формат отчёта: json или csv")
		return
	}
	if err := s.recordRead(r, "patient.export", patient); err != nil {
		s.fail(w, r, err)
		return
	}
	if format == "csv" {
		s.exportDiaryCSV(w, r, patient)
		return
	}
	type section struct {
		name, query string
		args        []any
	}
	args := []any{patient}
	messageArgs := []any{patient, current(r).Role, current(r).ID}
	sections := []section{
		{"profile", `SELECT jsonb_build_object('id',id,'name',name,'email',email,'timezone',timezone,'role',role,'created_at',created_at) FROM users WHERE id=$1`, args},
		{"diary_revisions", `SELECT to_jsonb(e)||jsonb_build_object('is_current',NOT EXISTS(SELECT 1 FROM diary_entries next WHERE next.supersedes_id=e.id)) FROM diary_entries e WHERE patient_id=$1 ORDER BY local_date,created_at,id`, args},
		{"alerts", `SELECT to_jsonb(a) FROM alerts a WHERE patient_id=$1 ORDER BY created_at,id`, args},
		{"alert_actions", `SELECT to_jsonb(a) FROM alert_actions a JOIN alerts x ON x.id=a.alert_id WHERE x.patient_id=$1 ORDER BY a.created_at,a.id`, args},
		{"reviews", `SELECT to_jsonb(x) FROM (SELECT id,doctor_id,patient_id,action,note,period_start,period_end,source_entry_ids,created_at FROM reviews WHERE patient_id=$1 ORDER BY created_at,id) x`, args},
		{"schedules", `SELECT to_jsonb(x) FROM (SELECT id,patient_id,kind,start_date,end_date,due_time,status,interval_days,created_by,created_at,updated_at FROM assignments WHERE patient_id=$1 ORDER BY created_at,id) x`, args},
		{"schedule_history", `SELECT to_jsonb(x) FROM (SELECT id,assignment_id,patient_id,actor_id,operation,snapshot,created_at FROM assignment_history WHERE patient_id=$1 ORDER BY created_at,id) x`, args},
		{"messages", `SELECT to_jsonb(x) FROM (SELECT m.id,m.care_link_id,m.sender_id,m.body,m.created_at,c.doctor_id FROM messages m JOIN care_links c ON c.id=m.care_link_id WHERE c.patient_id=$1 AND ($2='patient' OR c.doctor_id=$3) ORDER BY m.created_at,m.id) x`, messageArgs},
		{"audio_recordings", `SELECT to_jsonb(x) FROM (SELECT id,patient_id,task,device,mime,size_bytes,consent_version,created_at FROM audio_recordings WHERE patient_id=$1 ORDER BY created_at,id) x`, args},
		{"audio_runs", `SELECT to_jsonb(x) FROM (SELECT j.id,j.recording_id,j.status,j.result,j.error_code,j.created_at,j.finished_at FROM audio_runs j JOIN audio_recordings a ON a.id=j.recording_id WHERE a.patient_id=$1 ORDER BY j.created_at,j.id) x`, args},
	}
	if current(r).Role == "doctor" {
		sections = append(sections,
			section{"clinical_context_versions", `SELECT to_jsonb(x) FROM (SELECT ` + clinicalVersionFields + ` FROM clinical_context_versions v JOIN users u ON u.id=v.author_id WHERE v.patient_id=$1 ORDER BY v.version) x`, args},
			section{"clinical_audio_reports", `SELECT to_jsonb(x) FROM (SELECT ` + clinicalReportFields + clinicalReportFrom + ` WHERE cr.patient_id=$1 ORDER BY cr.created_at,cr.id) x`, args},
		)
	}
	// The export is streamed from a single consistent read snapshot; it is not limited to UI pages.
	tx, err := s.db.BeginTx(r.Context(), readSnapshotOptions())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rollback(tx, r.Context())
	w.Header().Set("Content-Disposition", `attachment; filename="psitech-`+patient+`.`+format+`"`)
	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	}
	csvWriter := csv.NewWriter(w)
	if format == "csv" {
		if err = csvWriter.Write([]string{"section", "record_json"}); err != nil {
			return
		}
	} else {
		if _, err = io.WriteString(w, `{"exported_at":`); err != nil {
			return
		}
		stamp, _ := json.Marshal(time.Now().UTC().Format(time.RFC3339))
		if _, err = w.Write(stamp); err != nil {
			return
		}
	}
	for _, section := range sections {
		rows, queryErr := tx.Query(r.Context(), section.query, section.args...)
		if queryErr != nil {
			slog.Error("export query failed", "section", section.name)
			return
		}
		if format == "json" {
			if _, err = io.WriteString(w, `,"`+section.name+`":[`); err != nil {
				rows.Close()
				return
			}
		}
		first := true
		for rows.Next() {
			var raw []byte
			if err = rows.Scan(&raw); err != nil {
				break
			}
			if format == "csv" {
				err = csvWriter.Write([]string{section.name, string(raw)})
			} else {
				if !first {
					_, err = io.WriteString(w, ",")
				}
				if err == nil {
					_, err = w.Write(raw)
				}
			}
			if err != nil {
				break
			}
			first = false
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			slog.Debug("export interrupted")
			return
		}
		if format == "json" {
			if _, err = io.WriteString(w, "]"); err != nil {
				return
			}
		}
	}
	if format == "json" {
		if _, err = io.WriteString(w, "}"); err != nil {
			return
		}
	} else {
		csvWriter.Flush()
		if err = csvWriter.Error(); err != nil {
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		slog.Debug("export snapshot close failed")
	}
}

func (s *Server) exportDiaryCSV(w http.ResponseWriter, r *http.Request, patient string) {
	tx, err := s.db.BeginTx(r.Context(), readSnapshotOptions())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rollback(tx, r.Context())
	rows, err := tx.Query(r.Context(), `SELECT patient_id::text,local_date::text,kind,timezone,form_id::text,created_at::text,
 coalesce(answers->>'mood',''),coalesce(answers->>'anhedonia',''),coalesce(answers->>'energy',''),coalesce(answers->>'anxiety',''),coalesce(answers->>'suicidal',''),coalesce(answers->>'comment',''),
 coalesce(answers->>'bedtime',''),coalesce(answers->>'sleep_attempt',''),coalesce(answers->>'latency',''),coalesce(answers->>'awakenings',''),coalesce(answers->>'awake_minutes',''),coalesce(answers->>'final_wake',''),coalesce(answers->>'after_wake_minutes',''),coalesce(answers->>'early_wake',''),coalesce(answers->>'early_minutes',''),coalesce(answers->>'rise_time',''),coalesce(answers->>'total_sleep',''),coalesce(answers->>'quality',''),coalesce(answers->>'rested',''),coalesce(answers->>'nap_minutes',''),coalesce(answers->>'alcohol',''),coalesce(answers->>'caffeine',''),
 coalesce(scores->>'tib',''),coalesce(scores->>'se',''),coalesce(scores->>'midpoint',''),coalesce(scores->>'waso',''),scoring_version
 FROM current_entries WHERE patient_id=$1 ORDER BY local_date,kind,id`, patient)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rows.Close()
	w.Header().Set("Content-Disposition", `attachment; filename="psitech-diaries-`+patient+`.csv"`)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	if _, err = io.WriteString(w, "\uFEFF"); err != nil {
		return
	}
	writer := csv.NewWriter(w)
	header := []string{"patient_id", "local_date", "form_type", "timezone", "form_id", "submitted_at", "mood", "anhedonia", "energy", "anxiety", "suicidal", "comment", "bedtime", "sleep_attempt", "latency_minutes", "awakenings", "awake_minutes", "final_wake", "after_wake_minutes", "early_wake", "early_minutes", "rise_time", "total_sleep_minutes", "quality", "rested", "nap_minutes", "alcohol", "caffeine", "time_in_bed_minutes", "sleep_efficiency", "sleep_midpoint", "waso_minutes", "scoring_version"}
	if err = writer.Write(header); err != nil {
		return
	}
	for rows.Next() {
		row := make([]string, len(header))
		destinations := make([]any, len(row))
		for i := range row {
			destinations[i] = &row[i]
		}
		if err = rows.Scan(destinations...); err != nil {
			return
		}
		if err = writer.Write(row); err != nil {
			return
		}
	}
	if err = rows.Err(); err != nil {
		return
	}
	rows.Close()
	writer.Flush()
	if writer.Error() != nil {
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		slog.Debug("diary export snapshot close failed")
	}
}
