package app

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"psitech/internal/mailer"
)

// EmailConfig configures the secondary notification channel and encrypted invitation payloads.
type EmailConfig struct {
	Enabled       bool
	Gmail         *mailer.Client
	PublicURL     string
	EncryptionKey []byte
}

// ValidateEmailConfig rejects unsafe public URLs and incomplete Gmail/encryption configuration.
func ValidateEmailConfig(c EmailConfig) error {
	if !c.Enabled {
		return nil
	}
	if c.Gmail == nil {
		return errors.New("gmail OAuth sender is required")
	}
	if len(c.EncryptionKey) != 32 {
		return errors.New("email encryption key must contain exactly 32 bytes")
	}
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("email public URL must be an origin without credentials, path or query")
	}
	local := u.Hostname() == "localhost"
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		local = ip.IsLoopback()
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !local) {
		return errors.New("email public URL requires HTTPS except on localhost")
	}
	return nil
}

func sealInvitation(key []byte, invitation, rawToken string) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, []byte(rawToken), []byte(invitation)), nil
}
func openInvitation(key []byte, invitation string, encrypted []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(encrypted) < gcm.NonceSize() {
		return "", errors.New("invalid encrypted invitation")
	}
	plain, err := gcm.Open(nil, encrypted[:gcm.NonceSize()], encrypted[gcm.NonceSize():], []byte(invitation))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// enqueueInvitationEmail participates in the caller's invitation transaction.
func (s *Server) enqueueInvitationEmail(ctx context.Context, tx pgx.Tx, doctorID, email, rawToken, invitationID string) error {
	c := s.config.Email
	if c == nil || !c.Enabled {
		return nil
	}
	if err := ValidateEmailConfig(*c); err != nil {
		return err
	}
	// The queue never stores the bearer token or registration URL in plaintext.
	encrypted, err := sealInvitation(c.EncryptionKey, invitationID, rawToken)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO email_jobs(source_type,source_id,recipient_key,recipient_email,invitation_id,encrypted_payload,priority)
 SELECT 'invitation',$1,$2,$3,$1,$4,50 FROM users WHERE id=$5 AND role='doctor' AND active
 ON CONFLICT(source_type,source_id,recipient_key) DO NOTHING`, invitationID, hash(strings.ToLower(email)), email, encrypted, doctorID)
	return err
}

// RunEmail independently drains durable jobs so a slow Gmail provider cannot delay in-app alerts.
func (s *Server) RunEmail(ctx context.Context) {
	c := s.config.Email
	if c == nil || !c.Enabled {
		return
	}
	timer := time.NewTicker(250 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := s.processEmail(ctx); err != nil && ctx.Err() == nil {
				slog.Error("email delivery attempt failed", "error_type", fmt.Sprintf("%T", err))
			}
		}
	}
}

type emailJob struct {
	ID             string
	SourceType     string
	SourceID       string
	RecipientID    *string
	RecipientEmail string
	NotificationID *string
	InvitationID   *string
	Encrypted      []byte
	Attempts       int
	Lease          string
}

func (s *Server) processEmail(ctx context.Context) error {
	c := s.config.Email
	if c == nil || !c.Enabled {
		return nil
	}
	if err := ValidateEmailConfig(*c); err != nil {
		return err
	}
	// Each notification has an independent email delivery. The migration cutoff avoids
	// unexpectedly emailing old historical events when this channel is first enabled.
	_, err := s.db.Exec(ctx, `INSERT INTO email_jobs(source_type,source_id,recipient_key,recipient_id,recipient_email,notification_id,priority)
 SELECT 'notification',n.id,n.recipient_id::text,n.recipient_id,u.email,n.id,CASE WHEN n.type IN ('risk.raised','deterioration.raised') THEN 100 ELSE 10 END
 FROM notifications n JOIN users u ON u.id=n.recipient_id
 WHERE n.seq>(SELECT notification_start_seq FROM email_delivery_state WHERE id)
 AND NOT EXISTS(SELECT 1 FROM email_jobs j WHERE j.source_type='notification' AND j.source_id=n.id AND j.recipient_key=n.recipient_id::text)
 ORDER BY n.seq LIMIT 250 ON CONFLICT(source_type,source_id,recipient_key) DO NOTHING`)
	if err != nil {
		return err
	}
	var job emailJob
	err = s.db.QueryRow(ctx, `WITH claim AS (
 SELECT id FROM email_jobs WHERE (status='pending' AND available_at<=now()) OR (status='sending' AND leased_until<now())
 ORDER BY priority DESC,available_at,created_at LIMIT 1 FOR UPDATE SKIP LOCKED
 ) UPDATE email_jobs j SET status='sending',attempts=attempts+1,lease_token=gen_random_uuid(),leased_until=now()+interval '2 minutes'
 FROM claim WHERE j.id=claim.id RETURNING j.id,j.source_type,j.source_id,j.recipient_id,j.recipient_email,j.notification_id,j.invitation_id,j.encrypted_payload,j.attempts,j.lease_token`).Scan(&job.ID, &job.SourceType, &job.SourceID, &job.RecipientID, &job.RecipientEmail, &job.NotificationID, &job.InvitationID, &job.Encrypted, &job.Attempts, &job.Lease)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	message, allowed, err := s.emailForJob(ctx, *c, job)
	if err != nil {
		return s.retryEmail(ctx, job, "payload_unavailable", err)
	}
	if !allowed {
		return s.finishEmail(ctx, job, "cancelled")
	}
	if err = c.Gmail.Send(ctx, message); err != nil {
		return s.retryEmail(ctx, job, mailer.ErrorCode(err), err)
	}
	return s.finishEmail(ctx, job, "sent")
}

func (s *Server) emailForJob(ctx context.Context, c EmailConfig, job emailJob) (mailer.Message, bool, error) {
	message := mailer.Message{MessageID: job.ID + "@psitech", To: job.RecipientEmail}
	origin := strings.TrimRight(c.PublicURL, "/")
	if job.SourceType == "password_reset" {
		return s.passwordResetEmail(ctx, c, job)
	}
	if job.SourceType == "operational" {
		message.Subject = "Проверка доставки в ПсиTech"
		message.Text = "В очереди обслуживания ПсиTech есть задача, требующая проверки оператором. Откройте защищённый операторский статус и проверьте очередь. Медицинские данные и идентификаторы пациентов в письмо не включены.\n"
		return message, true, nil
	}
	if job.SourceType == "invitation" {
		var address, expectedHash string
		var allowed bool
		err := s.db.QueryRow(ctx, `SELECT i.email,i.token_hash,(i.accepted_at IS NULL AND i.expires_at>now() AND u.active AND u.role='doctor') FROM invitations i JOIN users u ON u.id=i.doctor_id WHERE i.id=$1`, job.InvitationID).Scan(&address, &expectedHash, &allowed)
		if errors.Is(err, pgx.ErrNoRows) {
			return message, false, nil
		}
		if err != nil {
			return message, false, err
		}
		if !allowed {
			return message, false, nil
		}
		raw, err := openInvitation(c.EncryptionKey, job.SourceID, job.Encrypted)
		if err != nil {
			return message, false, err
		}
		if hash(raw) != expectedHash {
			return message, false, errors.New("invitation payload no longer matches")
		}
		message.To = address
		message.Subject = "Приглашение в ПсиTech"
		message.Text = "Врач пригласил вас в личный кабинет ПсиTech. Для регистрации задайте пароль по ссылке:\n\n" + origin + "/patient/#invite=" + url.QueryEscape(raw) + "\n\nСсылка одноразовая и действует 7 дней с момента создания. Если вы не ожидали приглашения, не переходите по ссылке.\n"
		return message, true, nil
	}
	var recipient, role, kind string
	var allowed bool
	err := s.db.QueryRow(ctx, `SELECT u.email,u.role,n.type,
 (u.active AND p.active AND (u.id=n.patient_id OR (u.role='doctor' AND EXISTS(SELECT 1 FROM care_links c WHERE c.patient_id=n.patient_id AND c.doctor_id=u.id AND c.active)))
 AND (n.type!='message.sent' OR EXISTS(SELECT 1 FROM outbox_events e JOIN messages m ON m.id=e.aggregate_id JOIN care_links c ON c.id=m.care_link_id WHERE e.id=n.event_id AND c.active AND m.sender_id!=u.id AND (c.doctor_id=u.id OR c.patient_id=u.id))))
 FROM notifications n JOIN users u ON u.id=n.recipient_id JOIN users p ON p.id=n.patient_id WHERE n.id=$1 AND n.recipient_id=$2`, job.NotificationID, job.RecipientID).Scan(&recipient, &role, &kind, &allowed)
	if errors.Is(err, pgx.ErrNoRows) {
		return message, false, nil
	}
	if err != nil {
		return message, false, err
	}
	if !allowed {
		return message, false, nil
	}
	message.To = recipient
	labels := map[string]string{"risk.raised": "Требуется внимание в ПсиTech", "deterioration.raised": "Изменение динамики в ПсиTech", "diary.submitted": "Новая запись в ПсиTech", "message.sent": "Новое сообщение в ПсиTech", "reminder.due": "Напоминание ПсиTech", "assignment.changed": "Расписание в ПсиTech обновлено", "review.recorded": "Обновление наблюдения в ПсиTech", "alert.updated": "Статус уведомления в ПсиTech обновлён", "invitation.accepted": "Регистрация в ПсиTech завершена", "review.overdue": "Напоминание об осмотре профиля в ПсиTech"}
	message.Subject = labels[kind]
	if message.Subject == "" {
		message.Subject = "Новое уведомление в ПсиTech"
	}
	portal := "/patient/"
	if role == "doctor" {
		portal = "/doctor/"
	}
	message.Text = "В вашем кабинете ПсиTech появилось новое уведомление.\n\nОткройте кабинет, чтобы ознакомиться с подробностями:\n" + origin + portal + "\n\nИз соображений конфиденциальности медицинские данные и текст переписки не включены в письмо.\nПрочтение письма не подтверждает ознакомление с сигналом в приложении.\n"
	return message, true, nil
}
func (s *Server) finishEmail(ctx context.Context, job emailJob, status string) error {
	_, err := s.db.Exec(ctx, `UPDATE email_jobs SET status=$3,accepted_at=CASE WHEN $3='sent' THEN now() ELSE accepted_at END,encrypted_payload=NULL,leased_until=NULL,lease_token=NULL,last_error_code=NULL WHERE id=$1 AND lease_token=$2`, job.ID, job.Lease, status)
	if err == nil {
		_, err = s.db.Exec(ctx, `UPDATE operational_exceptions SET status='resolved',resolved_at=now() WHERE kind='email_delivery' AND aggregate_id=$1 AND status='open'`, job.ID)
	}
	return err
}
func (s *Server) retryEmail(ctx context.Context, job emailJob, code string, cause error) error {
	attempts := job.Attempts
	if attempts > 8 {
		attempts = 8
	}
	delay := time.Second * time.Duration(1<<attempts)
	// Never persist provider error strings: provider errors can contain addresses or credentials.
	_, err := s.db.Exec(ctx, `UPDATE email_jobs SET status='pending',available_at=now()+$3::interval,leased_until=NULL,lease_token=NULL,last_error_code=$4 WHERE id=$1 AND lease_token=$2`, job.ID, job.Lease, fmt.Sprintf("%f seconds", delay.Seconds()), code)
	if err != nil {
		return err
	}
	if job.Attempts >= 3 && job.SourceType != "operational" {
		if noteErr := s.noteOperational(ctx, "email_delivery", job.ID, code); noteErr != nil {
			return noteErr
		}
	}
	return fmt.Errorf("email attempt deferred: %w", cause)
}

// EmailQueueStats exposes aggregate delivery health without recipient addresses or payloads.
func (s *Server) EmailQueueStats(ctx context.Context) (json.RawMessage, error) {
	return s.jsonQuery(ctx, `SELECT jsonb_build_object('pending',count(*) FILTER(WHERE status='pending'),'sending',count(*) FILTER(WHERE status='sending'),'accepted',count(*) FILTER(WHERE status='sent'),'cancelled',count(*) FILTER(WHERE status='cancelled'),'retrying',count(*) FILTER(WHERE status='pending' AND attempts>0)) FROM email_jobs`)
}

func (s *Server) passwordResetEmail(ctx context.Context, c EmailConfig, job emailJob) (mailer.Message, bool, error) {
	message := mailer.Message{MessageID: job.ID + "@psitech", To: job.RecipientEmail}
	var address, role, expected string
	var allowed bool
	err := s.db.QueryRow(ctx, `SELECT u.email,u.role,p.token_hash,(u.active AND p.used_at IS NULL AND p.expires_at>now()) FROM password_resets p JOIN users u ON u.id=p.user_id WHERE p.id=$1`, job.SourceID).Scan(&address, &role, &expected, &allowed)
	if errors.Is(err, pgx.ErrNoRows) {
		return message, false, nil
	}
	if err != nil {
		return message, false, err
	}
	if !allowed {
		return message, false, nil
	}
	raw, err := openInvitation(c.EncryptionKey, job.SourceID, job.Encrypted)
	if err != nil {
		return message, false, err
	}
	if hash(raw) != expected {
		return message, false, errors.New("reset payload unavailable")
	}
	portal := "/patient/"
	if role == "doctor" {
		portal = "/doctor/"
	}
	message.To = address
	message.Subject = "Восстановление доступа к ПсиTech"
	message.Text = "Для задания нового пароля откройте одноразовую ссылку в течение 30 минут:\n\n" + strings.TrimRight(c.PublicURL, "/") + portal + "#reset=" + url.QueryEscape(raw) + "\n\nЕсли вы не запрашивали восстановление, не открывайте ссылку. Двухфакторная защита врача остаётся включённой. После смены пароля старые сессии завершаются.\n"
	return message, true, nil
}
