package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	mediatype "mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const audioLimit = 12 << 20

type audioRecording struct {
	ID          string           `json:"id"`
	Task        string           `json:"task"`
	Device      string           `json:"device"`
	CreatedAt   time.Time        `json:"created_at"`
	RunID       string           `json:"run_id"`
	Status      string           `json:"status"`
	ErrorCode   string           `json:"error_code"`
	Result      json.RawMessage  `json:"result"`
	ResultRunID *string          `json:"result_run_id"`
	RunHistory  json.RawMessage  `json:"run_history"`
	RunTotal    int64            `json:"run_total"`
	Comparison  *audioComparison `json:"comparison,omitempty"`
}
type audioComparison struct {
	RecordingID string             `json:"recording_id"`
	Deltas      map[string]float64 `json:"deltas"`
}
type audioSegment struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}
type audioResult struct {
	Pipeline    string              `json:"pipeline"`
	Usable      bool                `json:"usable"`
	Metrics     map[string]*float64 `json:"metrics"`
	Segments    []audioSegment      `json:"segments"`
	ASRModel    string              `json:"asr_model"`
	ASREngine   string              `json:"asr_engine"`
	ASRRevision string              `json:"asr_revision"`
}

func (s *Server) uploadAudio(w http.ResponseWriter, r *http.Request) {
	if s.config.AudioDir == "" || s.config.AudioURL == "" {
		writeError(w, 503, "Обработка аудио не настроена")
		return
	}
	p := current(r)
	task, device, key := r.URL.Query().Get("task"), strings.TrimSpace(r.URL.Query().Get("device")), r.URL.Query().Get("key")
	mime, _, mimeErr := mediatype.ParseMediaType(r.Header.Get("Content-Type"))
	if (task != "reading" && task != "free" && task != "vowel") || len([]rune(device)) < 1 || len([]rune(device)) > 120 || !uuid(key) || r.Header.Get("X-Audio-Consent") != "audio-v1" {
		writeError(w, 400, "Укажите задание, устройство и согласие на обработку аудио")
		return
	}
	if mimeErr != nil {
		writeError(w, 415, "Некорректный тип аудиозаписи")
		return
	}
	switch mime {
	case "audio/x-m4a", "audio/m4a":
		mime = "audio/mp4"
	case "audio/wav", "audio/x-wav", "audio/webm", "audio/ogg", "audio/mp4", "audio/mpeg":
	default:
		writeError(w, 415, "Поддерживаются WAV, WebM, Ogg, MP4 и MP3")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, audioLimit)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, 413, "Максимальный размер записи — 12 МБ")
		return
	}
	if len(data) < 44 {
		writeError(w, 400, "Запись пуста или повреждена")
		return
	}
	checksum := hash(string(data))
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rollback(tx, r.Context())
	// Serialize idempotent requests before any file creation, including conflicting payloads.
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "audio-upload:"+p.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	var id, oldHash, oldTask, oldDevice string
	err = tx.QueryRow(r.Context(), `SELECT id::text,sha256,task,device FROM audio_recordings WHERE patient_id=$1 AND request_key=$2`, p.ID, key).Scan(&id, &oldHash, &oldTask, &oldDevice)
	if err == nil {
		if oldHash != checksum || oldTask != task || oldDevice != device {
			writeError(w, 409, "Этот ключ уже использован для другой записи")
			return
		}
		writeJSON(w, 200, map[string]string{"id": id})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		s.fail(w, r, err)
		return
	}
	var count int
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM audio_recordings WHERE patient_id=$1 AND created_at>now()-interval '1 day'`, p.ID).Scan(&count); err != nil {
		s.fail(w, r, err)
		return
	}
	if count >= 20 {
		writeError(w, 429, "Не более 20 записей за сутки")
		return
	}
	object := token()
	path := filepath.Join(s.config.AudioDir, object)
	if err = os.WriteFile(path, data, 0600); err != nil {
		s.fail(w, r, err)
		return
	}
	retainObject := false
	defer func() {
		if !retainObject {
			if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				slog.Error("audio orphan cleanup failed", "error_type", fmt.Sprintf("%T", removeErr))
			}
		}
	}()
	err = tx.QueryRow(r.Context(), `INSERT INTO audio_recordings(patient_id,request_key,task,device,object_key,sha256,mime,size_bytes,consent_version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'audio-v1') RETURNING id::text`, p.ID, key, task, device, object, checksum, mime, len(data)).Scan(&id)
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO audio_runs(recording_id) VALUES($1)`, id)
	}
	if err == nil {
		err = audit(r.Context(), tx, p, "audio.upload", id, r.Context().Value(requestKey{}).(string))
	}
	if err == nil {
		// A network error during COMMIT has an ambiguous outcome. Retain the private
		// object so a committed recording never loses its bytes. Reconcile old orphans.
		retainObject = true
		err = tx.Commit(r.Context())
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	retainObject = true
	writeJSON(w, 201, map[string]string{"id": id})
}

func (s *Server) listAudio(w http.ResponseWriter, r *http.Request) {
	patient := r.PathValue("id")
	if !s.authorized(r, patient) {
		writeError(w, 403, "Нет доступа к аудио пациента")
		return
	}
	if err := s.recordRead(r, "audio.list", patient); err != nil {
		s.fail(w, r, err)
		return
	}
	selected := r.PathValue("audio")
	if selected != "" && !uuid(selected) {
		writeError(w, 404, "Запись не найдена")
		return
	}
	limit, cursor, pageErr := pagination(r)
	if pageErr != nil {
		writeError(w, 400, "Проверьте страницу аудио")
		return
	}
	var at *time.Time
	cursorID := "00000000-0000-0000-0000-000000000000"
	if cursor != nil {
		at = &cursor.Time
		cursorID = cursor.ID
	}
	var total int64
	if err := s.db.QueryRow(r.Context(), `SELECT count(*) FROM audio_recordings WHERE patient_id=$1`, patient).Scan(&total); err != nil {
		s.fail(w, r, err)
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT a.id::text,a.task,a.device,a.created_at,j.id::text,j.status,j.error_code,COALESCE(g.result,'null'::jsonb),g.id::text,
 (SELECT coalesce(jsonb_agg(to_jsonb(h) ORDER BY h.created_at DESC,h.id DESC),'[]'::jsonb) FROM (SELECT id,status,created_at,finished_at,error_code,result->>'pipeline' pipeline FROM audio_runs WHERE recording_id=a.id ORDER BY created_at DESC,id DESC LIMIT 20) h),
 (SELECT count(*) FROM audio_runs WHERE recording_id=a.id)
 FROM audio_recordings a JOIN LATERAL(SELECT * FROM audio_runs WHERE recording_id=a.id ORDER BY created_at DESC,id DESC LIMIT 1) j ON true
 LEFT JOIN LATERAL(SELECT * FROM audio_runs WHERE recording_id=a.id AND status='ready' ORDER BY created_at DESC,id DESC LIMIT 1) g ON true
 WHERE a.patient_id=$1 AND (nullif($5,'')::uuid IS NULL OR a.id=nullif($5,'')::uuid) AND ($2::timestamptz IS NULL OR (a.created_at,a.id)<($2::timestamptz,$3::uuid)) ORDER BY a.created_at DESC,a.id DESC LIMIT $4`, patient, at, cursorID, limit+1, selected)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rows.Close()
	records := []audioRecording{}
	for rows.Next() {
		var a audioRecording
		if err = rows.Scan(&a.ID, &a.Task, &a.Device, &a.CreatedAt, &a.RunID, &a.Status, &a.ErrorCode, &a.Result, &a.ResultRunID, &a.RunHistory, &a.RunTotal); err != nil {
			s.fail(w, r, err)
			return
		}
		records = append(records, a)
	}
	if err = rows.Err(); err != nil {
		s.fail(w, r, err)
		return
	}
	var next *string
	if len(records) > limit {
		v := encodeCursor(records[limit-1].CreatedAt, records[limit-1].ID)
		next = &v
		records = records[:limit]
	}
	for i := range records {
		var now audioResult
		if json.Unmarshal(records[i].Result, &now) != nil || !now.Usable {
			continue
		}
		for j := i + 1; j < len(records); j++ {
			prior := records[j]
			if prior.Task != records[i].Task || prior.Device != records[i].Device {
				continue
			}
			var old audioResult
			if json.Unmarshal(prior.Result, &old) != nil || !old.Usable || old.Pipeline != now.Pipeline {
				continue
			}
			delta := map[string]float64{}
			for _, key := range []string{"f0_median_hz", "f0_sd_hz", "pause_ratio", "words_per_minute", "hnr_db"} {
				if key == "words_per_minute" && (now.ASRModel != old.ASRModel || now.ASREngine != old.ASREngine || now.ASRRevision == "" || now.ASRRevision != old.ASRRevision) {
					continue
				}
				if now.Metrics[key] != nil && old.Metrics[key] != nil {
					delta[key] = *now.Metrics[key] - *old.Metrics[key]
				}
			}
			records[i].Comparison = &audioComparison{RecordingID: prior.ID, Deltas: delta}
			break
		}
	}
	if selected != "" {
		if len(records) == 0 {
			writeError(w, 404, "Запись не найдена")
			return
		}
		writeJSON(w, 200, records[0])
		return
	}
	writeJSON(w, 200, map[string]any{"recordings": records, "total": total, "next_cursor": next})
}

func (s *Server) audioFile(w http.ResponseWriter, r *http.Request) {
	patient, id := r.PathValue("id"), r.PathValue("audio")
	if !s.authorized(r, patient) {
		writeError(w, 403, "Нет доступа к аудио пациента")
		return
	}
	if !uuid(id) {
		writeError(w, 404, "Запись не найдена")
		return
	}
	var object, mime string
	err := s.db.QueryRow(r.Context(), `SELECT object_key,mime FROM audio_recordings WHERE id=$1 AND patient_id=$2`, id, patient).Scan(&object, &mime)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "Запись не найдена")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err = s.recordRead(r, "audio.play", id); err != nil {
		s.fail(w, r, err)
		return
	}
	file, err := os.Open(filepath.Join(s.config.AudioDir, object))
	if err != nil {
		writeError(w, 404, "Файл записи недоступен")
		return
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			slog.Debug("audio file close failed")
		}
	}()
	info, err := file.Stat()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Disposition", "inline; filename=recording")
	http.ServeContent(w, r, "recording", info.ModTime(), file)
}

func (s *Server) retryAudio(w http.ResponseWriter, r *http.Request) {
	patient, id := r.PathValue("id"), r.PathValue("audio")
	if !s.authorized(r, patient) {
		writeError(w, 403, "Нет доступа к аудио пациента")
		return
	}
	if !uuid(id) {
		writeError(w, 404, "Запись не найдена")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rollback(tx, r.Context())
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended('audio-retry:'||$1::text,0))`, id); err != nil {
		s.fail(w, r, err)
		return
	}
	var found string
	err = tx.QueryRow(r.Context(), `SELECT id::text FROM audio_recordings WHERE id=$1 AND patient_id=$2`, id, patient).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "Запись не найдена")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var status string
	var count int
	err = tx.QueryRow(r.Context(), `SELECT status FROM audio_runs WHERE recording_id=$1 ORDER BY created_at DESC,id DESC LIMIT 1`, id).Scan(&status)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if status == "processing" || status == "queued" {
		writeError(w, 409, "Запись уже обрабатывается")
		return
	}
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM audio_runs WHERE recording_id=$1 AND created_at>now()-interval '1 day'`, id).Scan(&count); err != nil {
		s.fail(w, r, err)
		return
	}
	if count >= 5 {
		writeError(w, 429, "Лимит повторной обработки на сегодня исчерпан")
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO audio_runs(recording_id) VALUES($1)`, id)
	if err == nil {
		err = audit(r.Context(), tx, current(r), "audio.retry", id, r.Context().Value(requestKey{}).(string))
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 202, map[string]string{"status": "queued"})
}

func (s *Server) audioCall(ctx context.Context, path string, body []byte, contentType string) ([]byte, int, error) {
	budget := 4 * time.Minute
	if path == "/search" {
		budget = 60 * time.Second
	}
	operation, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	req, err := http.NewRequestWithContext(operation, "POST", strings.TrimRight(s.config.AudioURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+s.config.AudioToken)
	req.Header.Set("Content-Type", contentType)
	client := http.Client{Timeout: 4 * time.Minute}
	response, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			slog.Debug("audio response close failed")
		}
	}()
	data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	return data, response.StatusCode, err
}

// RunAudio independently drains persisted audio work so ML cannot block alerts.
func (s *Server) RunAudio(ctx context.Context) {
	if s.config.AudioURL == "" {
		return
	}
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := s.processAudio(ctx); err != nil && ctx.Err() == nil {
				slog.Error("audio processing failed", "error_type", fmt.Sprintf("%T", err))
			}
		}
	}
}

func (s *Server) processAudio(ctx context.Context) error {
	// Expired final attempts need a terminal state even after a process crash.
	if _, err := s.db.Exec(ctx, `UPDATE audio_runs SET status='failed',error_code='processing_timeout',finished_at=now() WHERE status='processing' AND lease_until<now() AND attempts>=3`); err != nil {
		return err
	}
	lease := token()
	var run, recording, patient, object, task string
	err := s.db.QueryRow(ctx, `WITH candidate AS(SELECT id FROM audio_runs WHERE attempts<3 AND ((status='queued' AND available_at<=now()) OR(status='processing' AND lease_until<now())) ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED), claimed AS(UPDATE audio_runs j SET status='processing',attempts=attempts+1,lease_until=now()+interval '5 minutes',lease_token=$1 FROM candidate c WHERE j.id=c.id RETURNING j.id,j.recording_id) SELECT c.id::text,a.id::text,a.patient_id::text,a.object_key,a.task FROM claimed c JOIN audio_recordings a ON a.id=c.recording_id`, lease).Scan(&run, &recording, &patient, &object, &task)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	data, readErr := os.ReadFile(filepath.Join(s.config.AudioDir, object))
	var result []byte
	var code int
	callErr := readErr
	if callErr == nil {
		result, code, callErr = s.audioCall(ctx, "/analyze?task="+task, data, "application/octet-stream")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var parsed audioResult
	if callErr != nil || code != 200 || json.Unmarshal(result, &parsed) != nil || parsed.Pipeline == "" {
		permanent := code == 422
		errorCode := "processing_unavailable"
		if permanent {
			errorCode = "invalid_audio"
		}
		_, err = s.db.Exec(ctx, `UPDATE audio_runs SET status=CASE WHEN attempts>=3 OR $3 THEN 'failed' ELSE 'queued' END,error_code=$4,available_at=now()+interval '30 seconds',lease_until=NULL,finished_at=CASE WHEN attempts>=3 OR $3 THEN now() ELSE NULL END WHERE id=$1 AND lease_token=$2 AND status='processing'`, run, lease, permanent, errorCode)
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx, ctx)
	tag, err := tx.Exec(ctx, `UPDATE audio_runs SET status='ready',result=$3::jsonb,error_code='',finished_at=now(),lease_until=NULL WHERE id=$1 AND lease_token=$2 AND status='processing' AND lease_until>now()`, run, lease, string(result))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	for i, seg := range parsed.Segments {
		if seg.Start < 0 || seg.End <= seg.Start || seg.End > 121 || strings.TrimSpace(seg.Text) == "" || len([]rune(seg.Text)) > 4000 {
			return fmt.Errorf("invalid audio segment")
		}
		if _, err = tx.Exec(ctx, `INSERT INTO audio_chunks(run_id,ordinal,start_seconds,end_seconds,text) VALUES($1,$2,$3,$4,$5)`, run, i, seg.Start, seg.End, seg.Text); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO outbox_events(type,patient_id,aggregate_id) VALUES('audio.ready',$1,$2)`, patient, run); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Server) searchAudio(w http.ResponseWriter, r *http.Request) {
	patient := r.PathValue("id")
	if !s.authorized(r, patient) {
		writeError(w, 403, "Нет доступа к аудио пациента")
		return
	}
	var input struct {
		Query string `json:"query"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Query = strings.TrimSpace(input.Query)
	if len([]rune(input.Query)) < 2 || len([]rune(input.Query)) > 1000 {
		writeError(w, 400, "Введите вопрос длиной от 2 до 1000 символов")
		return
	}
	if s.config.AudioURL == "" {
		writeError(w, 503, "Поиск по аудио не настроен")
		return
	}
	if err := s.recordRead(r, "audio.search", patient); err != nil {
		s.fail(w, r, err)
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT c.id::text,a.id::text,c.start_seconds,c.end_seconds,c.text FROM audio_recordings a JOIN LATERAL(SELECT id,status FROM audio_runs WHERE recording_id=a.id AND status='ready' ORDER BY created_at DESC,id DESC LIMIT 1) j ON j.status='ready' JOIN audio_chunks c ON c.run_id=j.id WHERE a.patient_id=$1 ORDER BY a.created_at DESC,c.ordinal LIMIT 501`, patient)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	chunks := []map[string]any{}
	for rows.Next() {
		var id, recording, text string
		var start, end float64
		if err = rows.Scan(&id, &recording, &start, &end, &text); err != nil {
			break
		}
		chunks = append(chunks, map[string]any{"id": id, "recording_id": recording, "start": start, "end": end, "text": text})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	truncated := len(chunks) > 500
	if truncated {
		chunks = chunks[:500]
	}
	body, err := json.Marshal(map[string]any{"query": input.Query, "chunks": chunks})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data, status, err := s.audioCall(r.Context(), "/search", body, "application/json")
	if err != nil || status != 200 {
		writeError(w, 503, "Поиск пока недоступен. Модель может загружаться; повторите позже")
		return
	}
	// Recheck after slow inference, including relationship/session revocation.
	if !s.authorized(r, patient) {
		writeError(w, 403, "Доступ к пациенту отозван")
		return
	}
	var active bool
	if err = s.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires_at>now() AND u.active AND s.auth_role=u.role AND s.auth_version=u.auth_version)`, current(r).Session).Scan(&active); err != nil || !active {
		writeError(w, 401, "Сессия завершена")
		return
	}
	var output map[string]any
	if err = json.Unmarshal(data, &output); err != nil {
		s.fail(w, r, err)
		return
	}
	output["truncated"] = truncated
	writeJSON(w, 200, output)
}
