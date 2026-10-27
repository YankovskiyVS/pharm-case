package mailer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

type desktopClient struct {
	Installed struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	} `json:"installed"`
}

func randomURLString() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func consentURL(clientID, redirect, state, verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	q := url.Values{"client_id": {clientID}, "redirect_uri": {redirect}, "response_type": {"code"}, "scope": {SendScope}, "access_type": {"offline"}, "prompt": {"consent"}, "state": {state}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(digest[:])}}
	return "https://accounts.google.com/o/oauth2/v2/auth?" + q.Encode()
}
func writeCredentials(path string, c Credentials) error {
	if err := c.validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".gmail-credentials-*")
	if err != nil {
		return errors.New("cannot create Gmail credential file")
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(data, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return errors.New("cannot write Gmail credential file")
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return errors.New("cannot install Gmail credential file")
	}
	return nil
}

// Authorize is an explicitly invoked operator-only Desktop OAuth flow. It does
// not connect to Gmail or send email. Tokens are written only to a private file.
func Authorize(ctx context.Context, clientFile, outputFile, listen string, out io.Writer) error {
	h := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	return authorize(ctx, clientFile, outputFile, listen, out, h)
}

func authorize(ctx context.Context, clientFile, outputFile, listen string, out io.Writer, h *http.Client) error {
	if clientFile == "" || outputFile == "" || filepath.Clean(clientFile) == filepath.Clean(outputFile) {
		return errors.New("distinct GMAIL_CLIENT_FILE and GMAIL_CREDENTIALS_FILE are required")
	}
	f, err := os.Open(clientFile)
	if err != nil {
		return errors.New("google Desktop OAuth client file unavailable")
	}
	var desktop desktopClient
	err = json.NewDecoder(io.LimitReader(f, 32768)).Decode(&desktop)
	_ = f.Close()
	if err != nil || desktop.Installed.ClientID == "" || desktop.Installed.ClientSecret == "" {
		return errors.New("invalid Google Desktop OAuth client JSON")
	}
	if listen == "" {
		listen = "127.0.0.1:0"
	}
	if listen != "127.0.0.1:0" && listen != "127.0.0.1:8765" && listen != "0.0.0.0:8765" {
		return errors.New("unsupported Gmail OAuth loopback listener")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	ln, err := net.Listen("tcp4", listen)
	if err != nil {
		return errors.New("gmail OAuth listener unavailable")
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	host := "127.0.0.1:" + port
	redirect := "http://" + host + "/callback"
	state, err := randomURLString()
	if err != nil {
		_ = ln.Close()
		return err
	}
	verifier, err := randomURLString()
	if err != nil {
		_ = ln.Close()
		return err
	}
	type callback struct {
		code string
		err  error
	}
	result := make(chan callback, 1)
	var consumed atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("GET /callback", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if r.Host != host || subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(state)) != 1 {
			http.Error(w, "Invalid authorization state", 400)
			return
		}
		if !consumed.CompareAndSwap(false, true) {
			http.Error(w, "Authorization already received", http.StatusConflict)
			return
		}
		v := callback{code: r.URL.Query().Get("code")}
		if r.URL.Query().Get("error") != "" || v.code == "" {
			v.err = errors.New("google authorization was not granted")
		}
		select {
		case result <- v:
			_, _ = fmt.Fprintln(w, "Authorization received. Return to the terminal.")
		default:
			http.Error(w, "Authorization already received", http.StatusConflict)
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 10 * time.Second}
	serveErr := make(chan struct{}, 1)
	go func() { _ = srv.Serve(ln); serveErr <- struct{}{} }()
	defer func() {
		closeCtx, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = srv.Shutdown(closeCtx)
	}()
	if _, err = fmt.Fprintln(out, "Open this URL in a browser on this computer to authorize the Gmail sender:\n"+consentURL(desktop.Installed.ClientID, redirect, state, verifier)); err != nil {
		return err
	}
	var grant callback
	select {
	case grant = <-result:
	case <-ctx.Done():
		return errors.New("gmail authorization timed out or cancelled")
	case <-serveErr:
		return errors.New("gmail OAuth listener stopped")
	}
	if grant.err != nil {
		return grant.err
	}
	v := url.Values{"code": {grant.code}, "client_id": {desktop.Installed.ClientID}, "client_secret": {desktop.Installed.ClientSecret}, "redirect_uri": {redirect}, "code_verifier": {verifier}, "grant_type": {"authorization_code"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(v.Encode()))
	if err != nil {
		return failure("gmail_auth_failed")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := h.Do(req)
	if err != nil {
		return networkError(ctx)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != 200 {
		return statusError(res.StatusCode)
	}
	var token struct {
		RefreshToken string `json:"refresh_token"`
		Scope        string `json:"scope"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 32768)).Decode(&token) != nil || token.RefreshToken == "" || !sendScopeGranted(token.Scope) {
		return errors.New("google did not issue an offline refresh token; authorize with consent again")
	}
	c := Credentials{ClientID: desktop.Installed.ClientID, ClientSecret: desktop.Installed.ClientSecret, RefreshToken: token.RefreshToken}
	if err = writeCredentials(outputFile, c); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, "Gmail credentials saved with mode 0600. No email was sent.")
	return err
}
