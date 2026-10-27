package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"psitech/internal/store"
)

func clinicalInput(base int, key string) map[string]any {
	return map[string]any{"base_version": base, "request_key": key, "context": map[string]any{"medical_history": "SYNTHETIC_PRIVATE_HISTORY", "psychiatric_history": "Synthetic assessed history", "diagnoses": "Documented by treating physician", "medications": "Reported medication; no advice", "confounders": "Synthetic recent respiratory illness", "external_scales": []any{map[string]any{"scale": "PHQ9", "total": 8, "assessed_on": "2026-10-25", "source": "Synthetic external clinician assessment"}}}}
}
func clinicalAudio(t *testing.T, f *fixture) string {
	t.Helper()
	f.server.config.AudioDir = t.TempDir()
	f.server.config.AudioURL = "http://unused-audio"
	w := f.upload(t, "86000000-0000-4000-8000-000000000001", "reading", []byte(strings.Repeat("x", 100)), f.patient, true)
	requireStatus(t, w, 201)
	id := decodeID(t, w)
	_, err := f.db.Exec(context.Background(), `UPDATE audio_runs SET status='ready',result='{"pipeline":"synthetic-acoustic-v1","usable":true,"asr_model":"test","asr_engine":"test","asr_revision":"test","metrics":{"f0_median_hz":180,"pause_ratio":0.2},"segments":[]}' WHERE recording_id=$1`, id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestIntegrationClinicalContextConcurrencyIdempotencyAndPrivacy(t *testing.T) {
	f := setup(t)
	base := "/patients/" + f.patient.ID + "/clinical-context"
	initial := f.request("GET", base, nil, f.doctor)
	requireStatus(t, initial, 200)
	if !strings.Contains(initial.Body.String(), `"version": 0`) && !strings.Contains(initial.Body.String(), `"version":0`) {
		t.Fatal(initial.Body.String())
	}
	input := clinicalInput(0, "87000000-0000-4000-8000-000000000001")
	first := f.request("POST", base, input, f.doctor)
	requireStatus(t, first, 201)
	id := decodeID(t, first)
	replay := f.request("POST", base, input, f.doctor)
	requireStatus(t, replay, 200)
	if decodeID(t, replay) != id {
		t.Fatal("context retry created another version")
	}
	input["context"].(map[string]any)["medical_history"] = "Changed payload"
	requireStatus(t, f.request("POST", base, input, f.doctor), 409)
	stale := clinicalInput(0, "87000000-0000-4000-8000-000000000002")
	requireStatus(t, f.request("POST", base, stale, f.doctor), 409)
	stale["base_version"] = 1
	requireStatus(t, f.request("POST", base, stale, f.doctor), 201)
	page := f.request("GET", base+"/history?limit=1", nil, f.doctor)
	requireStatus(t, page, 200)
	var history pageResult
	if json.Unmarshal(page.Body.Bytes(), &history) != nil || history.Total != 2 || len(history.Items) != 1 || history.NextCursor == nil {
		t.Fatal("context history not complete/paged")
	}
	for _, p := range []principal{f.patient, f.other} {
		requireStatus(t, f.request("GET", base, nil, p), 403)
		requireStatus(t, f.request("POST", base, input, p), 403)
		requireStatus(t, f.request("GET", base+"/history", nil, p), 403)
	}
	for _, route := range []string{"/dashboard", "/patients/" + f.patient.ID + "/export"} {
		w := f.request("GET", route, nil, f.patient)
		requireStatus(t, w, 200)
		if strings.Contains(w.Body.String(), "SYNTHETIC_PRIVATE_HISTORY") {
			t.Fatal("doctor context leaked to patient")
		}
	}
	if _, err := f.db.Exec(context.Background(), `UPDATE care_links SET active=false WHERE patient_id=$1 AND doctor_id=$2`, f.patient.ID, f.doctor.ID); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.request("GET", base, nil, f.doctor), 403)
}

func TestIntegrationClinicalContextSingleWinnerAndExternalTotalValidation(t *testing.T) {
	f := setup(t)
	base := "/patients/" + f.patient.ID + "/clinical-context"
	var wg sync.WaitGroup
	var created, conflicts atomic.Int32
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := f.request("POST", base, clinicalInput(0, fmt.Sprintf("88000000-0000-4000-8000-%012d", i)), f.doctor)
			switch w.Code {
			case 201:
				created.Add(1)
			case 409:
				conflicts.Add(1)
			default:
				t.Errorf("unexpected concurrent status %d %s", w.Code, w.Body.String())
			}
		}(i)
	}
	wg.Wait()
	if created.Load() != 1 || conflicts.Load() != 1 {
		t.Fatal("lost optimistic concurrency protection")
	}
	for i, scale := range []string{"PHQ9", "GAD7", "MADRS", "HDRS17"} {
		in := clinicalInput(1, fmt.Sprintf("89000000-0000-4000-8000-%012d", i))
		e := map[string]any{"scale": scale, "total": 100, "assessed_on": "2026-10-25", "source": "Synthetic external form"}
		in["context"].(map[string]any)["external_scales"] = []any{e}
		requireStatus(t, f.request("POST", base, in, f.doctor), 400)
		delete(e, "total")
		requireStatus(t, f.request("POST", base, in, f.doctor), 400)
		e["total"] = 0
		delete(e, "source")
		requireStatus(t, f.request("POST", base, in, f.doctor), 400)
	}
}

func TestIntegrationClinicalReportSnapshotsMissingModelAndConclusions(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	contextPath := "/patients/" + f.patient.ID + "/clinical-context"
	requireStatus(t, f.request("POST", contextPath, clinicalInput(0, "90000000-0000-4000-8000-000000000001"), f.doctor), 201)
	entry := f.request("POST", "/patient/diaries", emaInput(f.today, "90000000-0000-4000-8000-000000000002", false), f.patient)
	requireStatus(t, entry, 201)
	entryID := decodeID(t, entry)
	recording := clinicalAudio(t, f)
	base := "/patients/" + f.patient.ID + "/audio/" + recording + "/clinical-reports"
	in := map[string]string{"request_key": "90000000-0000-4000-8000-000000000003"}
	w := f.request("POST", base, in, f.doctor)
	requireStatus(t, w, 201)
	report := decodeID(t, w)
	var state struct {
		ModelStatus string                 `json:"model_status"`
		Reason      string                 `json:"model_reason"`
		Description string                 `json:"description"`
		Snapshot    clinicalReportSnapshot `json:"source_snapshot"`
		Conclusions []any                  `json:"conclusions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.ModelStatus != "model_unavailable" || state.Reason != "not_configured" || len(state.Snapshot.Diaries.SourceEntryIDs) < 1 || !strings.Contains(strings.Join(state.Snapshot.Diaries.SourceEntryIDs, ","), entryID) || !strings.Contains(state.Description, "SYNTHETIC_PRIVATE_HISTORY") {
		t.Fatal("report missing useful immutable sources without model")
	}
	var captured string
	if err := f.db.QueryRow(ctx, `SELECT source_snapshot::text FROM clinical_audio_reports WHERE id=$1`, report).Scan(&captured); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.request("POST", contextPath, clinicalInput(1, "90000000-0000-4000-8000-000000000004"), f.doctor), 201)
	correction := emaInput(f.today, "90000000-0000-4000-8000-000000000005", false)
	correction["supersedes_id"] = entryID
	correction["answers"].(map[string]any)["mood"] = 9
	requireStatus(t, f.request("POST", "/patient/diaries", correction, f.patient), 201)
	replay := f.request("POST", base, in, f.doctor)
	requireStatus(t, replay, 200)
	if decodeID(t, replay) != report {
		t.Fatal("report replay re-snapshotted changed context")
	}
	var after string
	if err := f.db.QueryRow(ctx, `SELECT source_snapshot::text FROM clinical_audio_reports WHERE id=$1`, report).Scan(&after); err != nil || after != captured {
		t.Fatal("source snapshot changed")
	}
	conclusion := map[string]string{"request_key": "90000000-0000-4000-8000-000000000006", "text": "Synthetic clinician conclusion; follow-up decided by physician."}
	c := f.request("POST", base+"/"+report+"/conclusions", conclusion, f.doctor)
	requireStatus(t, c, 201)
	replayed := f.request("POST", base+"/"+report+"/conclusions", conclusion, f.doctor)
	requireStatus(t, replayed, 200)
	if decodeID(t, c) != decodeID(t, replayed) {
		t.Fatal("duplicate conclusion")
	}
	conclusion["text"] = "Different"
	requireStatus(t, f.request("POST", base+"/"+report+"/conclusions", conclusion, f.doctor), 409)
	for _, p := range []principal{f.patient, f.other} {
		requireStatus(t, f.request("GET", base, nil, p), 403)
		requireStatus(t, f.request("POST", base, in, p), 403)
		requireStatus(t, f.request("GET", base+"/"+report, nil, p), 403)
		requireStatus(t, f.request("POST", base+"/"+report+"/conclusions", conclusion, p), 403)
	}
	requireStatus(t, f.request("GET", base+"?limit=1", nil, f.doctor), 200)
	var notifications int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE type LIKE 'clinical%'`).Scan(&notifications); err != nil || notifications != 0 {
		t.Fatal("private clinical report emitted patient event")
	}
}

func TestIntegrationVoiceFMUnavailableMockProvenanceAndRuntimeImmutability(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	recording := clinicalAudio(t, f)
	var unavailable, wrongMode, revokeDuring atomic.Bool
	var calls atomic.Int32
	unavailable.Store(true)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-private-token" {
			t.Error("missing private model auth")
		}
		if r.URL.Path == "/status" {
			writeJSON(w, 200, map[string]any{"status": "ready", "ready": true, "mode": "mock", "is_mock": true})
			return
		}
		if r.URL.Path != "/analyze" || r.Header.Get("X-Voice-Task") != "reading" {
			t.Error("wrong inference contract")
		}

		calls.Add(1)
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil || len(body) != 100 || strings.Contains(string(body), "SYNTHETIC_PRIVATE_HISTORY") {
			t.Error("private context leaked or audio body changed")
		}
		if revokeDuring.Load() {
			if _, err := f.db.Exec(ctx, `UPDATE care_links SET active=false WHERE patient_id=$1 AND doctor_id=$2`, f.patient.ID, f.doctor.ID); err != nil {
				t.Error(err)
			}
		}
		if wrongMode.Load() {
			writeJSON(w, 200, map[string]any{"status": "ready", "mode": "real", "is_mock": false, "clinical_use": false, "input_used_for_prediction": true})
			return
		}
		if unavailable.Load() {
			writeJSON(w, 503, map[string]string{"status": "model_unavailable", "reason_code": "not_installed"})
			return
		}
		writeJSON(w, 200, map[string]any{"status": "ready", "mode": "mock", "is_mock": true, "clinical_use": false, "input_used_for_prediction": false, "research_only": true, "limitations": []string{"Synthetic test only; not patient inference."}, "model": map[string]string{"name": "synthetic-test-fixture", "revision": "test-v1"}, "outputs": map[string]any{"scores": []map[string]any{{"label": "cat_mood", "score": 0.4}}, "interpretation": "Synthetic test demonstration; not patient inference."}})
	}))
	defer provider.Close()
	f.server.config.VoiceFMURL = provider.URL
	f.server.config.VoiceFMToken = "synthetic-private-token"
	f.server.config.VoiceFMMode = "mock"
	if err := store.ProvisionRuntimeRole(ctx, f.db, "runtime-clinical-test-password-32-bytes"); err != nil {
		t.Fatal(err)
	}
	config := f.db.Config()
	config.ConnConfig.User = "psitech_app"
	config.ConnConfig.Password = "runtime-clinical-test-password-32-bytes"
	runtime, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	f.server = New(runtime, f.server.config)
	requireStatus(t, f.request("POST", "/patients/"+f.patient.ID+"/clinical-context", clinicalInput(0, "91000000-0000-4000-8000-000000000001"), f.doctor), 201)
	base := "/patients/" + f.patient.ID + "/audio/" + recording + "/clinical-reports"
	first := f.request("POST", base, map[string]string{"request_key": "91000000-0000-4000-8000-000000000002"}, f.doctor)
	requireStatus(t, first, 201)
	firstID := decodeID(t, first)
	if err = f.server.processVoiceFM(ctx); err != nil {
		t.Fatal(err)
	}
	var status, reason string
	if err = runtime.QueryRow(ctx, `SELECT status,reason_code FROM voicefm_jobs WHERE report_id=$1`, firstID).Scan(&status, &reason); err != nil || status != "model_unavailable" || reason != "not_installed" {
		t.Fatalf("missing artifact became fake success/retry: %s %s %v", status, reason, err)
	}
	unavailable.Store(false)
	second := f.request("POST", base, map[string]string{"request_key": "91000000-0000-4000-8000-000000000003"}, f.doctor)
	requireStatus(t, second, 201)
	secondID := decodeID(t, second)
	if err = f.server.processVoiceFM(ctx); err != nil {
		t.Fatal(err)
	}
	ready := f.request("GET", base+"/"+secondID, nil, f.doctor)
	requireStatus(t, ready, 200)
	var result struct {
		Status      string                 `json:"model_status"`
		Result      map[string]any         `json:"model_result"`
		Snapshot    clinicalReportSnapshot `json:"source_snapshot"`
		Description string                 `json:"description"`
	}
	if json.Unmarshal(ready.Body.Bytes(), &result) != nil || result.Status != "ready" || result.Result["is_mock"] != true || result.Result["input_used_for_prediction"] != false || result.Snapshot.ModelRequest["is_mock"] != true || !strings.Contains(result.Description, "ДЕМОНСТРАЦИОННЫЙ") {
		t.Fatal("mock provenance hidden")
	}
	requireStatus(t, f.request("GET", "/voicefm/status", nil, f.doctor), 200)
	requireStatus(t, f.request("GET", "/voicefm/status", nil, f.patient), 403)

	newReport := func(index int) string {
		t.Helper()
		w := f.request("POST", base, map[string]string{"request_key": fmt.Sprintf("91000000-0000-4000-8000-%012d", index)}, f.doctor)
		requireStatus(t, w, 201)
		return decodeID(t, w)
	}
	assertUnavailable := func(id, code string) {
		t.Helper()
		var n int
		if err := f.db.QueryRow(ctx, `SELECT j.status,j.reason_code,(SELECT count(*) FROM voicefm_results WHERE report_id=j.report_id) FROM voicefm_jobs j WHERE report_id=$1`, id).Scan(&status, &reason, &n); err != nil || status != "model_unavailable" || reason != code || n != 0 {
			t.Fatalf("unsafe unavailable job: %s %s %d %v", status, reason, n, err)
		}
	}
	mismatch := newReport(4)
	wrongMode.Store(true)
	if err = f.server.processVoiceFM(ctx); err != nil {
		t.Fatal(err)
	}
	assertUnavailable(mismatch, "mode_mismatch")
	wrongMode.Store(false)
	revoked := newReport(5)
	before := calls.Load()
	if _, err = f.db.Exec(ctx, `UPDATE care_links SET active=false WHERE patient_id=$1 AND doctor_id=$2`, f.patient.ID, f.doctor.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.server.processVoiceFM(ctx); err != nil {
		t.Fatal(err)
	}
	assertUnavailable(revoked, "access_revoked")
	if calls.Load() != before {
		t.Fatal("queued job transmitted audio after access revocation")
	}
	requireStatus(t, f.request("GET", base, nil, f.doctor), 403)
	if _, err = f.db.Exec(ctx, `UPDATE care_links SET active=true WHERE patient_id=$1 AND doctor_id=$2`, f.patient.ID, f.doctor.ID); err != nil {
		t.Fatal(err)
	}
	during := newReport(6)
	revokeDuring.Store(true)
	if err = f.server.processVoiceFM(ctx); err != nil {
		t.Fatal(err)
	}
	assertUnavailable(during, "access_revoked")
	for _, table := range []string{"clinical_context_versions", "clinical_audio_reports", "voicefm_results", "clinical_report_conclusions"} {
		for _, verb := range []string{"UPDATE " + table + " SET id=id WHERE false", "DELETE FROM " + table + " WHERE false"} {
			if _, err = runtime.Exec(ctx, verb); err == nil {
				t.Fatalf("runtime can rewrite immutable clinical record: %s", verb)
			}
		}
	}
}
