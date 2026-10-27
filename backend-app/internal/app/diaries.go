package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
	"psitech/internal/scoring"
	"time"
)

type diaryInput struct {
	Kind       string          `json:"kind"`
	LocalDate  string          `json:"local_date"`
	Timezone   string          `json:"timezone"`
	FormID     string          `json:"form_id"`
	Supersedes *string         `json:"supersedes_id"`
	Answers    json.RawMessage `json:"answers"`
}

const deteriorationRuleVersion = "ema-mood-delta-3d-v1"

func parseAnswers(raw json.RawMessage, target any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(target)
}
func (s *Server) submitDiary(w http.ResponseWriter, r *http.Request) {
	var in diaryInput
	if !decode(w, r, &in) {
		return
	}
	if !uuid(in.FormID) || !date(in.LocalDate) || (in.Supersedes != nil && !uuid(*in.Supersedes)) {
		writeError(w, 400, "Проверьте дату и идентификатор записи")
		return
	}
	if in.Supersedes == nil && in.Timezone != current(r).Timezone {
		writeError(w, 400, "Используйте часовой пояс вашего профиля")
		return
	}
	zone, err := time.LoadLocation(in.Timezone)
	if err != nil || in.LocalDate > time.Now().In(zone).Format("2006-01-02") {
		writeError(w, 400, "Укажите корректную дату и часовой пояс")
		return
	}
	scores := any(map[string]string{})
	suicidal := false
	switch in.Kind {
	case "ema":
		var e scoring.EMA
		if err = parseAnswers(in.Answers, &e); err == nil {
			err = e.Validate()
		}
		if err != nil {
			writeError(w, 400, "Заполните все ответы EMA: оценки 0–10 и ответ Да/Нет")
			return
		}
		suicidal = *e.Suicidal
	case "csd":
		var c scoring.CSD
		if err = parseAnswers(in.Answers, &c); err == nil {
			scores, err = scoring.CalculateCSD(c)
		}
		if err != nil {
			writeError(w, 400, "Проверьте время и оценки дневника сна")
			return
		}
		if c.Bedtime == "" || c.RiseTime == "" || c.TotalSleep == nil {
			writeError(w, 400, "Укажите время в постель, подъёма и общее время сна")
			return
		}
	default:
		writeError(w, 400, "Неизвестный дневник")
		return
	}
	rawScores, err := json.Marshal(scores)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p := current(r)
	ctx := r.Context()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rollback(tx, ctx)
	if _, err = tx.Exec(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, p.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	var existing string
	var same bool
	err = tx.QueryRow(ctx, `SELECT id,kind=$3 AND local_date=$4::date AND timezone=$5 AND answers=$6::jsonb AND supersedes_id IS NOT DISTINCT FROM $7::uuid FROM diary_entries WHERE patient_id=$1 AND form_id=$2`, p.ID, in.FormID, in.Kind, in.LocalDate, in.Timezone, string(in.Answers), in.Supersedes).Scan(&existing, &same)
	if err == nil {
		if !same {
			writeError(w, 409, "Идентификатор уже использован для других данных")
			return
		}
		writeJSON(w, 200, map[string]string{"id": existing})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		s.fail(w, r, err)
		return
	}
	if in.Supersedes != nil {
		var valid bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM current_entries WHERE id=$1 AND patient_id=$2 AND kind=$3 AND local_date=$4::date AND timezone=$5)`, *in.Supersedes, p.ID, in.Kind, in.LocalDate, in.Timezone).Scan(&valid)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if !valid {
			writeError(w, 409, "Запись уже изменена. Обновите историю")
			return
		}
	} else {
		var allowed bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assignments WHERE patient_id=$1 AND kind=$2 AND status='active' AND $3::date BETWEEN start_date AND end_date AND mod(($3::date-start_date),interval_days)=0)`, p.ID, in.Kind, in.LocalDate).Scan(&allowed)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if !allowed {
			writeError(w, 409, "На эту дату нет назначения дневника. Напишите врачу")
			return
		}

		var duplicate bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM current_entries WHERE patient_id=$1 AND kind=$2 AND local_date=$3::date)`, p.ID, in.Kind, in.LocalDate).Scan(&duplicate)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if duplicate {
			writeError(w, 409, "Дневник на эту дату уже заполнен. Используйте исправление в истории")
			return
		}
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO diary_entries(patient_id,kind,local_date,timezone,form_id,answers,scores,supersedes_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`, p.ID, in.Kind, in.LocalDate, in.Timezone, in.FormID, string(in.Answers), string(rawScores), in.Supersedes).Scan(&id)
	if err == nil && suicidal {
		_, err = createRisk(ctx, tx, p.ID, in.FormID)
	}
	if err == nil && in.Kind == "ema" {
		_, _, err = createDeteriorationAlert(ctx, tx, p.ID, in.LocalDate)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO outbox_events(type,patient_id,aggregate_id) VALUES('diary.submitted',$1,$2)`, p.ID, id)
	}
	if err == nil {
		err = audit(ctx, tx, p, "diary.submit", id, r.Context().Value(requestKey{}).(string))
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 201, map[string]string{"id": id})
}

// createDeteriorationAlert applies the versioned indicator from the supplied EMA
// workbook. It is an explainable prompt for clinician review, not a diagnosis.
func createDeteriorationAlert(ctx context.Context, tx pgx.Tx, patient, periodEnd string) (string, bool, error) {
	end, err := time.Parse("2006-01-02", periodEnd)
	if err != nil {
		return "", false, err
	}
	start := end.AddDate(0, 0, -2).Format("2006-01-02")
	rows, err := tx.Query(ctx, `SELECT id::text,local_date::text,answers FROM current_entries
 WHERE patient_id=$1 AND kind='ema' AND local_date BETWEEN $2::date AND $3::date
 ORDER BY local_date`, patient, start, periodEnd)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	type day struct {
		id, date string
		answers  scoring.EMA
	}
	days := []day{}
	for rows.Next() {
		var d day
		var raw []byte
		if err = rows.Scan(&d.id, &d.date, &raw); err != nil {
			return "", false, err
		}
		if err = json.Unmarshal(raw, &d.answers); err != nil {
			return "", false, err
		}
		days = append(days, d)
	}
	if err = rows.Err(); err != nil {
		return "", false, err
	}
	rows.Close()
	if len(days) != 3 || days[0].date != start || days[1].date != end.AddDate(0, 0, -1).Format("2006-01-02") || days[2].date != periodEnd {
		return "", false, nil
	}
	for _, d := range days {
		if err = d.answers.Validate(); err != nil {
			return "", false, nil
		}
	}
	delta := *days[2].answers.Mood - *days[0].answers.Mood
	if delta < 3 {
		return "", false, nil
	}
	details, err := json.Marshal(map[string]int{
		"baseline":  *days[0].answers.Mood,
		"current":   *days[2].answers.Mood,
		"delta":     delta,
		"threshold": 3,
	})
	if err != nil {
		return "", false, err
	}
	sources := []string{days[0].id, days[1].id, days[2].id}
	var alert string
	err = tx.QueryRow(ctx, `INSERT INTO alerts(patient_id,signal_id,type,rule_version,period_start,period_end,source_entry_ids,details,severity,title,reason,metric,baseline_value,current_value,delta)
 VALUES($1,NULL,'DETERIORATION',$2,$3::date,$4::date,$5::uuid[],$6::jsonb,'warning','Ухудшение динамики EMA','Настроение ухудшилось на 3 балла или более за три дня','mood',$7,$8,$9)
 ON CONFLICT DO NOTHING RETURNING id::text`, patient, deteriorationRuleVersion, start, periodEnd, sources, string(details), *days[0].answers.Mood, *days[2].answers.Mood, delta).Scan(&alert)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox_events(type,patient_id,aggregate_id) VALUES('deterioration.raised',$1,$2)
 ON CONFLICT(type,aggregate_id) DO NOTHING`, patient, alert)
	return alert, true, err
}

func createRisk(ctx context.Context, tx pgx.Tx, patient, form string) (string, error) {
	var signal, alert string
	_, err := tx.Exec(ctx, `INSERT INTO risk_signals(patient_id,form_id) VALUES($1,$2) ON CONFLICT(patient_id,form_id) DO NOTHING`, patient, form)
	if err != nil {
		return "", err
	}
	err = tx.QueryRow(ctx, `SELECT id FROM risk_signals WHERE patient_id=$1 AND form_id=$2`, patient, form).Scan(&signal)
	if err != nil {
		return "", err
	}
	err = tx.QueryRow(ctx, `INSERT INTO alerts(patient_id,signal_id) VALUES($1,$2) ON CONFLICT(signal_id) DO UPDATE SET signal_id=excluded.signal_id RETURNING id`, patient, signal).Scan(&alert)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox_events(type,patient_id,aggregate_id) VALUES('risk.raised',$1,$2) ON CONFLICT(type,aggregate_id) DO NOTHING`, patient, alert)
	return alert, err
}
func (s *Server) riskSignal(w http.ResponseWriter, r *http.Request) {
	var in struct {
		FormID string `json:"form_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !uuid(in.FormID) {
		writeError(w, 400, "Некорректный идентификатор формы")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rollback(tx, r.Context())
	id, err := createRisk(r.Context(), tx, current(r).ID, in.FormID)
	if err == nil {
		err = audit(r.Context(), tx, current(r), "risk.signal", id, r.Context().Value(requestKey{}).(string))
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 201, map[string]string{"id": id})
}
