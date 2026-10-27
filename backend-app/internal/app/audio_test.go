package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func (f *fixture) upload(t *testing.T, key, task string, data []byte, p principal, csrf bool) *httptest.ResponseRecorder {
	t.Helper()
	query := url.Values{"key": {key}, "task": {task}, "device": {"synthetic microphone"}}
	r := httptest.NewRequest("POST", "http://localhost:8080/api/v1/patient/audio?"+query.Encode(), bytes.NewReader(data))
	r.Header.Set("Origin", "http://localhost:8080")
	r.Header.Set("Content-Type", "audio/wav")
	r.Header.Set("X-Audio-Consent", "audio-v1")
	if csrf {
		r.Header.Set("X-CSRF-Token", p.CSRF)
	}
	r.AddCookie(&http.Cookie{Name: "psitech_session", Value: p.Session})
	w := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(w, r)
	return w
}

func TestIntegrationAudioLifecycle(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-audio-token" {
			t.Error("missing worker authentication")
		}
		if r.URL.Path == "/search" {
			var input struct {
				Chunks []struct {
					Recording string `json:"recording_id"`
				} `json:"chunks"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if len(input.Chunks) != 1 {
				t.Errorf("unexpected chunk count %d", len(input.Chunks))
			}
			writeJSON(w, 200, map[string]any{"status": "ready", "answer": "Synthetic citation", "sources": []any{}})
			return
		}
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
		}
		writeJSON(w, 200, map[string]any{"pipeline": "test-v1", "usable": true, "metrics": map[string]any{"f0_median_hz": 180.0}, "segments": []map[string]any{{"start": 0.5, "end": 3.0, "text": "Synthetic sleep statement"}}})
	}))
	defer model.Close()
	f.server.config.AudioDir = t.TempDir()
	f.server.config.AudioURL = model.URL
	f.server.config.AudioToken = "test-audio-token"
	payload := bytes.Repeat([]byte{42}, 100)
	key := "20000000-0000-4000-8000-000000000001"
	requireStatus(t, f.upload(t, key, "reading", payload, f.patient, false), 403)
	requireStatus(t, f.upload(t, key, "reading", payload, f.doctor, true), 403)
	created := f.upload(t, key, "reading", payload, f.patient, true)
	requireStatus(t, created, 201)
	id := decodeID(t, created)
	replay := f.upload(t, key, "reading", payload, f.patient, true)
	requireStatus(t, replay, 200)
	if decodeID(t, replay) != id {
		t.Fatal("idempotency changed recording")
	}
	requireStatus(t, f.upload(t, key, "free", payload, f.patient, true), 409)
	requireStatus(t, f.upload(t, key, "reading", bytes.Repeat([]byte{1}, 100), f.patient, true), 409)
	requireStatus(t, f.request("GET", "/patients/"+f.patient.ID+"/audio", nil, f.other), 403)
	requireStatus(t, f.request("GET", "/patients/"+f.patient.ID+"/audio/"+id+"/file", nil, f.doctor), 200)
	files, err := os.ReadDir(f.server.config.AudioDir)
	if err != nil || len(files) != 1 {
		t.Fatalf("duplicate blobs: %v %d", err, len(files))
	}
	// A fresh Server must recover the queued recording without upload process state.
	restarted := New(f.db, f.server.config)
	if err = restarted.processAudio(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	var chunks int
	if err = f.db.QueryRow(ctx, `SELECT status FROM audio_runs WHERE recording_id=$1`, id).Scan(&status); err != nil || status != "ready" {
		t.Fatalf("status %s %v", status, err)
	}
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM audio_chunks c JOIN audio_runs j ON j.id=c.run_id WHERE j.recording_id=$1`, id).Scan(&chunks); err != nil || chunks != 1 {
		t.Fatalf("chunks %d %v", chunks, err)
	}
	if err = restarted.processAudio(ctx); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.request("POST", "/patients/"+f.patient.ID+"/audio/search", map[string]string{"query": "sleep"}, f.doctor), 200)
	requireStatus(t, f.request("POST", "/patients/"+f.patient.ID+"/audio/search", map[string]string{"query": "sleep"}, f.patient), 403)
	requireStatus(t, f.request("POST", "/patients/"+f.patient.ID+"/audio/"+id+"/retry", map[string]string{}, f.doctor), 202)
	requireStatus(t, f.request("POST", "/patients/"+f.patient.ID+"/audio/"+id+"/retry", map[string]string{}, f.doctor), 409)
	if _, err = f.db.Exec(ctx, `UPDATE care_links SET active=false WHERE doctor_id=$1 AND patient_id=$2`, f.doctor.ID, f.patient.ID); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.request("GET", "/patients/"+f.patient.ID+"/audio/"+id+"/file", nil, f.doctor), 403)
	requireStatus(t, f.request("POST", "/patients/"+f.patient.ID+"/audio/search", map[string]string{"query": "sleep"}, f.doctor), 403)
}

func TestIntegrationAudioLeaseFencing(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	started := make(chan struct{})
	release := make(chan struct{})
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		writeJSON(w, 200, map[string]any{"pipeline": "test-v1", "usable": true, "metrics": map[string]float64{}, "segments": []any{}})
	}))
	defer model.Close()
	f.server.config.AudioDir = t.TempDir()
	f.server.config.AudioURL = model.URL
	created := f.upload(t, "20000000-0000-4000-8000-000000000002", "reading", bytes.Repeat([]byte{1}, 100), f.patient, true)
	requireStatus(t, created, 201)
	id := decodeID(t, created)
	done := make(chan error, 1)
	go func() { done <- f.server.processAudio(ctx) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("worker did not claim")
	}
	if _, err := f.db.Exec(ctx, `UPDATE audio_runs SET lease_token='new-owner',lease_until=now()+interval '5 minutes' WHERE recording_id=$1`, id); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var status string
	if err := f.db.QueryRow(ctx, `SELECT status FROM audio_runs WHERE recording_id=$1`, id).Scan(&status); err != nil || status != "processing" {
		t.Fatalf("stale worker committed: %s %v", status, err)
	}
	// Recovery also terminates an expired third attempt rather than leaving processing forever.
	if _, err := f.db.Exec(ctx, `UPDATE audio_runs SET attempts=3,lease_until=now()-interval '1 second' WHERE recording_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := f.server.processAudio(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(ctx, `SELECT status FROM audio_runs WHERE recording_id=$1`, id).Scan(&status); err != nil || status != "failed" {
		t.Fatalf("expired final lease %s %v", status, err)
	}
}

func TestAudioResultNullMetrics(t *testing.T) {
	var result audioResult
	if err := json.Unmarshal([]byte(`{"pipeline":"v1","usable":false,"metrics":{"f0_median_hz":null}}`), &result); err != nil {
		t.Fatal(err)
	}
	if result.Metrics["f0_median_hz"] != nil {
		t.Fatal("missing metric became zero")
	}
}

func TestAudioBinaryBodyRequiresExactUploadRoute(t *testing.T) {
	server := New(nil, Config{Origins: []string{"http://localhost:8080"}})
	for _, path := range []string{"/api/v1/patient/diaries", "/api/v1/auth/login"} {
		t.Run(path, func(t *testing.T) {
			r := httptest.NewRequest("POST", path, strings.NewReader("audio"))
			r.Header.Set("Origin", "http://localhost:8080")
			r.Header.Set("Content-Type", "audio/wav")
			w := httptest.NewRecorder()
			server.Handler().ServeHTTP(w, r)
			requireStatus(t, w, 415)
		})
	}
}
