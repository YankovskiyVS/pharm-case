package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"psitech/internal/scoring"
)

type reportAudio struct {
	RecordingID string          `json:"recording_id"`
	RunID       string          `json:"run_id"`
	RecordedAt  time.Time       `json:"recorded_at"`
	Task        string          `json:"task"`
	Device      string          `json:"device"`
	SHA256      string          `json:"sha256"`
	Result      json.RawMessage `json:"result"`
}
type reportDiaryEntry struct {
	ID             string          `json:"id"`
	Kind           string          `json:"kind"`
	LocalDate      string          `json:"local_date"`
	Answers        json.RawMessage `json:"answers"`
	Scores         json.RawMessage `json:"scores"`
	ScoringVersion string          `json:"scoring_version"`
}
type reportDiaries struct {
	PeriodStart    string               `json:"period_start"`
	PeriodEnd      string               `json:"period_end"`
	Timezone       string               `json:"timezone"`
	SourceEntryIDs []string             `json:"source_entry_ids"`
	Entries        []reportDiaryEntry   `json:"entries"`
	EMA            scoring.Summary      `json:"ema_summary"`
	CSD            scoring.SleepSummary `json:"csd_summary"`
}
type reportComparison struct {
	RecordingID string             `json:"recording_id"`
	RunID       string             `json:"run_id"`
	Deltas      map[string]float64 `json:"deltas"`
}
type clinicalReportSnapshot struct {
	ModelRequest   map[string]any    `json:"model_request"`
	CapturedAt     time.Time         `json:"captured_at"`
	ContextVersion json.RawMessage   `json:"context_version"`
	Audio          reportAudio       `json:"audio"`
	Diaries        reportDiaries     `json:"diaries"`
	Comparison     *reportComparison `json:"comparison"`
	Limitations    []string          `json:"limitations"`
}

const clinicalReportFields = `cr.id,cr.patient_id,cr.recording_id,cr.run_id,cr.context_id,cr.author_id,u.name author_name,cr.created_at,cr.source_snapshot,cr.description,
 j.status model_status,j.reason_code model_reason,j.attempts model_attempts,vr.result model_result,
 (SELECT coalesce(jsonb_agg(jsonb_build_object('id',c.id,'author_id',c.author_id,'author_name',d.name,'text',c.text,'created_at',c.created_at) ORDER BY c.created_at,c.id),'[]'::jsonb) FROM clinical_report_conclusions c JOIN users d ON d.id=c.author_id WHERE c.report_id=cr.id) conclusions`
const clinicalReportFrom = ` FROM clinical_audio_reports cr JOIN users u ON u.id=cr.author_id JOIN voicefm_jobs j ON j.report_id=cr.id LEFT JOIN voicefm_results vr ON vr.report_id=cr.id `

func (s *Server) clinicalRecordingAccess(w http.ResponseWriter, r *http.Request) bool {
	if !s.clinicalAccess(w, r) {
		return false
	}
	if !uuid(r.PathValue("audio")) {
		writeError(w, 404, "Запись не найдена")
		return false
	}
	var exists bool
	if err := s.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM audio_recordings WHERE id=$1 AND patient_id=$2)`, r.PathValue("audio"), r.PathValue("id")).Scan(&exists); err != nil {
		s.fail(w, r, err)
		return false
	}
	if !exists {
		writeError(w, 404, "Запись не найдена")
		return false
	}
	return true
}
func (s *Server) clinicalReports(w http.ResponseWriter, r *http.Request) {
	if !s.clinicalRecordingAccess(w, r) {
		return
	}
	if err := s.recordRead(r, "clinical_report.list", r.PathValue("audio")); err != nil {
		s.fail(w, r, err)
		return
	}
	s.listPage(w, r, `SELECT `+clinicalReportFields+clinicalReportFrom+` WHERE cr.patient_id=$1 AND cr.recording_id=$2`, r.PathValue("id"), r.PathValue("audio"))
}
func (s *Server) clinicalReportDetail(w http.ResponseWriter, r *http.Request) {
	if !s.clinicalRecordingAccess(w, r) {
		return
	}
	if !uuid(r.PathValue("report")) {
		writeError(w, 404, "Отчёт не найден")
		return
	}
	data, err := s.jsonQuery(r.Context(), `SELECT to_jsonb(x) FROM (SELECT `+clinicalReportFields+clinicalReportFrom+` WHERE cr.patient_id=$1 AND cr.recording_id=$2 AND cr.id=$3)x`, r.PathValue("id"), r.PathValue("audio"), r.PathValue("report"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(data) == 0 {
		writeError(w, 404, "Отчёт не найден")
		return
	}
	if err = s.recordRead(r, "clinical_report.read", r.PathValue("report")); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, data)
}
func (s *Server) createClinicalReport(w http.ResponseWriter, r *http.Request) {
	if !s.clinicalRecordingAccess(w, r) {
		return
	}
	var in struct {
		RequestKey string `json:"request_key"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !uuid(in.RequestKey) {
		writeError(w, 400, "Укажите ключ запроса")
		return
	}
	patient, recording, doctor := r.PathValue("id"), r.PathValue("audio"), current(r).ID
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rollback(tx, r.Context())
	if err = lockClinicalAccess(r.Context(), tx, patient, doctor); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 403, "Доступ к пациенту отозван")
		} else {
			s.fail(w, r, err)
		}
		return
	}
	var prior []byte
	err = tx.QueryRow(r.Context(), `SELECT to_jsonb(x) FROM (SELECT `+clinicalReportFields+clinicalReportFrom+` WHERE cr.recording_id=$1 AND cr.author_id=$2 AND cr.request_key=$3)x`, recording, doctor, in.RequestKey).Scan(&prior)
	if err == nil {
		writeJSON(w, 200, json.RawMessage(prior))
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		s.fail(w, r, err)
		return
	}
	snapshot, contextID, err := buildClinicalSnapshot(r.Context(), tx, patient, recording)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 409, "Для отчёта нужен завершённый успешный анализ записи")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	snapshot.ModelRequest = map[string]any{"provider": "voicefm", "mode": s.voiceFMMode(), "is_mock": s.voiceFMMode() == "mock", "clinical_use": false}
	if s.voiceFMMode() == "mock" {
		snapshot.ModelRequest["input_used_for_prediction"] = false
	}
	raw, _ := json.Marshal(snapshot)
	description := describeClinicalSnapshot(snapshot)
	var id string
	err = tx.QueryRow(r.Context(), `INSERT INTO clinical_audio_reports(patient_id,recording_id,run_id,context_id,author_id,source_snapshot,description,request_key) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id::text`, patient, recording, snapshot.Audio.RunID, contextID, doctor, raw, description, in.RequestKey).Scan(&id)
	status, reason := "queued", ""
	if s.config.VoiceFMURL == "" {
		status, reason = "model_unavailable", "not_configured"
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO voicefm_jobs(report_id,status,reason_code,finished_at) VALUES($1,$2,$3,CASE WHEN $2='model_unavailable' THEN now() END)`, id, status, reason)
	}
	if err == nil {
		err = audit(r.Context(), tx, current(r), "clinical_report.create", id, r.Context().Value(requestKey{}).(string))
	}
	var result []byte
	if err == nil {
		err = tx.QueryRow(r.Context(), `SELECT to_jsonb(x) FROM (SELECT `+clinicalReportFields+clinicalReportFrom+` WHERE cr.id=$1)x`, id).Scan(&result)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 201, json.RawMessage(result))
}

func buildClinicalSnapshot(ctx context.Context, tx pgx.Tx, patient, recording string) (clinicalReportSnapshot, *string, error) {
	s := clinicalReportSnapshot{CapturedAt: time.Now().UTC(), ContextVersion: json.RawMessage("null"), Limitations: []string{
		"Описательный отчёт объединяет зафиксированные источники; он не устанавливает диагноз и не рекомендует лечение.",
		"Акустические показатели зависят от задания, устройства, шума, языка, состояния голоса и лекарств. Популяционные нормы и клинические пороги не применялись.",
		"VoiceFM — отдельный исследовательский источник. Реальные оценки без проверенных весов и обученной диагностической головы не рассчитываются. Демонстрационные значения синтетические и не характеризуют пациента. Валидность для русской речи и конкретного устройства не установлена.",
	}}
	err := tx.QueryRow(ctx, `SELECT a.id::text,j.id::text,a.created_at,a.task,a.device,a.sha256,j.result FROM audio_recordings a JOIN LATERAL(SELECT id,result FROM audio_runs WHERE recording_id=a.id AND status='ready' ORDER BY created_at DESC,id DESC LIMIT 1)j ON true WHERE a.id=$1 AND a.patient_id=$2`, recording, patient).Scan(&s.Audio.RecordingID, &s.Audio.RunID, &s.Audio.RecordedAt, &s.Audio.Task, &s.Audio.Device, &s.Audio.SHA256, &s.Audio.Result)
	if err != nil {
		return s, nil, err
	}
	var contextID *string
	var contextRaw []byte
	err = tx.QueryRow(ctx, `SELECT v.id::text,to_jsonb(x) FROM clinical_context_versions v CROSS JOIN LATERAL(SELECT v.id,v.patient_id,v.version,v.context,v.author_id,u.name author_name,v.created_at FROM users u WHERE u.id=v.author_id)x WHERE v.patient_id=$1 ORDER BY v.version DESC LIMIT 1`, patient).Scan(&contextID, &contextRaw)
	if err == nil {
		s.ContextVersion = contextRaw
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return s, nil, err
	}
	if err = tx.QueryRow(ctx, `SELECT timezone FROM users WHERE id=$1`, patient).Scan(&s.Diaries.Timezone); err != nil {
		return s, nil, err
	}
	zone, err := time.LoadLocation(s.Diaries.Timezone)
	if err != nil {
		return s, nil, err
	}
	end := s.CapturedAt.In(zone)
	s.Diaries.PeriodStart = end.AddDate(0, 0, -13).Format("2006-01-02")
	s.Diaries.PeriodEnd = end.Format("2006-01-02")
	s.Diaries.Entries = []reportDiaryEntry{}
	s.Diaries.SourceEntryIDs = []string{}
	days := []scoring.DailyEMA{}
	nights := []scoring.DailyCSD{}
	rows, err := tx.Query(ctx, `SELECT id::text,kind,local_date::text,answers,scores,scoring_version FROM current_entries WHERE patient_id=$1 AND local_date BETWEEN $2::date AND $3::date ORDER BY local_date,kind,id`, patient, s.Diaries.PeriodStart, s.Diaries.PeriodEnd)
	if err != nil {
		return s, nil, err
	}
	for rows.Next() {
		var e reportDiaryEntry
		if err = rows.Scan(&e.ID, &e.Kind, &e.LocalDate, &e.Answers, &e.Scores, &e.ScoringVersion); err != nil {
			break
		}
		s.Diaries.Entries = append(s.Diaries.Entries, e)
		s.Diaries.SourceEntryIDs = append(s.Diaries.SourceEntryIDs, e.ID)
		if e.Kind == "ema" {
			var a scoring.EMA
			if err = json.Unmarshal(e.Answers, &a); err != nil {
				break
			}
			days = append(days, scoring.DailyEMA{Date: e.LocalDate, Answers: a})
		} else if e.Kind == "csd" {
			var a scoring.CSD
			if err = json.Unmarshal(e.Answers, &a); err != nil {
				break
			}
			nights = append(nights, scoring.DailyCSD{Date: e.LocalDate, Answers: a})
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return s, nil, err
	}
	s.Diaries.EMA = scoring.SummarizeEMA(days, end)
	s.Diaries.CSD = scoring.SummarizeCSD(nights, end)
	var now audioResult
	if err = json.Unmarshal(s.Audio.Result, &now); err != nil {
		return s, nil, err
	}
	var previousRecord, previousRun string
	var previous []byte
	err = tx.QueryRow(ctx, `SELECT a.id::text,j.id::text,j.result FROM audio_recordings a JOIN LATERAL(SELECT id,result FROM audio_runs WHERE recording_id=a.id AND status='ready' ORDER BY created_at DESC,id DESC LIMIT 1)j ON true WHERE a.patient_id=$1 AND a.task=$2 AND a.device=$3 AND (a.created_at,a.id)<($4::timestamptz,$5::uuid) AND j.result->>'pipeline'=$6 AND j.result->>'usable'='true' ORDER BY a.created_at DESC,a.id DESC LIMIT 1`, patient, s.Audio.Task, s.Audio.Device, s.Audio.RecordedAt, recording, now.Pipeline).Scan(&previousRecord, &previousRun, &previous)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return s, nil, err
	}
	if err == nil && now.Usable {
		var old audioResult
		if json.Unmarshal(previous, &old) == nil {
			deltas := map[string]float64{}
			for key, value := range now.Metrics {
				prior := old.Metrics[key]
				if value == nil || prior == nil || math.IsNaN(*value) || math.IsNaN(*prior) || math.IsInf(*value, 0) || math.IsInf(*prior, 0) {
					continue
				}
				if key == "words_per_minute" && (now.ASRRevision == "" || now.ASRRevision != old.ASRRevision || now.ASRModel != old.ASRModel || now.ASREngine != old.ASREngine) {
					continue
				}
				deltas[key] = *value - *prior
			}
			s.Comparison = &reportComparison{RecordingID: previousRecord, RunID: previousRun, Deltas: deltas}
		}
	}
	return s, contextID, nil
}

func describeClinicalSnapshot(s clinicalReportSnapshot) string {
	var b strings.Builder
	if s.ModelRequest["is_mock"] == true {
		b.WriteString("ДЕМОНСТРАЦИОННЫЙ РЕЖИМ: результат модели синтетический, не оценка состояния пациента. Аудио не используется для предсказания демонстрационных значений. Ниже отдельно приведены фактические источники контекста и акустики.\n\n")
	}
	fmt.Fprintf(&b, "Описательный клинический отчёт от %s\nЗадание: %s. Устройство (со слов пациента): %s.\nЗапись: %s. Успешный анализ: %s.\n", s.CapturedAt.Format("02.01.2006 15:04 UTC"), map[string]string{"reading": "чтение", "free": "свободная речь", "vowel": "протяжный гласный"}[s.Audio.Task], s.Audio.Device, s.Audio.RecordingID, s.Audio.RunID)
	var v struct {
		Version    int                 `json:"version"`
		AuthorName string              `json:"author_name"`
		Context    clinicalContextData `json:"context"`
	}
	if string(s.ContextVersion) == "null" {
		b.WriteString("\nКлинический контекст не внесён; анамнез, диагнозы и лекарства неизвестны.\n")
	} else if json.Unmarshal(s.ContextVersion, &v) == nil {
		fmt.Fprintf(&b, "\nКонтекст, версия %d. Автор: %s. Сведения внесены врачом, автоматической проверки диагноза нет.\n", v.Version, v.AuthorName)
		for _, f := range []struct{ name, value string }{{"Медицинский анамнез", v.Context.MedicalHistory}, {"Психиатрический анамнез", v.Context.PsychiatricHistory}, {"Диагнозы, указанные врачом", v.Context.Diagnoses}, {"Лекарства, указанные врачом", v.Context.Medications}, {"Факторы влияния на голос", v.Context.Confounders}} {
			if f.value == "" {
				f.value = "не указано"
			}
			fmt.Fprintf(&b, "%s: %s\n", f.name, f.value)
		}
		if len(v.Context.ExternalScales) == 0 {
			b.WriteString("Внешние шкалы не внесены.\n")
		}
		for _, e := range v.Context.ExternalScales {
			if e.Total != nil {
				fmt.Fprintf(&b, "Внешняя шкала %s: %d, дата %s, источник: %s. %s\n", e.Scale, *e.Total, e.AssessedOn, e.Source, e.Notes)
			}
		}
	}
	var a audioResult
	if json.Unmarshal(s.Audio.Result, &a) == nil {
		fmt.Fprintf(&b, "\nАкустический конвейер: %s. Пригодность записи для расчёта: %t.\n", a.Pipeline, a.Usable)
		labels := map[string]string{"f0_median_hz": "Медиана основной частоты, Гц", "f0_sd_hz": "Разброс основной частоты, Гц", "pause_ratio": "Доля пауз (0–1)", "words_per_minute": "Слов в минуту по ASR", "hnr_db": "Отношение гармоник к шуму, дБ", "jitter_local": "Локальный jitter (доля)", "shimmer_local": "Локальный shimmer (доля)"}
		keys := make([]string, 0, len(a.Metrics))
		for k := range a.Metrics {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			label := labels[k]
			if label == "" {
				label = k
			}
			if value := a.Metrics[k]; value != nil {
				fmt.Fprintf(&b, "%s: %.4g\n", label, *value)
			} else {
				fmt.Fprintf(&b, "%s: нет данных\n", label)
			}
		}
	}
	if s.Comparison == nil {
		b.WriteString("Сопоставимой предшествующей записи с тем же заданием, устройством и конвейером нет.\n")
	} else {
		fmt.Fprintf(&b, "Сравнение с записью %s (анализ %s): разности зафиксированы в источниках отчёта, без оценки клинической значимости.\n", s.Comparison.RecordingID, s.Comparison.RunID)
	}

	if s.Comparison != nil {
		keys := make([]string, 0, len(s.Comparison.Deltas))
		for key := range s.Comparison.Deltas {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(&b, "Разность %s (текущая минус предыдущая): %+.4g.\n", key, s.Comparison.Deltas[key])
		}
	}
	fmt.Fprintf(&b, "\nДневники %s — %s (%s): EMA за последние 3 дня — %d из 3; CSD за 14 дней — %d из 14. Недостающие значения не заменялись нормой.\n", s.Diaries.PeriodStart, s.Diaries.PeriodEnd, s.Diaries.Timezone, s.Diaries.EMA.Count, s.Diaries.CSD.Count)

	for _, value := range []struct {
		label string
		v     *float64
	}{{"Среднее настроение EMA (0–10)", s.Diaries.EMA.Mood}, {"Средняя ангедония EMA (0–10)", s.Diaries.EMA.Anhedonia}, {"Средняя энергия EMA (0–10)", s.Diaries.EMA.Energy}, {"Средняя тревога EMA (0–10)", s.Diaries.EMA.Anxiety}, {"Среднее время засыпания CSD, мин", s.Diaries.CSD.MeanLatency}, {"Средний общий сон CSD, мин", s.Diaries.CSD.MeanTotalSleep}, {"Средняя эффективность сна CSD (доля)", s.Diaries.CSD.MeanSE}} {
		if value.v != nil {
			fmt.Fprintf(&b, "%s: %.4g.\n", value.label, *value.v)
		} else {
			fmt.Fprintf(&b, "%s: нет данных.\n", value.label)
		}
	}
	for _, line := range s.Limitations {
		b.WriteString("\n")
		b.WriteString(line)
	}
	b.WriteString("\nЗаключение лечащего врача добавляется отдельно и не является автоматическим выводом модели.")
	return b.String()
}

func (s *Server) appendClinicalConclusion(w http.ResponseWriter, r *http.Request) {
	if !s.clinicalRecordingAccess(w, r) {
		return
	}
	report := r.PathValue("report")
	if !uuid(report) {
		writeError(w, 404, "Отчёт не найден")
		return
	}
	var in struct {
		RequestKey string `json:"request_key"`
		Text       string `json:"text"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Text = strings.TrimSpace(in.Text)
	if !uuid(in.RequestKey) || in.Text == "" || !utf8.ValidString(in.Text) || len([]rune(in.Text)) > 10000 {
		writeError(w, 400, "Заключение должно содержать 1–10000 символов")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rollback(tx, r.Context())
	if err = lockClinicalAccess(r.Context(), tx, r.PathValue("id"), current(r).ID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 403, "Доступ к пациенту отозван")
		} else {
			s.fail(w, r, err)
		}
		return
	}
	var exists bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM clinical_audio_reports WHERE id=$1 AND recording_id=$2 AND patient_id=$3)`, report, r.PathValue("audio"), r.PathValue("id")).Scan(&exists); err != nil {
		s.fail(w, r, err)
		return
	}
	if !exists {
		writeError(w, 404, "Отчёт не найден")
		return
	}
	var id, old string
	err = tx.QueryRow(r.Context(), `SELECT id::text,text FROM clinical_report_conclusions WHERE report_id=$1 AND author_id=$2 AND request_key=$3`, report, current(r).ID, in.RequestKey).Scan(&id, &old)
	status := 201
	if err == nil {
		if old != in.Text {
			writeError(w, 409, "Ключ уже использован для другого заключения")
			return
		}
		status = 200
	} else if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(r.Context(), `INSERT INTO clinical_report_conclusions(report_id,author_id,text,request_key) VALUES($1,$2,$3,$4) RETURNING id::text`, report, current(r).ID, in.Text, in.RequestKey).Scan(&id)
		if err == nil {
			err = audit(r.Context(), tx, current(r), "clinical_conclusion.create", id, r.Context().Value(requestKey{}).(string))
		}
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var result []byte
	err = tx.QueryRow(r.Context(), `SELECT jsonb_build_object('id',c.id,'author_id',c.author_id,'author_name',u.name,'text',c.text,'created_at',c.created_at) FROM clinical_report_conclusions c JOIN users u ON u.id=c.author_id WHERE c.id=$1`, id).Scan(&result)
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, status, json.RawMessage(result))
}
