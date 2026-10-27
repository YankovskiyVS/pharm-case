package mailer

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type redirectTransport struct {
	server    *url.URL
	transport http.RoundTripper
}

func (t redirectTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || (r.URL.Host != "oauth2.googleapis.com" && r.URL.Host != "gmail.googleapis.com") {
		return nil, fmt.Errorf("unexpected test destination")
	}
	c := r.Clone(r.Context())
	u := *r.URL
	u.Scheme = t.server.Scheme
	u.Host = t.server.Host
	c.URL = &u
	return t.transport.RoundTrip(c)
}
func testClient(t *testing.T, handler http.HandlerFunc) *http.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	target, _ := url.Parse(server.URL)
	return &http.Client{Transport: redirectTransport{server: target, transport: server.Client().Transport}}
}
func testConfig(h *http.Client) Config {
	return Config{From: "ПсиTech <sender@example.org>", Credentials: Credentials{ClientID: "test-client", ClientSecret: "private-client", RefreshToken: "private-refresh"}, HTTPClient: h, Timeout: time.Second}
}
func testMessage() Message {
	return Message{To: "patient@example.org", Subject: "Новое уведомление", Text: "Откройте кабинет", MessageID: "stable-job@psitech"}
}
func newClient(t *testing.T, h *http.Client) *Client {
	t.Helper()
	c, err := New(testConfig(h))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestGmailRawMIMEAndConcurrentTokenCache(t *testing.T) {
	var refreshes, sends atomic.Int32
	h := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			refreshes.Add(1)
			if r.ParseForm() != nil || r.Form.Get("refresh_token") != "private-refresh" || r.Form.Get("grant_type") != "refresh_token" {
				t.Error("wrong offline grant")
			}
			io.WriteString(w, `{"access_token":"cached-token","token_type":"Bearer","expires_in":3600}`)
			return
		}
		if r.URL.Path != "/gmail/v1/users/me/messages/send" || r.Header.Get("Authorization") != "Bearer cached-token" {
			t.Error("wrong Gmail request")
		}
		var payload struct {
			Raw string `json:"raw"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			return
		}
		raw, err := base64.RawURLEncoding.DecodeString(payload.Raw)
		if err != nil {
			t.Error(err)
			return
		}
		m, err := mail.ReadMessage(strings.NewReader(string(raw)))
		if err != nil {
			t.Error(err)
			return
		}
		body, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, m.Body))
		if err != nil || string(body) != testMessage().Text || m.Header.Get("Message-ID") != "<stable-job@psitech>" || m.Header.Get("Content-Type") != "text/plain; charset=UTF-8" {
			t.Error("invalid UTF8 MIME or retry identity")
		}
		if _, err = mail.ParseAddress(m.Header.Get("From")); err != nil {
			t.Error(err)
		}
		sends.Add(1)
		io.WriteString(w, `{"id":"accepted"}`)
	})
	c := newClient(t, h)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.Send(context.Background(), testMessage()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if refreshes.Load() != 1 || sends.Load() != 12 {
		t.Fatalf("cache refreshes=%d sends=%d", refreshes.Load(), sends.Load())
	}
	c.expires = time.Now().Add(-time.Second)
	if err := c.Send(context.Background(), testMessage()); err != nil {
		t.Fatal(err)
	}
	if refreshes.Load() != 2 || sends.Load() != 13 {
		t.Fatal("expired access token was reused")
	}

}

func TestGmailRejectedAccessTokenRefreshedOnce(t *testing.T) {
	for _, permanent := range []bool{false, true} {
		t.Run(fmt.Sprint(permanent), func(t *testing.T) {
			var refreshes, sends atomic.Int32
			h := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					n := refreshes.Add(1)
					fmt.Fprintf(w, `{"access_token":"token-%d","token_type":"Bearer","expires_in":3600}`, n)
					return
				}
				sends.Add(1)
				if permanent || r.Header.Get("Authorization") == "Bearer token-1" {
					http.Error(w, "private rejected token", http.StatusUnauthorized)
					return
				}
				io.WriteString(w, `{"id":"accepted"}`)
			})
			err := newClient(t, h).Send(context.Background(), testMessage())
			if permanent && ErrorCode(err) != "gmail_auth_failed" {
				t.Fatal(err)
			}
			if !permanent && err != nil {
				t.Fatal(err)
			}
			if refreshes.Load() != 2 || sends.Load() != 2 {
				t.Fatalf("unbounded/missing retry %d %d", refreshes.Load(), sends.Load())
			}
		})
	}
}

func TestGmailSafeFailuresQuotaAndNoRedirect(t *testing.T) {
	for _, tc := range []struct {
		status     int
		body, code string
	}{{429, `private payload`, "gmail_quota"}, {403, `{"error":{"errors":[{"reason":"userRateLimitExceeded"}]}}`, "gmail_quota"}, {403, `private denied`, "gmail_forbidden"}, {503, `private provider data`, "gmail_provider_failed"}, {400, `private invalid message`, "gmail_invalid_message"}, {302, `private redirect`, "gmail_provider_failed"}} {
		t.Run(fmt.Sprint(tc.status, tc.code), func(t *testing.T) {
			h := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					io.WriteString(w, `{"access_token":"token","token_type":"Bearer","expires_in":3600}`)
					return
				}
				w.Header().Set("Location", "https://untrusted.example/steal")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			})
			err := newClient(t, h).Send(context.Background(), testMessage())
			if err == nil || ErrorCode(err) != tc.code || strings.Contains(err.Error(), "private") {
				t.Fatalf("unsafe or wrong error: %v", err)
			}
		})
	}

	t.Run("missing send grant", func(t *testing.T) {
		h := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/token" {
				t.Error("scope-limited token reached Gmail")
			}
			io.WriteString(w, `{"access_token":"token","token_type":"Bearer","expires_in":3600,"scope":"https://www.googleapis.com/auth/drive.readonly"}`)
		})
		if ErrorCode(newClient(t, h).Send(context.Background(), testMessage())) != "gmail_auth_failed" {
			t.Fatal("missing send grant accepted")
		}
	})
	t.Run("revoked refresh token", func(t *testing.T) {
		h := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":"invalid_grant","private":"secret"}`, 400)
		})
		err := newClient(t, h).Send(context.Background(), testMessage())
		if ErrorCode(err) != "gmail_auth_failed" || strings.Contains(err.Error(), "secret") {
			t.Fatal(err)
		}
	})
}

func TestGmailCancellationAndCacheWaitCancellation(t *testing.T) {
	started := make(chan struct{})
	h := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	})
	c := newClient(t, h)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Send(ctx, testMessage()) }()
	<-started
	waitCtx, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	start := time.Now()
	err := c.Send(waitCtx, testMessage())
	if ErrorCode(err) != "gmail_cancelled" || time.Since(start) > time.Second {
		t.Fatalf("blocked token waiter: %v", err)
	}
	cancel()
	if ErrorCode(<-done) != "gmail_cancelled" {
		t.Fatal("in-flight request ignored cancellation")
	}
}

func TestGmailCredentialAndHeaderValidation(t *testing.T) {
	c := testConfig(nil)
	for _, bad := range []Config{{}, {From: "sender@example.org", Credentials: Credentials{ClientID: "id"}}, {From: "sender@example.org\r\nBcc: other@example.org", Credentials: c.Credentials}, {From: c.From, Credentials: c.Credentials, Timeout: time.Minute}} {
		if _, err := New(bad); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
	client := newClient(t, testClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid message reached provider") }))
	for _, m := range []Message{{To: "a@example.org", Subject: "safe\r\nBcc: other@example.org", MessageID: "id"}, {To: "a@example.org\r\nBcc: other@example.org", MessageID: "id"}, {To: "a@example.org", MessageID: "id\r\n"}} {
		if client.Send(context.Background(), m) == nil {
			t.Fatal("header injection accepted")
		}
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	if err := writeCredentials(path, c.Credentials); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("unsafe credential file mode")
	}
	loaded, err := LoadCredentials(path)
	if err != nil || loaded != c.Credentials {
		t.Fatal("credential roundtrip failed")
	}
	for _, data := range []string{`{}`, `{"client_id":"secret"}`, `{"client_id":"a","client_secret":"b","refresh_token":"c","endpoint":"http://evil"}`, `{} {}`} {
		os.WriteFile(path, []byte(data), 0600)
		if _, err = LoadCredentials(path); err == nil {
			t.Fatal("malformed credential accepted")
		}
	}
}

type authorizationOutput struct {
	mu   sync.Mutex
	text strings.Builder
	url  chan string
}

func (o *authorizationOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.text.Write(p)
	for _, line := range strings.Split(string(p), "\n") {
		if strings.HasPrefix(line, "https://accounts.google.com/") {
			o.url <- line
		}
	}
	return len(p), nil
}
func TestDesktopOAuthStatePKCEOneTimeAndPrivateOutput(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "client.json")
	target := filepath.Join(dir, "credentials.json")
	os.WriteFile(source, []byte(`{"installed":{"client_id":"desktop-id","client_secret":"private-client"}}`), 0600)
	output := &authorizationOutput{url: make(chan string, 1)}
	entered := make(chan struct{})
	release := make(chan struct{})
	var challenge string
	h := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			t.Error("authorization attempted to send mail")
		}
		if r.ParseForm() != nil {
			t.Error("bad exchange")
		}
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge || r.Form.Get("code") != "private-code" || r.Form.Get("grant_type") != "authorization_code" {
			t.Error("PKCE exchange mismatch")
		}
		close(entered)
		<-release
		io.WriteString(w, `{"refresh_token":"private-refresh"}`)
	})
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { done <- authorize(ctx, source, target, "", output, h) }()
	var raw string
	select {
	case raw = <-output.url:
	case <-ctx.Done():
		t.Fatal("missing consent URL")
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	challenge = q.Get("code_challenge")
	if q.Get("scope") != SendScope || q.Get("access_type") != "offline" || q.Get("prompt") != "consent" || q.Get("code_challenge_method") != "S256" || len(q.Get("state")) < 40 {
		t.Fatal("unsafe OAuth consent parameters")
	}
	callback := q.Get("redirect_uri")
	res, err := http.Get(callback + "?state=wrong&code=private-code")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 400 {
		t.Fatal("wrong state accepted")
	}
	url := callback + "?state=" + q.Get("state") + "&code=private-code"
	res, err = http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal("valid callback rejected")
	}
	<-entered
	res, err = http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 409 {
		t.Fatal("callback replay accepted")
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	credentials, err := LoadCredentials(target)
	if err != nil || credentials.RefreshToken != "private-refresh" {
		t.Fatal("offline grant not saved")
	}
	output.mu.Lock()
	printed := output.text.String()
	output.mu.Unlock()
	for _, secret := range []string{"private-refresh", "private-client", "private-code"} {
		if strings.Contains(printed, secret) {
			t.Fatal("OAuth secret printed")
		}
	}
}
