package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func validVoiceFMFixture() map[string]any {
	return map[string]any{"status": "ready", "mode": "mock", "is_mock": true, "clinical_use": false, "input_used_for_prediction": false, "research_only": true, "limitations": []string{"Synthetic demonstration only"}, "model": map[string]string{"name": "synthetic-test-fixture", "revision": "test-v1"}, "outputs": map[string]any{"scores": []any{map[string]any{"label": "cat_mood", "score": 0.4}}, "interpretation": "Synthetic demonstration; not patient inference"}}
}

func TestIntegrationVoiceFMConfiguredModeChangeDoesNotReadOrTransmitAudio(t *testing.T) {
	for _, original := range []string{"mock", "real"} {
		t.Run(original, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			recording := clinicalAudio(t, f)
			var calls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				t.Error("mode-changed report reached the adapter")
			}))
			defer provider.Close()
			f.server.config.VoiceFMURL = provider.URL
			f.server.config.VoiceFMMode = original
			f.server.config.VoiceFMToken = "synthetic-token"
			response := f.request("POST", "/patients/"+f.patient.ID+"/audio/"+recording+"/clinical-reports", map[string]string{"request_key": "93000000-0000-4000-8000-000000000001"}, f.doctor)
			requireStatus(t, response, 201)
			report := decodeID(t, response)
			if original == "mock" {
				f.server.config.VoiceFMMode = "real"
			} else {
				f.server.config.VoiceFMMode = "mock"
			}
			// Missing source files must not override the earlier mode-mismatch guard.
			f.server.config.AudioDir = t.TempDir()
			if err := f.server.processVoiceFM(ctx); err != nil {
				t.Fatal(err)
			}
			var state, reason string
			var results int
			if err := f.db.QueryRow(ctx, `SELECT j.status,j.reason_code,(SELECT count(*) FROM voicefm_results WHERE report_id=j.report_id) FROM voicefm_jobs j WHERE report_id=$1`, report).Scan(&state, &reason, &results); err != nil || state != "model_unavailable" || reason != "mode_mismatch" || results != 0 || calls.Load() != 0 {
				t.Fatalf("mode-changed report was processed: %s %s results=%d calls=%d err=%v", state, reason, results, calls.Load(), err)
			}
		})
	}
}
func TestVoiceFMRejectsImpossibleOrUnlabeledClinicalOutputs(t *testing.T) {
	data, _ := json.Marshal(validVoiceFMFixture())
	if !validateVoiceFMResult(data, "mock") {
		t.Fatal("explicit mock fixture rejected")
	}
	mutations := []func(map[string]any){
		func(v map[string]any) { v["clinical_use"] = true }, func(v map[string]any) { v["input_used_for_prediction"] = true }, func(v map[string]any) { delete(v, "research_only") }, func(v map[string]any) { v["limitations"] = []string{} }, func(v map[string]any) { v["outputs"].(map[string]any)["interpretation"] = "" },
		func(v map[string]any) {
			v["outputs"].(map[string]any)["scores"] = []any{map[string]any{"label": "cat_mood", "score": 1.1}}
		},
		func(v map[string]any) {
			v["outputs"].(map[string]any)["scores"] = []any{map[string]any{"label": "cat_mood", "score": -0.1}}
		},
		func(v map[string]any) {
			v["outputs"].(map[string]any)["scores"] = []any{map[string]any{"label": "invented_diagnosis", "score": 0.4}}
		},
		func(v map[string]any) {
			v["outputs"].(map[string]any)["scores"] = []any{map[string]any{"label": "cat_mood", "score": 0.4}, map[string]any{"label": "cat_mood", "score": 0.3}}
		},
	}
	for i, mutate := range mutations {
		v := validVoiceFMFixture()
		mutate(v)
		raw, _ := json.Marshal(v)
		if validateVoiceFMResult(raw, "mock") {
			t.Fatalf("unsafe result accepted: case%d", i)
		}
	}
}

func TestIntegrationVoiceFMTransientTimeoutBoundedAndStatusModeMismatch(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	recording := clinicalAudio(t, f)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			writeJSON(w, 200, map[string]any{"status": "ready", "ready": true, "mode": "real", "is_mock": false})
			return
		}
		writeJSON(w, 503, map[string]string{"status": "model_unavailable", "reason_code": "timeout"})
	}))
	defer provider.Close()
	f.server.config.VoiceFMURL = provider.URL
	f.server.config.VoiceFMToken = "synthetic-token"
	f.server.config.VoiceFMMode = "mock"
	status := f.request("GET", "/voicefm/status", nil, f.doctor)
	requireStatus(t, status, 200)
	var modelStatus struct {
		Ready  bool   `json:"ready"`
		Reason string `json:"reason_code"`
	}
	if json.Unmarshal(status.Body.Bytes(), &modelStatus) != nil || modelStatus.Ready || modelStatus.Reason != "mode_mismatch" {
		t.Fatal("misleading model status for mismatched mode")
	}
	w := f.request("POST", "/patients/"+f.patient.ID+"/audio/"+recording+"/clinical-reports", map[string]string{"request_key": "92000000-0000-4000-8000-000000000001"}, f.doctor)
	requireStatus(t, w, 201)
	report := decodeID(t, w)
	for attempt := 1; attempt <= 3; attempt++ {
		if _, err := f.db.Exec(ctx, `UPDATE voicefm_jobs SET available_at=now()-interval '1 second' WHERE report_id=$1`, report); err != nil {
			t.Fatal(err)
		}
		if err := f.server.processVoiceFM(ctx); err != nil {
			t.Fatal(err)
		}
		var state, reason string
		var attempts int
		if err := f.db.QueryRow(ctx, `SELECT status,reason_code,attempts FROM voicefm_jobs WHERE report_id=$1`, report).Scan(&state, &reason, &attempts); err != nil || attempts != attempt || reason != "timeout" {
			t.Fatalf("wrong transient attempt %d %s %s %v", attempts, state, reason, err)
		}
		if attempt < 3 && state != "queued" {
			t.Fatal("transient inference timeout treated as permanent missing model")
		}
		if attempt == 3 && state != "failed" {
			t.Fatal("unbounded model retry")
		}
		if err := f.server.processVoiceFM(ctx); err != nil {
			t.Fatal(err)
		}
		var after int
		if err := f.db.QueryRow(ctx, `SELECT attempts FROM voicefm_jobs WHERE report_id=$1`, report).Scan(&after); err != nil || after != attempt {
			t.Fatal("model retry ignored backoff or terminal state")
		}
	}
}
