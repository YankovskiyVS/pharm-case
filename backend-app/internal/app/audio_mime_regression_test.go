package app

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestIntegrationM4AMIMAliasesKeepUploadValidation(t *testing.T) {
	f := setup(t)
	f.server.config.AudioDir = t.TempDir()
	f.server.config.AudioURL = "http://audio-service:8000"
	for index, contentType := range []string{"audio/x-m4a", "audio/m4a", `audio/x-m4a; codecs="mp4a.40.2"`, "application/octet-stream", "audio/x-unknown"} {
		t.Run(contentType, func(t *testing.T) {
			query := url.Values{"key": {fmt.Sprintf("85000000-0000-4000-8000-%012d", index)}, "task": {"reading"}, "device": {"synthetic microphone"}}
			r := httptest.NewRequest(http.MethodPost, "http://localhost:8080/api/v1/patient/audio?"+query.Encode(), bytes.NewBufferString(strings.Repeat("x", 100)))
			r.Header.Set("Origin", "http://localhost:8080")
			r.Header.Set("Content-Type", contentType)
			r.Header.Set("X-Audio-Consent", "audio-v1")
			r.Header.Set("X-CSRF-Token", f.patient.CSRF)
			r.AddCookie(&http.Cookie{Name: "psitech_session", Value: f.patient.Session})
			w := httptest.NewRecorder()
			f.server.Handler().ServeHTTP(w, r)
			if index >= 3 {
				requireStatus(t, w, http.StatusUnsupportedMediaType)
				return
			}
			requireStatus(t, w, http.StatusCreated)
			var stored string
			if err := f.db.QueryRow(context.Background(), `SELECT mime FROM audio_recordings WHERE id=$1`, decodeID(t, w)).Scan(&stored); err != nil || stored != "audio/mp4" {
				t.Fatalf("M4A alias was not canonicalized: %s %v", stored, err)
			}
		})
	}
}
