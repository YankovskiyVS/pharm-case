package app

import (
	"errors"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"strings"
	"time"
)

func validPassword(value string) bool { return len(value) >= 12 && len(value) <= 72 }
func (s *Server) profile(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     *string `json:"name"`
		Timezone *string `json:"timezone"`
	}
	if !decode(w, r, &in) {
		return
	}
	p := current(r)
	name, zone := p.Name, p.Timezone
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
	}
	if in.Timezone != nil {
		zone = *in.Timezone
	}
	if len([]rune(name)) < 2 || len([]rune(name)) > 120 {
		writeError(w, 400, "Имя должно содержать 2–120 символов")
		return
	}
	if _, err := time.LoadLocation(zone); err != nil || zone == "Local" || zone == "" {
		writeError(w, 400, "Укажите часовой пояс IANA")
		return
	}
	var supported bool
	if err := s.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM pg_timezone_names WHERE name=$1)`, zone).Scan(&supported); err != nil {
		s.fail(w, r, err)
		return
	}
	if !supported {
		writeError(w, 400, "Часовой пояс не поддерживается")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rollback(tx, r.Context())
	_, err = tx.Exec(r.Context(), `UPDATE users SET name=$2,timezone=$3 WHERE id=$1`, p.ID, name, zone)
	if err == nil {
		err = audit(r.Context(), tx, p, "profile.update", p.ID, r.Context().Value(requestKey{}).(string))
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p.Name = name
	p.Timezone = zone
	writeJSON(w, 200, p.User)
}
func (s *Server) sessions(w http.ResponseWriter, r *http.Request) {
	data, err := s.jsonQuery(r.Context(), `SELECT jsonb_build_object('items',coalesce(jsonb_agg(jsonb_build_object('id',id,'created_at',created_at,'expires_at',expires_at,'current',token_hash=$2) ORDER BY created_at DESC),'[]'::jsonb)) FROM sessions WHERE user_id=$1 AND expires_at>now() AND auth_version=(SELECT auth_version FROM users WHERE id=$1)`, current(r).ID, current(r).Session)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, data)
}
func (s *Server) revokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	_, err := s.db.Exec(r.Context(), `DELETE FROM sessions WHERE user_id=$1 AND token_hash!=$2`, current(r).ID, current(r).Session)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) revokeSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("session")
	if !uuid(id) {
		writeError(w, 400, "Некорректная сессия")
		return
	}
	tag, err := s.db.Exec(r.Context(), `DELETE FROM sessions WHERE id=$1 AND user_id=$2`, id, current(r).ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, 404, "Сессия не найдена")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Current string `json:"current_password"`
		Next    string `json:"new_password"`
		Code    string `json:"code"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Current) > 72 || !validPassword(in.Next) {
		writeError(w, 400, "Новый пароль должен содержать 12–72 байта")
		return
	}
	keys := s.attemptKeys(r, current(r).ID, "password_change")
	limited, err := s.attemptsLimited(r.Context(), keys, 10)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if limited {
		writeError(w, 429, "Повторите попытку позже")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rollback(tx, r.Context())
	var digest, secret string
	err = tx.QueryRow(r.Context(), `SELECT password_hash,totp_secret FROM users WHERE id=$1 FOR UPDATE`, current(r).ID).Scan(&digest, &secret)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(digest), []byte(in.Current)) != nil || (current(r).Role == "doctor" && !validTOTP(secret, in.Code)) {
		rollback(tx, r.Context())
		if err = s.noteFailure(r.Context(), keys); err != nil {
			s.fail(w, r, err)
			return
		}
		writeError(w, 400, "Проверьте текущий пароль и код аутентификатора")
		return
	}
	encrypted, err := bcrypt.GenerateFromPassword([]byte(in.Next), bcrypt.DefaultCost)
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE users SET password_hash=$2 WHERE id=$1`, current(r).ID, string(encrypted))
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM sessions WHERE user_id=$1`, current(r).ID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE password_resets SET used_at=now() WHERE user_id=$1 AND used_at IS NULL`, current(r).ID)
	}
	if err == nil {
		err = audit(r.Context(), tx, current(r), "password.change", current(r).ID, r.Context().Value(requestKey{}).(string))
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) requestPasswordReset(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email string `json:"email"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	if len(in.Email) > 254 {
		writeError(w, 400, "Проверьте адрес")
		return
	}
	keys := s.attemptKeys(r, in.Email, "password_reset")
	limited, err := s.attemptsLimited(r.Context(), keys, 5)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if s.config.Email == nil || !s.config.Email.Enabled {
		writeError(w, 503, "Почтовое восстановление доступа не настроено")
		return
	}
	if limited {
		writeJSON(w, 202, map[string]bool{"ok": true})
		return
	}
	if err = s.noteFailure(r.Context(), keys); err != nil {
		s.fail(w, r, err)
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rollback(tx, r.Context())
	var user string
	err = tx.QueryRow(r.Context(), `SELECT id FROM users WHERE email=$1 AND active FOR UPDATE`, in.Email).Scan(&user)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, 202, map[string]bool{"ok": true})
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	raw := token()
	var reset string
	_, err = tx.Exec(r.Context(), `UPDATE password_resets SET used_at=now() WHERE user_id=$1 AND used_at IS NULL`, user)
	if err == nil {
		err = tx.QueryRow(r.Context(), `INSERT INTO password_resets(user_id,token_hash,expires_at) VALUES($1,$2,now()+interval '30 minutes') RETURNING id`, user, hash(raw)).Scan(&reset)
	}
	var sealed []byte
	if err == nil {
		sealed, err = sealInvitation(s.config.Email.EncryptionKey, reset, raw)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO email_jobs(source_type,source_id,recipient_key,recipient_id,recipient_email,password_reset_id,encrypted_payload,priority) VALUES('password_reset',$1,$2,$3,$4,$1,$5,75)`, reset, user, user, in.Email, sealed)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 202, map[string]bool{"ok": true})
}
func (s *Server) confirmPasswordReset(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token    string `json:"token"`
		Password string `json:"new_password"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Token) != 64 || !validPassword(in.Password) {
		writeError(w, 400, "Проверьте ссылку и пароль (12–72 байта)")
		return
	}
	keys := s.attemptKeys(r, "", "reset_confirm")
	limited, err := s.attemptsLimited(r.Context(), keys, 20)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if limited {
		writeError(w, 429, "Повторите позже")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rollback(tx, r.Context())
	var user, reset string
	err = tx.QueryRow(r.Context(), `SELECT p.user_id,p.id FROM password_resets p JOIN users u ON u.id=p.user_id WHERE p.token_hash=$1 AND p.used_at IS NULL AND p.expires_at>now() AND u.active`, hash(in.Token)).Scan(&user, &reset)
	if err == nil {
		_, err = tx.Exec(r.Context(), `SELECT id FROM users WHERE id=$1 FOR UPDATE`, user)
		if err == nil {
			err = tx.QueryRow(r.Context(), `SELECT id FROM password_resets WHERE id=$1 AND user_id=$2 AND used_at IS NULL AND expires_at>now() FOR UPDATE`, reset, user).Scan(&reset)
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		rollback(tx, r.Context())
		if err = s.noteFailure(r.Context(), keys); err != nil {
			s.fail(w, r, err)
			return
		}
		writeError(w, 400, "Ссылка истекла или уже использована")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	encrypted, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE users SET password_hash=$2 WHERE id=$1`, user, string(encrypted))
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE password_resets SET used_at=now() WHERE user_id=$1 AND used_at IS NULL`, user)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM sessions WHERE user_id=$1`, user)
	}
	if err == nil {
		err = audit(r.Context(), tx, principal{User: User{ID: user}}, "password.reset", reset, r.Context().Value(requestKey{}).(string))
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
