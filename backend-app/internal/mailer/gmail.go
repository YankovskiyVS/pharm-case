// Package mailer delivers minimal text messages using Google's Gmail API only.
package mailer

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"strings"
	"time"
)

const tokenURL = "https://oauth2.googleapis.com/token"
const sendURL = "https://gmail.googleapis.com/gmail/v1/users/me/messages/send"

// SendScope is the sole permission requested from the sending Google account.
const SendScope = "https://www.googleapis.com/auth/gmail.send"

func sendScopeGranted(scope string) bool {
	if scope == "" {
		return true
	} // Google may omit unchanged scope on refresh.
	for _, s := range strings.Fields(scope) {
		if s == SendScope {
			return true
		}
	}
	return false
}

// Credentials contains the offline OAuth grant, never an account password.
type Credentials struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
}

func (c Credentials) validate() error {
	for _, value := range []string{c.ClientID, c.ClientSecret, c.RefreshToken} {
		if strings.TrimSpace(value) == "" || len(value) > 8192 || strings.ContainsAny(value, "\r\n") {
			return errors.New("invalid Gmail OAuth credentials")
		}
	}
	return nil
}

// LoadCredentials reads the explicitly mounted operator-authorized grant.
func LoadCredentials(path string) (Credentials, error) {
	var c Credentials
	f, err := os.Open(path)
	if err != nil {
		return c, errors.New("gmail credential file unavailable")
	}
	defer func() { _ = f.Close() }()
	d := json.NewDecoder(io.LimitReader(f, 32769))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil {
		return c, errors.New("invalid Gmail credential JSON")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return c, errors.New("invalid Gmail credential JSON")
	}
	return c, c.validate()
}

// Config uses fixed official Google endpoints. HTTPClient is a code-level
// transport injection for isolated tests; no endpoint is configurable by env.
type Config struct {
	From        string
	Credentials Credentials
	Timeout     time.Duration
	HTTPClient  *http.Client
}

func (c Config) Validate() error {
	if _, err := mailbox(c.From); err != nil {
		return errors.New("invalid Gmail sender")
	}
	if c.Timeout < 0 || c.Timeout > 30*time.Second {
		return errors.New("gmail timeout must not exceed 30 seconds")
	}
	return c.Credentials.validate()
}

// Client shares a bounded, context-aware token cache across queue jobs.
type Client struct {
	config  Config
	http    *http.Client
	gate    chan struct{}
	token   string
	expires time.Time
}

func New(c Config) (*Client, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.Timeout == 0 {
		c.Timeout = 10 * time.Second
	}
	h := c.HTTPClient
	if h == nil {
		h = &http.Client{Timeout: c.Timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	} else {
		copyClient := *h
		copyClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
		h = &copyClient
	}
	return &Client{config: c, http: h, gate: make(chan struct{}, 1)}, nil
}

// Message is an intentionally minimal single-recipient RFC 5322 message.
type Message struct {
	To        string
	Subject   string
	Text      string
	MessageID string
}

type deliveryError struct{ code string }

func (e *deliveryError) Error() string { return e.code }
func failure(code string) error        { return &deliveryError{code: code} }

// ErrorCode returns a safe operational classification, without provider payloads.
func ErrorCode(err error) string {
	var e *deliveryError
	if errors.As(err, &e) {
		return e.code
	}
	return "gmail_unavailable"
}
func statusError(status int) error {
	switch status {
	case 400, 401:
		return failure("gmail_auth_failed")
	case 403:
		return failure("gmail_forbidden")
	case 429:
		return failure("gmail_quota")
	default:
		return failure("gmail_provider_failed")
	}
}

func sendStatusError(status int, body io.Reader) error {
	if status == 400 {
		return failure("gmail_invalid_message")
	}
	if status == 403 {
		var value struct {
			Error struct {
				Errors []struct {
					Reason string `json:"reason"`
				} `json:"errors"`
			} `json:"error"`
		}
		if json.NewDecoder(io.LimitReader(body, 32768)).Decode(&value) == nil {
			for _, e := range value.Error.Errors {
				switch e.Reason {
				case "rateLimitExceeded", "userRateLimitExceeded", "dailyLimitExceeded":
					return failure("gmail_quota")
				}
			}
		}
	}
	return statusError(status)
}
func networkError(ctx context.Context) error {
	if ctx.Err() != nil {
		return failure("gmail_cancelled")
	}
	return failure("gmail_unavailable")
}
func (c *Client) accessToken(ctx context.Context, rejected string) (string, error) {
	select {
	case c.gate <- struct{}{}:
	case <-ctx.Done():
		return "", networkError(ctx)
	}
	defer func() { <-c.gate }()
	if c.token != "" && time.Now().Before(c.expires) && c.token != rejected {
		return c.token, nil
	}
	v := url.Values{"client_id": {c.config.Credentials.ClientID}, "client_secret": {c.config.Credentials.ClientSecret}, "refresh_token": {c.config.Credentials.RefreshToken}, "grant_type": {"refresh_token"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(v.Encode()))
	if err != nil {
		return "", failure("gmail_auth_failed")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.http.Do(req)
	if err != nil {
		return "", networkError(ctx)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != 200 {
		return "", statusError(res.StatusCode)
	}
	var token struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		TokenType   string `json:"token_type"`
		Scope       string `json:"scope"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 32768)).Decode(&token) != nil || token.AccessToken == "" || strings.ContainsAny(token.AccessToken, "\r\n") || !strings.EqualFold(token.TokenType, "Bearer") || token.ExpiresIn < 1 || token.ExpiresIn > 86400 || !sendScopeGranted(token.Scope) {
		return "", failure("gmail_auth_failed")
	}
	c.token = token.AccessToken
	c.expires = time.Now().Add(time.Duration(token.ExpiresIn)*time.Second - 30*time.Second)
	return c.token, nil
}

func mailbox(value string) (*mail.Address, error) {
	if strings.ContainsAny(value, "\r\n") {
		return nil, errors.New("invalid email address")
	}
	a, e := mail.ParseAddress(value)
	if e != nil || !strings.Contains(a.Address, "@") {
		return nil, errors.New("invalid email address")
	}
	return a, nil
}
func rawMessage(from string, m Message) ([]byte, error) {
	f, err := mailbox(from)
	if err != nil {
		return nil, err
	}
	to, err := mailbox(m.To)
	if err != nil {
		return nil, err
	}
	if m.MessageID == "" || strings.ContainsAny(m.MessageID, "\r\n <>") || strings.ContainsAny(m.Subject, "\r\n") {
		return nil, errors.New("invalid email headers")
	}
	body := base64.StdEncoding.EncodeToString([]byte(m.Text))
	var wrapped strings.Builder
	for len(body) > 76 {
		wrapped.WriteString(body[:76])
		wrapped.WriteString("\r\n")
		body = body[76:]
	}
	wrapped.WriteString(body)
	wrapped.WriteString("\r\n")
	headers := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMessage-ID: <%s>\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n", f.String(), to.String(), mime.QEncoding.Encode("UTF-8", m.Subject), m.MessageID, time.Now().UTC().Format(time.RFC1123Z))
	return []byte(headers + wrapped.String()), nil
}

// Send succeeds only after Gmail returns an accepted message ID. A rejected
// access token is refreshed once; quota/provider failures return to the durable queue.
func (c *Client) Send(ctx context.Context, m Message) error {
	raw, err := rawMessage(c.config.From, m)
	if err != nil {
		return failure("gmail_invalid_message")
	}
	ctx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()
	payload, _ := json.Marshal(map[string]string{"raw": base64.RawURLEncoding.EncodeToString(raw)})
	rejected := ""
	for attempt := 0; attempt < 2; attempt++ {
		token, err := c.accessToken(ctx, rejected)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, sendURL, bytes.NewReader(payload))
		if err != nil {
			return failure("gmail_invalid_message")
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		res, err := c.http.Do(req)
		if err != nil {
			return networkError(ctx)
		}
		var accepted struct {
			ID string `json:"id"`
		}
		if res.StatusCode == 200 {
			err = json.NewDecoder(io.LimitReader(res.Body, 32768)).Decode(&accepted)
		}
		var statusErr error
		if res.StatusCode != 200 {
			statusErr = sendStatusError(res.StatusCode, res.Body)
		}
		_ = res.Body.Close()
		if res.StatusCode == 401 && attempt == 0 {
			rejected = token
			continue
		}
		if res.StatusCode != 200 {
			return statusErr
		}
		if err != nil || accepted.ID == "" {
			return failure("gmail_invalid_response")
		}
		return nil
	}
	return failure("gmail_auth_failed")
}
