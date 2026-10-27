package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"psitech/internal/store"
	"strings"
	"sync"
	"testing"
	"time"
)

type fixture struct {
	server                 *Server
	db                     *pgxpool.Pool
	patient, doctor, other principal
	today                  string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	connection := os.Getenv("TEST_DATABASE_URL")
	if connection == "" {
		t.Skip("TEST_DATABASE_URL not set; use docker compose run --rm api-test")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_" + token()[:16]
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	parsed, err := url.Parse(connection)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := store.Open(ctx, parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	api := New(db, Config{Origins: []string{"http://localhost:8080"}})
	if err = api.Seed(ctx, "integration-password-123", "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"); err != nil {
		t.Fatal(err)
	}
	f := &fixture{server: api, db: db, today: time.Now().In(time.FixedZone("Moscow", 3*3600)).Format("2006-01-02")}
	for i, p := range []*principal{&f.patient, &f.doctor, &f.other} {
		email := []string{"patient@psitech.local", "doctor@psitech.local", "patient2@psitech.local"}[i]
		err = db.QueryRow(ctx, `SELECT id,email,name,role,timezone FROM users WHERE email=$1`, email).Scan(&p.ID, &p.Email, &p.Name, &p.Role, &p.Timezone)
		if err != nil {
			t.Fatal(err)
		}
		raw := token()
		p.Session = raw
		p.CSRF = token()
		if _, err = db.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,csrf,expires_at) VALUES($1,$2,$3,now()+interval '1 hour')`, hash(raw), p.ID, p.CSRF); err != nil {
			t.Fatal(err)
		}
	}
	return f
}
func (f *fixture) request(method, path string, body any, p principal) *httptest.ResponseRecorder {
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, "http://localhost:8080/api/v1"+path, bytes.NewReader(data))
	r.Header.Set("Origin", "http://localhost:8080")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", p.CSRF)
	if p.Session != "" {
		r.AddCookie(&http.Cookie{Name: "psitech_session", Value: p.Session})
	}
	w := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(w, r)
	return w
}
func requireStatus(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("expected %d got %d: %s", status, w.Code, w.Body.String())
	}
}
func decodeID(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var data struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	return data.ID
}
func emaInput(day, form string, suicidal bool) map[string]any {
	return map[string]any{"kind": "ema", "local_date": day, "timezone": "Europe/Moscow", "form_id": form, "answers": map[string]any{"mood": 4, "anhedonia": 3, "energy": 7, "anxiety": 2, "suicidal": suicidal, "comment": "Synthetic integration entry"}}
}
func TestIntegrationClinicalFlow(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	form := "10000000-0000-4000-8000-000000000001"
	// Early positive response must survive even if the user never submits the rest.
	risk := f.request("POST", "/patient/risk-signals", map[string]string{"form_id": form}, f.patient)
	requireStatus(t, risk, 201)
	alertID := decodeID(t, risk)
	input := emaInput(f.today, form, true)
	entry := f.request("POST", "/patient/diaries", input, f.patient)
	requireStatus(t, entry, 201)
	entryID := decodeID(t, entry)
	replay := f.request("POST", "/patient/diaries", input, f.patient)
	requireStatus(t, replay, 200)
	if decodeID(t, replay) != entryID {
		t.Fatal("retry changed entry ID")
	}
	input["answers"].(map[string]any)["mood"] = 5
	requireStatus(t, f.request("POST", "/patient/diaries", input, f.patient), 409)
	var count int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM alerts WHERE patient_id=$1`, f.patient.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate alerts: %d %v", count, err)
	}
	// Simulate a process restart: work is in PostgreSQL, not in memory.
	restarted := New(f.db, f.server.config)
	if err := restarted.dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	if err := restarted.dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE recipient_id=$1 AND type='risk.raised'`, f.doctor.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("notification not exactly one: %d %v", count, err)
	}
	requireStatus(t, f.request("GET", "/patients/"+f.patient.ID, nil, f.other), 403)
	requireStatus(t, f.request("GET", "/doctor/patients", nil, f.patient), 403)
	requireStatus(t, f.request("POST", "/alerts/"+alertID+"/actions", map[string]string{"action": "ack", "note": ""}, f.patient), 403)
	requireStatus(t, f.request("POST", "/alerts/"+alertID+"/actions", map[string]string{"action": "resolved", "note": "Contacted"}, f.doctor), 409)
	requireStatus(t, f.request("POST", "/alerts/"+alertID+"/actions", map[string]string{"action": "ack", "note": ""}, f.doctor), 200)
	requireStatus(t, f.request("POST", "/alerts/"+alertID+"/actions", map[string]string{"action": "resolved", "note": ""}, f.doctor), 400)
	requireStatus(t, f.request("POST", "/alerts/"+alertID+"/actions", map[string]string{"action": "resolved", "note": "Synthetic test: contacted patient"}, f.doctor), 200)
	// Append-only correction; changing an answer does not erase a historical alert.
	corrected := emaInput(f.today, "10000000-0000-4000-8000-000000000002", false)
	corrected["supersedes_id"] = entryID
	requireStatus(t, f.request("POST", "/patient/diaries", corrected, f.patient), 201)
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM diary_entries WHERE patient_id=$1 AND local_date=$2 AND kind='ema'`, f.patient.ID, f.today).Scan(&count); err != nil || count != 2 {
		t.Fatalf("original overwritten %d %v", count, err)
	}
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM current_entries WHERE patient_id=$1 AND local_date=$2 AND kind='ema'`, f.patient.ID, f.today).Scan(&count); err != nil || count != 1 {
		t.Fatalf("current revisions %d %v", count, err)
	}
	requireStatus(t, f.request("GET", "/patients/"+f.patient.ID, nil, f.doctor), 200)
	requireStatus(t, f.request("GET", "/dashboard", nil, f.doctor), 200)
	requireStatus(t, f.request("GET", "/dashboard", nil, f.patient), 200)
	message := map[string]string{"body": "Synthetic message", "request_key": "20000000-0000-4000-8000-000000000001", "doctor_id": f.doctor.ID}
	requireStatus(t, f.request("POST", "/patients/"+f.patient.ID+"/messages", message, f.patient), 201)
	requireStatus(t, f.request("POST", "/patients/"+f.patient.ID+"/messages", message, f.patient), 201)
	message["body"] = "Different"
	requireStatus(t, f.request("POST", "/patients/"+f.patient.ID+"/messages", message, f.patient), 409)
	requireStatus(t, f.request("GET", "/patients/"+f.patient.ID+"/messages", nil, f.doctor), 200)
	requireStatus(t, f.request("GET", "/patients/"+f.patient.ID+"/messages", nil, f.other), 403)
	if _, err := f.db.Exec(ctx, `UPDATE care_links SET active=false WHERE doctor_id=$1 AND patient_id=$2`, f.doctor.ID, f.patient.ID); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.request("GET", "/patients/"+f.patient.ID, nil, f.doctor), 403)
	requireStatus(t, f.request("GET", "/patients/"+f.patient.ID+"/messages", nil, f.doctor), 403)
}
func TestIntegrationValidationAuthenticationAndInvitations(t *testing.T) {
	f := setup(t)
	invalid := f.patient
	invalid.CSRF = "wrong"
	requireStatus(t, f.request("POST", "/patient/risk-signals", map[string]string{"form_id": "10000000-0000-4000-8000-000000000001"}, invalid), 403)
	requireStatus(t, f.request("GET", "/dashboard", nil, principal{}), 401)
	login := map[string]string{"email": "doctor@psitech.local", "password": "integration-password-123", "portal": "doctor", "code": "000000"}
	requireStatus(t, f.request("POST", "/auth/login", login, principal{}), 401)
	login["code"] = TOTP("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", time.Now())
	requireStatus(t, f.request("POST", "/auth/login", login, principal{}), 200)
	login["portal"] = "patient"
	requireStatus(t, f.request("POST", "/auth/login", login, principal{}), 401)
	invite := f.request("POST", "/doctor/invitations", map[string]string{"email": "new@psitech.local", "name": "Synthetic New Patient"}, f.doctor)
	requireStatus(t, invite, 201)
	var invited struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(invite.Body.Bytes(), &invited); err != nil {
		t.Fatal(err)
	}
	acceptance := map[string]any{"token": invited.Token, "password": "new-password-123", "consent": true}
	requireStatus(t, f.request("POST", "/auth/accept-invitation", acceptance, principal{}), 201)
	requireStatus(t, f.request("POST", "/auth/accept-invitation", acceptance, principal{}), 400)
	invalidDiary := emaInput(f.today, "30000000-0000-4000-8000-000000000001", false)
	delete(invalidDiary["answers"].(map[string]any), "energy")
	requireStatus(t, f.request("POST", "/patient/diaries", invalidDiary, f.patient), 400)
	requireStatus(t, f.request("POST", "/patients/"+f.patient.ID+"/assignments", map[string]string{"kind": "csd", "start_date": f.today, "end_date": f.today, "due_time": "09:00"}, f.doctor), 200)
	requireStatus(t, f.request("POST", "/patients/"+f.patient.ID+"/reviews", map[string]string{"action": "reviewed", "note": "Synthetic review", "snapshot_token": reviewRegressionSnapshot(t, f)}, f.doctor), 201)
}
func TestIntegrationConcurrentRiskAndRollback(t *testing.T) {
	f := setup(t)
	form := "40000000-0000-4000-8000-000000000001"
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			w := f.request("POST", "/patient/risk-signals", map[string]string{"form_id": form}, f.patient)
			if w.Code != 201 {
				t.Errorf("concurrent risk: %d", w.Code)
			}
		}()
	}
	group.Wait()
	var count int
	if err := f.db.QueryRow(context.Background(), `SELECT count(*) FROM alerts WHERE patient_id=$1`, f.patient.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("risk dedup %d %v", count, err)
	}
	// A failing outbox insert must roll back the clinical entry AND alert.
	if _, err := f.db.Exec(context.Background(), `ALTER TABLE outbox_events ADD CONSTRAINT test_reject CHECK(type!='diary.submitted')`); err != nil {
		t.Fatal(err)
	}
	input := emaInput(f.today, "40000000-0000-4000-8000-000000000002", true)
	requireStatus(t, f.request("POST", "/patient/diaries", input, f.patient), 500)
	if err := f.db.QueryRow(context.Background(), `SELECT count(*) FROM diary_entries WHERE patient_id=$1 AND form_id=$2`, f.patient.ID, input["form_id"]).Scan(&count); err != nil || count != 0 {
		t.Fatalf("entry committed on failure %d %v", count, err)
	}
	if err := f.db.QueryRow(context.Background(), `SELECT count(*) FROM alerts WHERE patient_id=$1`, f.patient.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("alert committed on rollback %d %v", count, err)
	}
}
func TestIntegrationRemindersAreDurable(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, err := f.db.Exec(ctx, `UPDATE assignments SET due_time='00:00'`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := f.server.reminders(ctx); err != nil {
			t.Fatal(err)
		}
		if err := f.server.dispatch(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE recipient_id=$1 AND type='reminder.due'`, f.patient.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("reminders %d: %v", count, err)
	}
}

func TestIntegrationNotificationsAndRevisions(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	// All CSD inputs are accepted and Excel ratios survive JSON/persistence.
	form := "50000000-0000-4000-8000-000000000001"
	sleep := map[string]any{"kind": "csd", "local_date": f.today, "timezone": "Europe/Moscow", "form_id": form, "answers": map[string]any{"bedtime": "23:00", "rise_time": "07:00", "latency": 30, "total_sleep": 420, "awake_minutes": 20}}
	response := f.request("POST", "/patient/diaries", sleep, f.patient)
	requireStatus(t, response, 201)
	original := decodeID(t, response)
	var se float64
	if err := f.db.QueryRow(ctx, `SELECT (scores->>'se')::float FROM diary_entries WHERE id=$1`, original).Scan(&se); err != nil || se != .875 {
		t.Fatalf("SE %v %v", se, err)
	}
	sleep["form_id"] = "50000000-0000-4000-8000-000000000002"
	sleep["supersedes_id"] = original
	sleep["answers"].(map[string]any)["total_sleep"] = 450
	response = f.request("POST", "/patient/diaries", sleep, f.patient)
	requireStatus(t, response, 201)
	latest := decodeID(t, response)
	revisions := f.request("GET", "/patients/"+f.patient.ID+"/diaries/"+latest+"/revisions", nil, f.patient)
	requireStatus(t, revisions, 200)
	var chain []map[string]any
	if err := json.Unmarshal(revisions.Body.Bytes(), &chain); err != nil || len(chain) != 2 {
		t.Fatalf("revision chain %d %v", len(chain), err)
	}
	requireStatus(t, f.request("GET", "/patients/"+f.patient.ID+"/diaries/"+latest+"/revisions", nil, f.other), 403)
	if err := f.server.dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	inbox := f.request("GET", "/notifications", nil, f.doctor)
	requireStatus(t, inbox, 200)
	var notices []struct {
		Seq  int64 `json:"seq"`
		Read bool  `json:"is_read"`
	}
	if err := json.Unmarshal(inbox.Body.Bytes(), &notices); err != nil || len(notices) != 2 {
		t.Fatalf("inbox %d %v", len(notices), err)
	}
	if notices[0].Read {
		t.Fatal("new notification marked read")
	}
	requireStatus(t, f.request("POST", "/notifications/read", map[string]int64{"through": notices[0].Seq}, f.doctor), 200)
	inbox = f.request("GET", "/notifications", nil, f.doctor)
	if err := json.Unmarshal(inbox.Body.Bytes(), &notices); err != nil || !notices[0].Read {
		t.Fatal("read cursor not persisted")
	}
	otherInbox := f.request("GET", "/notifications", nil, f.other)
	var other []any
	if err := json.Unmarshal(otherInbox.Body.Bytes(), &other); err != nil || len(other) != 0 {
		t.Fatal("cross-recipient notifications leaked")
	}
	requireStatus(t, f.request("POST", "/auth/logout", map[string]bool{}, f.patient), 200)
	requireStatus(t, f.request("GET", "/me", nil, f.patient), 401)
}

func TestIntegrationSSEPersistsBeforeRefresh(t *testing.T) {
	f := setup(t)
	server := httptest.NewServer(f.server.Handler())
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: "psitech_session", Value: f.doctor.Session})
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	start := time.Now()
	risk := f.request("POST", "/patient/risk-signals", map[string]string{"form_id": "60000000-0000-4000-8000-000000000001"}, f.patient)
	requireStatus(t, risk, 201)
	if err = f.server.dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(response.Body)
	found := false
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "event: refresh") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no persisted event refresh: %v", scanner.Err())
	}
	if time.Since(start) >= 60*time.Second {
		t.Fatal("in-app event exceeded target")
	}
}
