package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
	"net"
	"net/http"
	"net/mail"
	"net/netip"
	"strings"
	"time"
)

// TOTP returns a six-digit RFC 6238 code for a base32 secret.
func TOTP(secret string, now time.Time) string {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil || len(key) < 16 {
		return ""
	}
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(now.Unix()/30))
	mac := hmac.New(sha1.New, key)
	if _, err = mac.Write(b); err != nil {
		return ""
	}
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 15
	n := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", n%1000000)
}
func validTOTP(secret, code string) bool {
	if len(code) != 6 {
		return false
	}
	for _, delta := range []time.Duration{-30 * time.Second, 0, 30 * time.Second} {
		if subtle.ConstantTimeCompare([]byte(TOTP(secret, time.Now().Add(delta))), []byte(code)) == 1 {
			return true
		}
	}
	return false
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Code     string `json:"code"`
		Portal   string `json:"portal"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	if len(in.Email) > 254 || len(in.Password) > 72 {
		writeError(w, 400, "Проверьте данные входа")
		return
	}
	keys := s.attemptKeys(r, in.Email, "login")
	limited, limitErr := s.attemptsLimited(r.Context(), keys, 30)
	if limitErr != nil {
		s.fail(w, r, limitErr)
		return
	}
	if limited {
		writeError(w, 429, "Слишком много неудачных попыток. Повторите через 10 минут")
		return
	}
	var u User
	var password, secret string
	err := s.db.QueryRow(r.Context(), `SELECT id,email,name,role,timezone,password_hash,totp_secret FROM users WHERE email=$1 AND active`, in.Email).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Timezone, &password, &secret)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.fail(w, r, err)
		return
	}
	// Run bcrypt for unknown identities too, avoiding a cheap account-existence oracle.
	compareHash := []byte(password)
	if errors.Is(err, pgx.ErrNoRows) {
		compareHash = []byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy")
	}
	passwordErr := bcrypt.CompareHashAndPassword(compareHash, []byte(in.Password))
	if err != nil || passwordErr != nil || u.Role != in.Portal {
		if err = s.noteFailure(r.Context(), keys); err != nil {
			s.fail(w, r, err)
			return
		}
		writeError(w, 401, "Проверьте почту, пароль и выбранный кабинет")
		return
	}
	if u.Role == "doctor" && !validTOTP(secret, in.Code) {
		if err = s.noteFailure(r.Context(), keys); err != nil {
			s.fail(w, r, err)
			return
		}
		writeError(w, 401, "Введите действующий код двухфакторной аутентификации")
		return
	}
	raw := token()
	csrf := token()
	_, err = s.db.Exec(r.Context(), `INSERT INTO sessions(token_hash,user_id,csrf,expires_at) VALUES($1,$2,$3,now()+interval '12 hours')`, hash(raw), u.ID, csrf)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "psitech_session", Value: raw, Path: "/api", HttpOnly: true, Secure: s.config.SecureCookies, SameSite: http.SameSiteStrictMode, MaxAge: 43200})
	writeJSON(w, 200, map[string]any{"user": u, "csrf": csrf})
}
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	p := current(r)
	writeJSON(w, 200, map[string]any{"user": p.User, "csrf": p.CSRF})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if _, err := s.db.Exec(r.Context(), `DELETE FROM sessions WHERE token_hash=$1`, current(r).Session); err != nil {
		s.fail(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "psitech_session", Value: "", Path: "/api", HttpOnly: true, Secure: s.config.SecureCookies, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) invite(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	in.Name = strings.TrimSpace(in.Name)
	address, err := mail.ParseAddress(in.Email)
	if err != nil || address.Address != in.Email || len(in.Email) > 254 || len([]rune(in.Name)) < 2 || len([]rune(in.Name)) > 120 {
		writeError(w, 400, "Укажите имя и корректную почту")
		return
	}
	raw := token()
	var id string
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rollback(tx, r.Context())
	var activeDoctor bool
	err = tx.QueryRow(r.Context(), `SELECT active AND role='doctor' FROM users WHERE id=$1 FOR SHARE`, current(r).ID).Scan(&activeDoctor)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !activeDoctor {
		writeError(w, 403, "Учётная запись врача недоступна")
		return
	}
	err = tx.QueryRow(r.Context(), `INSERT INTO invitations(token_hash,doctor_id,email,name,expires_at) VALUES($1,$2,$3,$4,now()+interval '7 days') RETURNING id`, hash(raw), current(r).ID, in.Email, in.Name).Scan(&id)
	if err == nil {
		err = s.enqueueInvitationEmail(r.Context(), tx, current(r).ID, in.Email, raw, id)
	}
	if err == nil {
		err = audit(r.Context(), tx, current(r), "invitation.create", id, r.Context().Value(requestKey{}).(string))
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 201, map[string]string{"id": id, "token": raw})
}
func (s *Server) acceptInvitation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token    string `json:"token"`
		Password string `json:"password"`
		Consent  bool   `json:"consent"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Password) < 12 || len(in.Password) > 72 || !in.Consent {
		writeError(w, 400, "Пароль: 12–72 символа. Подтвердите согласие на обработку данных")
		return
	}
	if len(in.Token) != 64 {
		writeError(w, 400, "Недействительное приглашение")
		return
	}
	keys := s.attemptKeys(r, "", "invitation")
	limited, err := s.attemptsLimited(r.Context(), keys, 30)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if limited {
		writeError(w, 429, "Слишком много попыток регистрации. Повторите позже")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rollback(tx, r.Context())
	var doctor, email, name, id, invitation string
	err = tx.QueryRow(r.Context(), `SELECT i.id,i.doctor_id,i.email,i.name FROM invitations i JOIN users d ON d.id=i.doctor_id WHERE i.token_hash=$1 AND i.accepted_at IS NULL AND i.expires_at>now() AND d.active AND d.role='doctor' FOR UPDATE OF i FOR SHARE OF d`, hash(in.Token)).Scan(&invitation, &doctor, &email, &name)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.fail(w, r, err)
			return
		}
		rollback(tx, r.Context())
		if err = s.noteFailure(r.Context(), keys); err != nil {
			s.fail(w, r, err)
			return
		}
		writeError(w, 400, "Приглашение истекло, использовано или врач недоступен")
		return
	}
	password, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	err = tx.QueryRow(r.Context(), `INSERT INTO users(email,name,role,password_hash) VALUES($1,$2,'patient',$3) ON CONFLICT(email) DO NOTHING RETURNING id`, email, name, string(password)).Scan(&id)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.fail(w, r, err)
			return
		}
		writeError(w, 409, "Этот адрес уже зарегистрирован. Обратитесь к врачу")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO care_links(doctor_id,patient_id) VALUES($1,$2)`, doctor, id); err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO consents(user_id,version) VALUES($1,'local-v1')`, id)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE invitations SET accepted_at=now() WHERE id=$1`, invitation)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO outbox_events(type,patient_id,aggregate_id) VALUES('invitation.accepted',$1,$2)`, id, invitation)
	}
	if err == nil {
		err = audit(r.Context(), tx, principal{User: User{ID: id}}, "invitation.accept", invitation, r.Context().Value(requestKey{}).(string))
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 201, map[string]bool{"ok": true})
}

// clientIP accepts forwarding headers only from explicitly configured proxy networks.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	remote, err := netip.ParseAddr(host)
	if err != nil {
		return "unknown"
	}
	for _, prefix := range s.config.TrustedProxyCIDRs {
		if prefix.Contains(remote) {
			if forwarded, parseErr := netip.ParseAddr(r.Header.Get("X-Real-IP")); parseErr == nil {
				return forwarded.String()
			}
		}
	}
	return remote.String()
}
func (s *Server) attemptKeys(r *http.Request, email, scope string) []string {
	keys := []string{hash(scope + ":ip:" + s.clientIP(r))}
	if email != "" {
		keys = append(keys, hash(scope+":account:"+email))
	}
	return keys
}
func (s *Server) attemptsLimited(ctx context.Context, keys []string, limit int) (bool, error) {
	var limited bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM login_attempts WHERE key=ANY($1::text[]) AND attempts>=$2 AND expires_at>now())`, keys, limit).Scan(&limited)
	return limited, err
}
func (s *Server) noteFailure(ctx context.Context, keys []string) error {
	_, err := s.db.Exec(ctx, `INSERT INTO login_attempts(key,attempts,expires_at)
 SELECT unnest($1::text[]),1,now()+interval '10 minutes'
 ON CONFLICT(key) DO UPDATE SET attempts=CASE WHEN login_attempts.expires_at<now() THEN 1 ELSE login_attempts.attempts+1 END,
 expires_at=CASE WHEN login_attempts.expires_at<now() THEN now()+interval '10 minutes' ELSE login_attempts.expires_at END`, keys)
	return err
}
