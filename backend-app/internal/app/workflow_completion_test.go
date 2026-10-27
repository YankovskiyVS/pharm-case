package app

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"psitech/internal/store"
	"strings"
	"testing"
	"time"
)

func TestIntegrationDisabledCounterpartAndUnassignedRiskRecovery(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, err := f.db.Exec(ctx, `UPDATE users SET active=false WHERE id=$1`, f.doctor.ID); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.request("POST", "/patients/"+f.patient.ID+"/messages", map[string]string{"body": "Cannot deliver to inactive doctor", "doctor_id": f.doctor.ID, "request_key": "81000000-0000-4000-8000-000000000001"}, f.patient), 403)
	profile := f.request("GET", "/dashboard", nil, f.patient)
	requireStatus(t, profile, 200)
	var detail struct {
		Team []any `json:"care_team"`
	}
	if err := json.Unmarshal(profile.Body.Bytes(), &detail); err != nil || len(detail.Team) != 0 {
		t.Fatal("inactive doctor still shown as available")
	}
	response := f.request("POST", "/patient/risk-signals", map[string]string{"form_id": "81000000-0000-4000-8000-000000000002"}, f.patient)
	requireStatus(t, response, 201)
	alert := decodeID(t, response)
	if err := f.server.dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	var exceptions, deliveries int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM operational_exceptions WHERE aggregate_id=$1 AND status='open'`, alert).Scan(&exceptions); err != nil || exceptions != 1 {
		t.Fatalf("unrouted risk not operationally visible: %d %v", exceptions, err)
	}
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM event_deliveries d JOIN outbox_events e ON e.id=d.event_id WHERE e.aggregate_id=$1`, alert).Scan(&deliveries); err != nil || deliveries != 0 {
		t.Fatalf("unrouted event silently completed: %d %v", deliveries, err)
	}
	// Restoring/reassigning an available clinician allows persisted routing to resume.
	if _, err := f.db.Exec(ctx, `UPDATE users SET active=true WHERE id=$1`, f.doctor.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(ctx, `UPDATE operational_exceptions SET last_seen_at=now()-interval '31 seconds'`); err != nil {
		t.Fatal(err)
	}
	if err := f.server.dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM operational_exceptions WHERE aggregate_id=$1 AND status='open'`, alert).Scan(&exceptions); err != nil || exceptions != 0 {
		t.Fatalf("restored route did not clear exception: %d %v", exceptions, err)
	}
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM notifications n JOIN outbox_events e ON e.id=n.event_id WHERE e.aggregate_id=$1 AND n.recipient_id=$2`, alert, f.doctor.ID).Scan(&deliveries); err != nil || deliveries != 1 {
		t.Fatalf("risk not delivered after route restored %d %v", deliveries, err)
	}
}

func TestIntegrationCursorPagesUnreadAndFullExport(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	doctor := f.doctor.ID
	// Construct history beyond all previous hard limits without clinical content.
	_, err := f.db.Exec(ctx, `INSERT INTO messages(care_link_id,sender_id,body,request_key,created_at) SELECT c.id,c.doctor_id,'Synthetic paginated message '||n,gen_random_uuid(),now()-make_interval(secs=>n) FROM care_links c CROSS JOIN generate_series(1,112) n WHERE c.patient_id=$1 AND c.doctor_id=$2`, f.patient.ID, doctor)
	if err != nil {
		t.Fatal(err)
	}
	url := "/patients/" + f.patient.ID + "/messages?page=true&doctor_id=" + doctor + "&limit=25"
	seen := map[string]bool{}
	pages := 0
	for {
		response := f.request("GET", url, nil, f.patient)
		requireStatus(t, response, 200)
		var page struct {
			Items []struct {
				ID   string `json:"id"`
				Read bool   `json:"is_read"`
			} `json:"items"`
			Total  int     `json:"total"`
			Next   *string `json:"next_cursor"`
			Unread int     `json:"unread_count"`
		}
		if err = json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if page.Total != 113 || page.Unread != 113 {
			t.Fatalf("incorrect global totals: %d %d", page.Total, page.Unread)
		}
		for _, m := range page.Items {
			if seen[m.ID] {
				t.Fatal("cursor duplicated message")
			}
			seen[m.ID] = true
		}
		pages++
		if page.Next == nil {
			break
		}
		url = "/patients/" + f.patient.ID + "/messages?page=true&doctor_id=" + doctor + "&limit=25&cursor=" + *page.Next
	}
	if len(seen) != 113 || pages != 5 {
		t.Fatalf("history truncated: %d records %d pages", len(seen), pages)
	}
	var one string
	if err = f.db.QueryRow(ctx, `SELECT m.id FROM messages m JOIN care_links c ON c.id=m.care_link_id WHERE c.patient_id=$1 LIMIT 1`, f.patient.ID).Scan(&one); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.request("POST", "/patients/"+f.patient.ID+"/messages/read", map[string]any{"doctor_id": doctor, "ids": []string{one}}, f.patient), 200)
	response := f.request("GET", "/patients/"+f.patient.ID+"/messages?page=true&doctor_id="+doctor, nil, f.patient)
	var unread struct {
		Unread int `json:"unread_count"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &unread); err != nil || unread.Unread != 112 {
		t.Fatalf("marking visible messages read affected hidden messages: %+v %v", unread, err)
	}
	// More than 100 open alerts remain reachable with a true queue total.
	if _, err = f.db.Exec(ctx, `WITH signals AS (INSERT INTO risk_signals(patient_id,form_id) SELECT $1,gen_random_uuid() FROM generate_series(1,105) RETURNING id) INSERT INTO alerts(patient_id,signal_id) SELECT $1,id FROM signals`, f.patient.ID); err != nil {
		t.Fatal(err)
	}
	response = f.request("GET", "/doctor/alerts?limit=25", nil, f.doctor)
	requireStatus(t, response, 200)
	var alertPage struct {
		Total int     `json:"total"`
		Items []any   `json:"items"`
		Next  *string `json:"next_cursor"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &alertPage); err != nil || alertPage.Total != 105 || len(alertPage.Items) != 25 || alertPage.Next == nil {
		t.Fatalf("open queue total/page wrong %+v %v", alertPage, err)
	}
	exported := f.request("GET", "/patients/"+f.patient.ID+"/export?format=json", nil, f.doctor)
	requireStatus(t, exported, 200)
	var report struct {
		Messages []any `json:"messages"`
		Alerts   []any `json:"alerts"`
	}
	if err = json.Unmarshal(exported.Body.Bytes(), &report); err != nil || len(report.Messages) != 113 || len(report.Alerts) != 105 {
		t.Fatalf("full export truncated: messages %d alerts %d err %v", len(report.Messages), len(report.Alerts), err)
	}
	requireStatus(t, f.request("GET", "/patients/"+f.patient.ID+"/export?format=csv", nil, f.patient), 200)
	requireStatus(t, f.request("GET", "/patients/"+f.patient.ID+"/diaries?limit=1&cursor=bad", nil, f.doctor), 400)
}

func TestIntegrationNotificationReadIsExplicitAndGlobal(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	_, err := f.db.Exec(ctx, `WITH events AS (INSERT INTO outbox_events(type,patient_id,aggregate_id) SELECT 'diary.submitted',$1,gen_random_uuid() FROM generate_series(1,65) RETURNING id) INSERT INTO notifications(recipient_id,patient_id,event_id,type) SELECT $2,$1,id,'diary.submitted' FROM events`, f.patient.ID, f.doctor.ID)
	if err != nil {
		t.Fatal(err)
	}
	response := f.request("GET", "/notifications?page=true&limit=10", nil, f.doctor)
	requireStatus(t, response, 200)
	var page struct {
		Items []struct {
			ID     string `json:"id"`
			Target string `json:"target_id"`
			Type   string `json:"target_type"`
		} `json:"items"`
		Unread int `json:"unread_count"`
		Total  int `json:"total"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &page); err != nil || page.Total != 65 || page.Unread != 65 || len(page.Items) != 10 || page.Items[0].Target == "" {
		t.Fatalf("notification total/targets wrong: %+v %v", page, err)
	}
	requireStatus(t, f.request("POST", "/notifications/read", map[string]any{"ids": []string{page.Items[0].ID}}, f.doctor), 200)
	response = f.request("GET", "/notifications?page=true", nil, f.doctor)
	if err = json.Unmarshal(response.Body.Bytes(), &page); err != nil || page.Unread != 64 {
		t.Fatalf("reading one notice marked hidden rows: %d %v", page.Unread, err)
	}
	requireStatus(t, f.request("POST", "/notifications/read", map[string]bool{"all": true}, f.doctor), 200)
	response = f.request("GET", "/notifications?page=true", nil, f.doctor)
	if err = json.Unmarshal(response.Body.Bytes(), &page); err != nil || page.Unread != 0 {
		t.Fatalf("read-all failed: %d %v", page.Unread, err)
	}
}

func TestIntegrationScheduleHistoryPauseRecurrenceAndReviewIdempotency(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	day, err := time.Parse("2006-01-02", f.today)
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"kind": "ema", "start_date": day.AddDate(0, 0, 20).Format("2006-01-02"), "end_date": day.AddDate(0, 0, 30).Format("2006-01-02"), "due_time": "20:00", "interval_days": 2, "request_key": "82000000-0000-4000-8000-000000000001"}
	response := f.request("POST", "/patients/"+f.patient.ID+"/schedules", input, f.doctor)
	requireStatus(t, response, 201)
	schedule := decodeID(t, response)
	requireStatus(t, f.request("POST", "/patients/"+f.patient.ID+"/schedules", input, f.doctor), 200)
	requireStatus(t, f.request("POST", "/patients/"+f.patient.ID+"/schedules/"+schedule+"/status", map[string]string{"status": "paused", "request_key": "82000000-0000-4000-8000-000000000002"}, f.doctor), 200)
	history := f.request("GET", "/patients/"+f.patient.ID+"/schedules/"+schedule+"/history", nil, f.patient)
	requireStatus(t, history, 200)
	var hp struct {
		Total int `json:"total"`
	}
	if err = json.Unmarshal(history.Body.Bytes(), &hp); err != nil || hp.Total != 2 {
		t.Fatalf("schedule history lost: %d %v", hp.Total, err)
	}
	snapshot := reviewRegressionSnapshot(t, f)
	review := map[string]string{"action": "reviewed", "note": "Synthetic idempotent review", "snapshot_token": snapshot, "request_key": "82000000-0000-4000-8000-000000000003"}
	response = f.request("POST", "/patients/"+f.patient.ID+"/reviews", review, f.doctor)
	requireStatus(t, response, 201)
	requireStatus(t, f.request("POST", "/patients/"+f.patient.ID+"/reviews", review, f.doctor), 200)
	review["note"] = "Different"
	requireStatus(t, f.request("POST", "/patients/"+f.patient.ID+"/reviews", review, f.doctor), 409)
	var count int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM reviews WHERE patient_id=$1`, f.patient.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("retry duplicated review: %d %v", count, err)
	}
	// Weekly reminders are sourced from the documented seven-day review obligation.
	if _, err = f.db.Exec(ctx, `UPDATE care_links SET created_at=now()-interval '9 days'`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE reviews SET created_at=now()-interval '8 days'`); err != nil {
		t.Fatal(err)
	}
	if err = f.server.reminders(ctx); err != nil {
		t.Fatal(err)
	}
	if err = f.server.reminders(ctx); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM weekly_review_reminders WHERE patient_id=$1 AND doctor_id=$2`, f.patient.ID, f.doctor.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("weekly review reminder missing/duplicated: %d %v", count, err)
	}
}

func TestIntegrationRuntimeRoleCannotRewriteClinicalHistory(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	password := "runtime-test-password-with-32-bytes"
	if err := store.ProvisionRuntimeRole(ctx, f.db, password); err != nil {
		t.Fatal(err)
	}
	config := f.db.Config()
	config.ConnConfig.User = "psitech_app"
	config.ConnConfig.Password = password
	db, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	runtime := New(db, f.server.config)
	f.server = runtime
	requireStatus(t, f.request("GET", "/dashboard", nil, f.patient), 200)
	requireStatus(t, f.request("POST", "/patient/diaries", emaInput(f.today, "83000000-0000-4000-8000-000000000001", true), f.patient), 201)
	if err = runtime.dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`CREATE TABLE should_not_exist(id int)`, `UPDATE diary_entries SET answers='{}' WHERE false`, `UPDATE current_entries SET answers='{}' WHERE false`, `DELETE FROM audit_events WHERE false`, `SELECT * FROM schema_migrations`} {
		if _, err = db.Exec(ctx, query); err == nil {
			t.Fatalf("runtime unexpectedly allowed privileged statement: %s", query)
		}
	}
}

func TestOpsStatusRejectsMissingCredential(t *testing.T) {
	server := New(nil, Config{OpsToken: strings.Repeat("a", 64)})
	request := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/ops/status", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatalf("operator status publicly exposed: %d", response.Code)
	}
}

func TestIntegrationPasswordResetEncryptedOneTimeAndRevokesSessions(t *testing.T) {
	f := setup(t)
	capture := captureGmail(t)
	configureTestEmail(f, capture)
	ctx := context.Background()
	requireStatus(t, f.request("POST", "/auth/password/reset/request", map[string]string{"email": f.patient.Email}, principal{}), 202)
	if err := f.server.processEmail(ctx); err != nil {
		t.Fatal(err)
	}
	_, body := captureMessage(t, capture)
	marker := "#reset="
	at := strings.Index(body, marker)
	if at < 0 {
		t.Fatal("reset link missing")
	}
	raw := strings.Fields(body[at+len(marker):])[0]
	var count int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM password_resets WHERE token_hash=$1 AND used_at IS NULL`, hash(raw)).Scan(&count); err != nil || count != 1 {
		t.Fatalf("durable token missing %d %v", count, err)
	}
	requireStatus(t, f.request("POST", "/auth/password/reset/confirm", map[string]string{"token": raw, "new_password": "changed-local-password-123"}, principal{}), 200)
	requireStatus(t, f.request("GET", "/me", nil, f.patient), 401)
	requireStatus(t, f.request("POST", "/auth/password/reset/confirm", map[string]string{"token": raw, "new_password": "changed-again-password-123"}, principal{}), 400)
	requireStatus(t, f.request("POST", "/auth/login", map[string]string{"email": f.patient.Email, "password": "integration-password-123", "portal": "patient"}, principal{}), 401)
	requireStatus(t, f.request("POST", "/auth/login", map[string]string{"email": f.patient.Email, "password": "changed-local-password-123", "portal": "patient"}, principal{}), 200)
	// A non-existent identity receives the same accepted response and no reset row.
	requireStatus(t, f.request("POST", "/auth/password/reset/request", map[string]string{"email": "unknown@psitech.local"}, principal{}), 202)
}

func TestIntegrationAudioRetryKeepsSuccessfulResultAndNewRunNotification(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			writeJSON(w, 200, map[string]any{"status": "ready", "models": map[string]any{"asr": map[string]string{"state": "ready", "model": "test"}}})
			return
		}
		if r.URL.Path == "/search" {
			writeJSON(w, 200, map[string]any{"status": "ready", "answer": "Synthetic citation", "sources": []any{}})
			return
		}
		writeJSON(w, 200, map[string]any{"pipeline": "test-v1", "usable": true, "metrics": map[string]float64{"f0_median_hz": 180}, "segments": []map[string]any{{"start": 0.5, "end": 2.5, "text": "Synthetic test transcript"}}})
	}))
	defer model.Close()
	f.server.config.AudioDir = t.TempDir()
	f.server.config.AudioURL = model.URL
	f.server.config.AudioToken = "test"
	response := f.upload(t, "84000000-0000-4000-8000-000000000001", "reading", []byte(strings.Repeat("x", 100)), f.patient, true)
	requireStatus(t, response, 201)
	recording := decodeID(t, response)
	if err := f.server.processAudio(ctx); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.request("POST", "/patients/"+f.patient.ID+"/audio/"+recording+"/retry", map[string]bool{}, f.patient), 202)
	response = f.request("GET", "/patients/"+f.patient.ID+"/audio/"+recording, nil, f.doctor)
	requireStatus(t, response, 200)
	var state struct {
		Status    string          `json:"status"`
		Result    json.RawMessage `json:"result"`
		ResultRun *string         `json:"result_run_id"`
		Runs      []any           `json:"run_history"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil || state.Status != "queued" || state.ResultRun == nil || string(state.Result) == "null" || len(state.Runs) != 2 {
		t.Fatalf("retry hid prior result: %+v %v", state, err)
	}
	if err := f.server.processAudio(ctx); err != nil {
		t.Fatal(err)
	}
	var notices int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE type='audio.ready' AND patient_id=$1`, f.patient.ID).Scan(&notices); err != nil || notices != 2 {
		t.Fatalf("second successful run rolled back or notification lost: %d %v", notices, err)
	}
	requireStatus(t, f.request("GET", "/patients/"+f.patient.ID+"/audio/"+recording+"/runs?limit=1", nil, f.doctor), 200)
	requireStatus(t, f.request("GET", "/audio/status", nil, f.patient), 200)
}

func TestIntegrationOperatorQueueStatusIsScopedAndUseful(t *testing.T) {
	f := setup(t)
	f.server.config.OpsToken = strings.Repeat("o", 64)
	req := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/ops/status", nil)
	req.Header.Set("Authorization", "Bearer "+f.server.config.OpsToken)
	out := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(out, req)
	requireStatus(t, out, 200)
	var state struct {
		Queues map[string]json.RawMessage `json:"queues"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &state); err != nil || state.Queues["email_jobs"] == nil || state.Queues["unassigned_risks"] == nil {
		t.Fatalf("operator snapshot missing actionable queue information: %v", err)
	}
	if strings.Contains(out.Body.String(), f.patient.Email) || strings.Contains(out.Body.String(), f.patient.Name) {
		t.Fatal("aggregate operator status exposed clinical identity")
	}
}

func TestIntegrationAccountSessionsChangeAndProfile(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	requireStatus(t, f.request("POST", "/me/profile", map[string]string{"timezone": "invalid-timezone"}, f.patient), 400)
	requireStatus(t, f.request("POST", "/me/profile", map[string]string{"name": "Synthetic Updated Patient", "timezone": "Europe/Moscow"}, f.patient), 200)
	var raw string
	if err := f.db.QueryRow(ctx, `INSERT INTO sessions(token_hash,user_id,csrf,expires_at) VALUES($1,$2,'test',now()+interval '1 hour') RETURNING id`, hash("another-synthetic-session"), f.patient.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	sessions := f.request("GET", "/auth/sessions", nil, f.patient)
	requireStatus(t, sessions, 200)
	var list struct {
		Items []any `json:"items"`
	}
	if err := json.Unmarshal(sessions.Body.Bytes(), &list); err != nil || len(list.Items) != 2 {
		t.Fatalf("session list wrong: %d %v", len(list.Items), err)
	}
	requireStatus(t, f.request("POST", "/auth/sessions/"+raw+"/revoke", map[string]bool{}, f.other), 404)
	requireStatus(t, f.request("POST", "/auth/sessions/revoke-others", map[string]bool{}, f.patient), 200)
	requireStatus(t, f.request("POST", "/auth/password/change", map[string]string{"current_password": "wrong", "new_password": "new-local-password-123"}, f.patient), 400)
	requireStatus(t, f.request("GET", "/me", nil, f.patient), 200)
	requireStatus(t, f.request("POST", "/auth/password/change", map[string]string{"current_password": "integration-password-123", "new_password": "new-local-password-123"}, f.patient), 200)
	requireStatus(t, f.request("GET", "/me", nil, f.patient), 401)
	// SQL credential updates automatically invalidate old doctor sessions too.
	if _, err := f.db.Exec(ctx, `UPDATE users SET totp_secret='JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP' WHERE id=$1`, f.doctor.ID); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.request("GET", "/me", nil, f.doctor), 401)
}
