package app

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func reviewRegressionSecondDoctor(t *testing.T, f *fixture) principal {
	t.Helper()
	p := principal{User: User{Email: "second-doctor@psitech.local", Name: "Synthetic Second Doctor", Role: "doctor", Timezone: "Europe/Moscow"}, Session: token(), CSRF: token()}
	err := f.db.QueryRow(context.Background(), `INSERT INTO users(email,name,role,password_hash,totp_secret) SELECT $1,$2,'doctor',password_hash,totp_secret FROM users WHERE id=$3 RETURNING id`, p.Email, p.Name, f.doctor.ID).Scan(&p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(context.Background(), `INSERT INTO care_links(doctor_id,patient_id) VALUES($1,$2)`, p.ID, f.patient.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(context.Background(), `INSERT INTO sessions(token_hash,user_id,csrf,expires_at) VALUES($1,$2,$3,now()+interval '1 hour')`, hash(p.Session), p.ID, p.CSRF); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReviewRegressionMessageNotificationsRespectConversation(t *testing.T) {
	f := setup(t)
	second := reviewRegressionSecondDoctor(t, f)
	ctx := context.Background()
	sent := f.request("POST", "/patients/"+f.patient.ID+"/messages", map[string]string{"body": "Only the selected doctor should be notified", "doctor_id": f.doctor.ID, "request_key": "71000000-0000-4000-8000-000000000001"}, f.patient)
	requireStatus(t, sent, 201)
	firstMessage := decodeID(t, sent)
	if err := f.server.dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		recipient string
		want      int
	}{{f.doctor.ID, 1}, {second.ID, 0}, {f.patient.ID, 0}} {
		var count int
		err := f.db.QueryRow(ctx, `SELECT count(*) FROM notifications n JOIN outbox_events e ON e.id=n.event_id WHERE e.aggregate_id=$1 AND n.recipient_id=$2`, firstMessage, tt.recipient).Scan(&count)
		if err != nil || count != tt.want {
			t.Fatalf("message notification recipient %s: count=%d want=%d err=%v", tt.recipient, count, tt.want, err)
		}
	}
	sent = f.request("POST", "/patients/"+f.patient.ID+"/messages", map[string]string{"body": "A separate doctor conversation", "doctor_id": second.ID, "request_key": "71000000-0000-4000-8000-000000000002"}, second)
	requireStatus(t, sent, 201)
	secondMessage := decodeID(t, sent)
	if err := f.server.dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		recipient string
		want      int
	}{{f.doctor.ID, 0}, {second.ID, 0}, {f.patient.ID, 1}} {
		var count int
		err := f.db.QueryRow(ctx, `SELECT count(*) FROM notifications n JOIN outbox_events e ON e.id=n.event_id WHERE e.aggregate_id=$1 AND n.recipient_id=$2`, secondMessage, tt.recipient).Scan(&count)
		if err != nil || count != tt.want {
			t.Fatalf("reply notification recipient %s: count=%d want=%d err=%v", tt.recipient, count, tt.want, err)
		}
	}
	// A patient must choose a conversation once multiple doctors are linked.
	requireStatus(t, f.request("GET", "/patients/"+f.patient.ID+"/messages", nil, f.patient), 400)
	for _, tt := range []struct{ doctorID, present, absent string }{{f.doctor.ID, firstMessage, secondMessage}, {second.ID, secondMessage, firstMessage}} {
		response := f.request("GET", "/patients/"+f.patient.ID+"/messages?doctor_id="+tt.doctorID, nil, f.patient)
		requireStatus(t, response, 200)
		var messages []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &messages); err != nil {
			t.Fatal(err)
		}
		present := false
		for _, message := range messages {
			if message.ID == tt.absent {
				t.Fatal("another conversation leaked into selected thread")
			}
			if message.ID == tt.present {
				present = true
			}
		}
		if !present {
			t.Fatal("selected conversation message missing")
		}
	}
	requireStatus(t, f.request("GET", "/patients/"+f.patient.ID+"/messages?doctor_id=00000000-0000-4000-8000-000000000099", nil, f.patient), 403)
}

func TestReviewRegressionHistoricalCorrectionAfterReschedule(t *testing.T) {
	f := setup(t)
	first := f.request("POST", "/patient/diaries", emaInput(f.today, "72000000-0000-4000-8000-000000000001", false), f.patient)
	requireStatus(t, first, 201)
	original := decodeID(t, first)
	day, err := time.Parse("2006-01-02", f.today)
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.request("POST", "/patients/"+f.patient.ID+"/assignments", map[string]string{"kind": "ema", "start_date": day.AddDate(0, 0, 1).Format("2006-01-02"), "end_date": day.AddDate(0, 0, 3).Format("2006-01-02"), "due_time": "20:00"}, f.doctor), 200)
	correction := emaInput(f.today, "72000000-0000-4000-8000-000000000002", false)
	correction["supersedes_id"] = original
	correction["answers"].(map[string]any)["mood"] = 2
	response := f.request("POST", "/patient/diaries", correction, f.patient)
	requireStatus(t, response, 201)
	var count int
	if err = f.db.QueryRow(context.Background(), `SELECT count(*) FROM diary_entries WHERE patient_id=$1 AND local_date=$2 AND kind='ema'`, f.patient.ID, f.today).Scan(&count); err != nil || count != 2 {
		t.Fatalf("expected original + correction, count=%d err=%v", count, err)
	}
}

func TestReviewRegressionInvitationRequiresActiveDoctor(t *testing.T) {
	for _, change := range []string{"inactive", "non-doctor"} {
		t.Run(change, func(t *testing.T) {
			f := setup(t)
			response := f.request("POST", "/doctor/invitations", map[string]string{"email": "pending@psitech.local", "name": "Synthetic Pending Patient"}, f.doctor)
			requireStatus(t, response, 201)
			var invite struct {
				Token string `json:"token"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &invite); err != nil {
				t.Fatal(err)
			}
			query := `UPDATE users SET active=false WHERE id=$1`
			if change == "non-doctor" {
				query = `UPDATE users SET role='patient' WHERE id=$1`
			}
			if _, err := f.db.Exec(context.Background(), query, f.doctor.ID); err != nil {
				t.Fatal(err)
			}
			requireStatus(t, f.request("POST", "/auth/accept-invitation", map[string]any{"token": invite.Token, "password": "new-patient-password-123", "consent": true}, principal{}), 400)
			var count int
			if err := f.db.QueryRow(context.Background(), `SELECT count(*) FROM users WHERE email='pending@psitech.local'`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("registration from unavailable issuer created account: %d %v", count, err)
			}
		})
	}
}

func TestReviewRegressionLoginCountsFailuresNotSuccessfulUse(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	login := map[string]string{"email": f.patient.Email, "password": "integration-password-123", "portal": "patient", "code": ""}
	for i := 0; i < 3; i++ {
		requireStatus(t, f.request("POST", "/auth/login", login, principal{}), 200)
	}
	var count int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM login_attempts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("successful logins consumed failure budget: %d %v", count, err)
	}
	mock := httptest.NewRequest("POST", "http://localhost:8080/api/v1/auth/login", nil)
	keys := f.server.attemptKeys(mock, f.patient.Email, "login")
	if _, err := f.db.Exec(ctx, `INSERT INTO login_attempts(key,attempts,expires_at) SELECT unnest($1::text[]),29,now()+interval '10 minutes'`, keys); err != nil {
		t.Fatal(err)
	}
	login["password"] = "incorrect-password"
	requireStatus(t, f.request("POST", "/auth/login", login, principal{}), 401)
	login["password"] = "integration-password-123"
	requireStatus(t, f.request("POST", "/auth/login", login, principal{}), 429)
	if _, err := f.db.Exec(ctx, `UPDATE login_attempts SET expires_at=now()-interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.request("POST", "/auth/login", login, principal{}), 200)
}

func reviewRegressionSnapshot(t *testing.T, f *fixture) string {
	t.Helper()
	response := f.request("GET", "/patients/"+f.patient.ID, nil, f.doctor)
	requireStatus(t, response, 200)
	var profile struct {
		Snapshot string `json:"review_snapshot"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &profile); err != nil {
		t.Fatal(err)
	}
	if profile.Snapshot == "" {
		t.Fatal("doctor profile did not include review snapshot")
	}
	return profile.Snapshot
}
func TestReviewRegressionRejectsUnseenDiarySnapshot(t *testing.T) {
	f := setup(t)
	before := reviewRegressionSnapshot(t, f)
	if again := reviewRegressionSnapshot(t, f); again != before {
		t.Fatal("unchanged profile snapshot should be deterministic")
	}
	submission := f.request("POST", "/patient/diaries", emaInput(f.today, "73000000-0000-4000-8000-000000000001", false), f.patient)
	requireStatus(t, submission, 201)
	entry := decodeID(t, submission)
	requireStatus(t, f.request("POST", "/patients/"+f.patient.ID+"/reviews", map[string]string{"action": "reviewed", "note": "Old view", "snapshot_token": before}, f.doctor), 409)
	var count int
	if err := f.db.QueryRow(context.Background(), `SELECT count(*) FROM reviews WHERE patient_id=$1`, f.patient.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale review was recorded: %d %v", count, err)
	}
	after := reviewRegressionSnapshot(t, f)
	if after == before {
		t.Fatal("new diary entry did not change review snapshot")
	}
	requireStatus(t, f.request("POST", "/patients/"+f.patient.ID+"/reviews", map[string]string{"action": "reviewed", "note": "Current view", "snapshot_token": after}, f.doctor), 201)
	var reviewed bool
	if err := f.db.QueryRow(context.Background(), `SELECT $2::uuid=ANY(source_entry_ids) FROM reviews WHERE patient_id=$1`, f.patient.ID, entry).Scan(&reviewed); err != nil || !reviewed {
		t.Fatalf("current reviewed source missing: %v %v", reviewed, err)
	}
}
