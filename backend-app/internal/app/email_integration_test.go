package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/url"
	"psitech/internal/mailer"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type gmailCapture struct {
	server   *httptest.Server
	messages chan string
	fail     atomic.Bool
}
type gmailTestTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (t gmailTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "oauth2.googleapis.com" && r.URL.Host != "gmail.googleapis.com" {
		panic("unexpected external mail request")
	}
	copyRequest := r.Clone(r.Context())
	u := *r.URL
	u.Scheme = t.target.Scheme
	u.Host = t.target.Host
	copyRequest.URL = &u
	return t.base.RoundTrip(copyRequest)
}
func captureGmail(t *testing.T) *gmailCapture {
	t.Helper()
	c := &gmailCapture{messages: make(chan string, 20)}
	c.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/token" {
			io.WriteString(w, `{"access_token":"synthetic-access","token_type":"Bearer","expires_in":3600}`)
			return
		}
		if r.URL.Path != "/gmail/v1/users/me/messages/send" {
			http.Error(w, "unexpected route", 404)
			return
		}
		if c.fail.Load() {
			http.Error(w, "synthetic provider failure", http.StatusServiceUnavailable)
			return
		}
		var message struct {
			Raw string `json:"raw"`
		}
		if json.NewDecoder(r.Body).Decode(&message) != nil {
			http.Error(w, "bad JSON", 400)
			return
		}
		raw, err := base64.RawURLEncoding.DecodeString(message.Raw)
		if err != nil {
			http.Error(w, "bad raw", 400)
			return
		}
		c.messages <- string(raw)
		io.WriteString(w, `{"id":"synthetic-accepted"}`)
	}))
	t.Cleanup(c.server.Close)
	return c
}
func (c *gmailCapture) config() mailer.Config {
	target, _ := url.Parse(c.server.URL)
	return mailer.Config{From: "notice@psitech.local", Credentials: mailer.Credentials{ClientID: "test-client", ClientSecret: "test-secret", RefreshToken: "test-refresh"}, HTTPClient: &http.Client{Transport: gmailTestTransport{target: target, base: c.server.Client().Transport}}, Timeout: time.Second}
}
func configureTestEmail(f *fixture, c *gmailCapture) {
	sender, err := mailer.New(c.config())
	if err != nil {
		panic(err)
	}
	f.server.config.Email = &EmailConfig{Enabled: true, Gmail: sender, PublicURL: "http://localhost:8080", EncryptionKey: bytes.Repeat([]byte{42}, 32)}
}
func captureMessage(t *testing.T, c *gmailCapture) (mail.Header, string) {
	t.Helper()
	select {
	case raw := <-c.messages:
		message, err := mail.ReadMessage(strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, message.Body))
		if err != nil {
			t.Fatal(err)
		}
		return message.Header, string(body)
	case <-time.After(time.Second):
		t.Fatal("no Gmail acceptance")
		return nil, ""
	}
}
func TestIntegrationEmailRetrySurvivesWorkerRestart(t *testing.T) {
	f := setup(t)
	capture := captureGmail(t)
	configureTestEmail(f, capture)
	ctx := context.Background()
	response := f.request("POST", "/patient/risk-signals", map[string]string{"form_id": "70000000-0000-4000-8000-000000000001"}, f.patient)
	requireStatus(t, response, 201)
	if err := f.server.dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	capture.fail.Store(true)
	if err := f.server.processEmail(ctx); err == nil {
		t.Fatal("failed Gmail acceptance reported successful")
	}
	var id, status, errorCode string
	var attempts int
	if err := f.db.QueryRow(ctx, `SELECT id,status,attempts,last_error_code FROM email_jobs WHERE source_type='notification'`).Scan(&id, &status, &attempts, &errorCode); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || attempts != 1 || errorCode != "gmail_provider_failed" {
		t.Fatalf("failed job state %s %d %s", status, attempts, errorCode)
	}
	if err := f.server.processEmail(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(ctx, `SELECT attempts FROM email_jobs WHERE id=$1`, id).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatal("retry ignored backoff")
	}
	if _, err := f.db.Exec(ctx, `UPDATE email_jobs SET available_at=now()-interval '1 second' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	capture.fail.Store(false)
	restarted := New(f.db, f.server.config)
	if err := restarted.processEmail(ctx); err != nil {
		t.Fatal(err)
	}
	headers, body := captureMessage(t, capture)
	if headers.Get("Message-ID") != "<"+id+"@psitech>" {
		t.Fatal("retry changed Message-ID")
	}
	if !strings.Contains(body, "/doctor/") || strings.Contains(body, f.patient.Name) || strings.Contains(body, "Суицидальные") {
		t.Fatal("clinical details exposed in email")
	}
	if err := restarted.processEmail(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-capture.messages:
		t.Fatal("sent job delivered twice")
	default:
	}
	if err := f.db.QueryRow(ctx, `SELECT status FROM email_jobs WHERE id=$1`, id).Scan(&status); err != nil || status != "sent" {
		t.Fatal("Gmail acceptance not persisted")
	}
}
func TestIntegrationInvitationEmailEncryptedAndInvalidated(t *testing.T) {
	f := setup(t)
	capture := captureGmail(t)
	configureTestEmail(f, capture)
	ctx := context.Background()
	raw := token()
	var invitation string
	tx, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = tx.QueryRow(ctx, `INSERT INTO invitations(token_hash,doctor_id,email,name,expires_at) VALUES($1,$2,'invite@psitech.local','Synthetic Invite',now()+interval '7 days') RETURNING id`, hash(raw), f.doctor.ID).Scan(&invitation)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.server.enqueueInvitationEmail(ctx, tx, f.doctor.ID, "invite@psitech.local", raw, invitation); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var encrypted []byte
	if err = f.db.QueryRow(ctx, `SELECT encrypted_payload FROM email_jobs WHERE invitation_id=$1`, invitation).Scan(&encrypted); err != nil || bytes.Contains(encrypted, []byte(raw)) {
		t.Fatal("bearer token not encrypted at rest")
	}
	if err = f.server.processEmail(ctx); err != nil {
		t.Fatal(err)
	}
	_, body := captureMessage(t, capture)
	if !strings.Contains(body, "/patient/#invite="+raw) {
		t.Fatal("invitation link absent")
	}
	var cleared bool
	if err = f.db.QueryRow(ctx, `SELECT encrypted_payload IS NULL FROM email_jobs WHERE invitation_id=$1`, invitation).Scan(&cleared); err != nil || !cleared {
		t.Fatal("token not cleared after acceptance")
	}
	// A later invitation that was accepted before the worker ran must not be sent.
	tx, err = f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw = token()
	err = tx.QueryRow(ctx, `INSERT INTO invitations(token_hash,doctor_id,email,name,expires_at,accepted_at) VALUES($1,$2,'accepted@psitech.local','Synthetic Accepted',now()+interval '7 days',now()) RETURNING id`, hash(raw), f.doctor.ID).Scan(&invitation)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.server.enqueueInvitationEmail(ctx, tx, f.doctor.ID, "accepted@psitech.local", raw, invitation); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = f.server.processEmail(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-capture.messages:
		t.Fatal("accepted invitation emailed")
	default:
	}
	var status string
	if err = f.db.QueryRow(ctx, `SELECT status FROM email_jobs WHERE invitation_id=$1`, invitation).Scan(&status); err != nil || status != "cancelled" {
		t.Fatal("accepted invite not cancelled")
	}
}
func TestIntegrationEmailRechecksCareAccess(t *testing.T) {
	f := setup(t)
	capture := captureGmail(t)
	configureTestEmail(f, capture)
	ctx := context.Background()
	response := f.request("POST", "/patient/risk-signals", map[string]string{"form_id": "70000000-0000-4000-8000-000000000002"}, f.patient)
	requireStatus(t, response, 201)
	if err := f.server.dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(ctx, `UPDATE care_links SET active=false WHERE doctor_id=$1 AND patient_id=$2`, f.doctor.ID, f.patient.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.server.processEmail(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-capture.messages:
		t.Fatal("revoked recipient emailed")
	default:
	}
	var status string
	if err := f.db.QueryRow(ctx, `SELECT status FROM email_jobs WHERE recipient_id=$1`, f.doctor.ID).Scan(&status); err != nil || status != "cancelled" {
		t.Fatal("revoked email job not cancelled")
	}
}
