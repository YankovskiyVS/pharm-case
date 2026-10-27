// Package app exposes the authenticated clinical application API.
package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// Config contains runtime security and local-deployment settings.
type Config struct {
	AudioDir               string
	AudioURL               string
	AudioToken             string
	VoiceFMURL             string
	VoiceFMToken           string
	VoiceFMMode            string
	SupportChatMode        string
	SupportChatURL         string
	SupportChatModel       string
	SupportChatAPIKey      string
	Origins                []string
	SecureCookies          bool
	Email                  *EmailConfig
	TrustedProxyCIDRs      []netip.Prefix
	OperatorEmail          string
	OpsToken               string
	PubMed                 *PubMedConfig
	ClinicalResearchURL    string
	ClinicalResearchModel  string
	ClinicalResearchAPIKey string
	ClinicalResearchMode   string
	SyntheticDeterioration bool
}

// Server owns handlers and a shared connection pool.
type Server struct {
	db           *pgxpool.Pool
	config       Config
	supportSlots chan struct{}
	pubmed       *PubMedClient
}

// User is the public account representation.
type User struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	Timezone string `json:"timezone"`
}
type principal struct {
	User
	CSRF    string
	Session string
}
type principalKey struct{}
type requestKey struct{}

// New creates an API server with explicit configuration.
func New(db *pgxpool.Pool, c Config) *Server {
	return &Server{db: db, config: c, supportSlots: make(chan struct{}, 4), pubmed: NewPubMedClient(c.PubMed)}
}

// Handler constructs routes and the security boundary.
func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	m.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		if err := s.db.Ping(r.Context()); err != nil {
			writeError(w, 503, "База данных недоступна")
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ready"})
	})
	m.Handle("POST /api/v1/patient/audio", s.auth("patient", http.HandlerFunc(s.uploadAudio)))
	m.Handle("GET /api/v1/patients/{id}/audio/{audio}", s.auth("", http.HandlerFunc(s.listAudio)))
	m.Handle("GET /api/v1/patients/{id}/audio", s.auth("", http.HandlerFunc(s.listAudio)))
	m.Handle("GET /api/v1/patients/{id}/audio/{audio}/file", s.auth("", http.HandlerFunc(s.audioFile)))
	m.Handle("POST /api/v1/patients/{id}/audio/{audio}/retry", s.auth("", http.HandlerFunc(s.retryAudio)))
	m.Handle("POST /api/v1/patients/{id}/audio/search", s.auth("doctor", http.HandlerFunc(s.searchAudio)))
	m.Handle("POST /api/v1/doctor/pubmed/search", s.auth("doctor", http.HandlerFunc(s.searchPubMed)))
	m.HandleFunc("POST /api/v1/auth/login", s.login)
	m.HandleFunc("POST /api/v1/auth/password/reset/request", s.requestPasswordReset)
	m.HandleFunc("POST /api/v1/auth/password/reset/confirm", s.confirmPasswordReset)
	m.Handle("POST /api/v1/auth/password/change", s.auth("", http.HandlerFunc(s.changePassword)))
	m.Handle("GET /api/v1/auth/sessions", s.auth("", http.HandlerFunc(s.sessions)))
	m.Handle("POST /api/v1/auth/sessions/revoke-others", s.auth("", http.HandlerFunc(s.revokeOtherSessions)))
	m.Handle("POST /api/v1/auth/sessions/{session}/revoke", s.auth("", http.HandlerFunc(s.revokeSession)))
	m.Handle("POST /api/v1/me/profile", s.auth("", http.HandlerFunc(s.profile)))
	m.Handle("GET /api/v1/alerts/{alert}", s.auth("", http.HandlerFunc(s.alertDetail)))
	m.Handle("GET /api/v1/patients/{id}/reviews/{review}", s.auth("", http.HandlerFunc(s.reviewDetail)))
	m.Handle("GET /api/v1/doctor/alerts", s.auth("doctor", http.HandlerFunc(s.doctorAlerts)))
	m.Handle("GET /api/v1/patients/{id}/diaries", s.auth("", http.HandlerFunc(s.patientDiaries)))
	m.Handle("GET /api/v1/patients/{id}/alerts", s.auth("", http.HandlerFunc(s.patientAlerts)))
	m.Handle("GET /api/v1/patients/{id}/reviews", s.auth("", http.HandlerFunc(s.patientReviews)))
	m.Handle("GET /api/v1/patients/{id}/export", s.auth("", http.HandlerFunc(s.exportPatient)))
	m.Handle("GET /api/v1/patients/{id}/schedules", s.auth("", http.HandlerFunc(s.schedules)))
	m.Handle("POST /api/v1/patients/{id}/schedules", s.auth("doctor", http.HandlerFunc(s.createSchedule)))
	m.Handle("POST /api/v1/patients/{id}/schedules/{schedule}/update", s.auth("doctor", http.HandlerFunc(s.updateSchedule)))
	m.Handle("POST /api/v1/patients/{id}/schedules/{schedule}/status", s.auth("doctor", http.HandlerFunc(s.scheduleStatus)))
	m.Handle("GET /api/v1/patients/{id}/schedules/{schedule}/history", s.auth("", http.HandlerFunc(s.scheduleHistory)))
	m.Handle("GET /api/v1/audio/status", s.auth("", http.HandlerFunc(s.audioStatus)))
	m.Handle("GET /api/v1/patients/{id}/audio/{audio}/runs", s.auth("", http.HandlerFunc(s.audioRuns)))
	m.Handle("GET /api/v1/patients/{id}/clinical-context", s.auth("doctor", http.HandlerFunc(s.clinicalContext)))
	m.Handle("POST /api/v1/patients/{id}/clinical-context", s.auth("doctor", http.HandlerFunc(s.saveClinicalContext)))
	m.Handle("GET /api/v1/patients/{id}/clinical-context/history", s.auth("doctor", http.HandlerFunc(s.clinicalContextHistory)))
	m.Handle("GET /api/v1/patients/{id}/clinical-evidence-reviews", s.auth("doctor", http.HandlerFunc(s.clinicalEvidenceReviews)))
	m.Handle("POST /api/v1/patients/{id}/clinical-evidence-reviews/{review}/retry", s.auth("doctor", http.HandlerFunc(s.retryClinicalEvidence)))
	m.Handle("GET /api/v1/patients/{id}/audio/{audio}/clinical-reports", s.auth("doctor", http.HandlerFunc(s.clinicalReports)))
	m.Handle("POST /api/v1/patients/{id}/audio/{audio}/clinical-reports", s.auth("doctor", http.HandlerFunc(s.createClinicalReport)))
	m.Handle("GET /api/v1/patients/{id}/audio/{audio}/clinical-reports/{report}", s.auth("doctor", http.HandlerFunc(s.clinicalReportDetail)))
	m.Handle("POST /api/v1/patients/{id}/audio/{audio}/clinical-reports/{report}/conclusions", s.auth("doctor", http.HandlerFunc(s.appendClinicalConclusion)))
	m.Handle("GET /api/v1/voicefm/status", s.auth("doctor", http.HandlerFunc(s.voiceFMStatus)))

	m.Handle("GET /api/v1/patient/support-chat", s.auth("patient", http.HandlerFunc(s.supportChatHistory)))
	m.Handle("POST /api/v1/patient/support-chat", s.auth("patient", http.HandlerFunc(s.sendSupportChat)))
	m.Handle("GET /api/v1/ops/status", s.ops(http.HandlerFunc(s.opsStatus)))
	m.Handle("POST /api/v1/ops/email/retry", s.ops(http.HandlerFunc(s.opsRetryEmail)))
	m.HandleFunc("POST /api/v1/auth/accept-invitation", s.acceptInvitation)
	m.Handle("POST /api/v1/auth/logout", s.auth("", http.HandlerFunc(s.logout)))
	m.Handle("GET /api/v1/me", s.auth("", http.HandlerFunc(s.me)))
	m.Handle("GET /api/v1/dashboard", s.auth("", http.HandlerFunc(s.dashboard)))
	m.Handle("GET /api/v1/doctor/patients", s.auth("doctor", http.HandlerFunc(s.patients)))
	m.Handle("POST /api/v1/doctor/invitations", s.auth("doctor", http.HandlerFunc(s.invite)))
	m.Handle("GET /api/v1/patients/{id}", s.auth("", http.HandlerFunc(s.patientDetail)))
	m.Handle("POST /api/v1/patient/diaries", s.auth("patient", http.HandlerFunc(s.submitDiary)))
	m.Handle("POST /api/v1/patient/risk-signals", s.auth("patient", http.HandlerFunc(s.riskSignal)))
	m.Handle("POST /api/v1/patients/{id}/assignments", s.auth("doctor", http.HandlerFunc(s.assign)))
	m.Handle("POST /api/v1/patients/{id}/reviews", s.auth("doctor", http.HandlerFunc(s.review)))
	m.Handle("POST /api/v1/alerts/{id}/actions", s.auth("doctor", http.HandlerFunc(s.alertAction)))
	m.Handle("GET /api/v1/patients/{id}/messages", s.auth("", http.HandlerFunc(s.messages)))
	m.Handle("POST /api/v1/patients/{id}/messages/read", s.auth("", http.HandlerFunc(s.readMessages)))
	m.Handle("POST /api/v1/patients/{id}/messages", s.auth("", http.HandlerFunc(s.sendMessage)))
	m.Handle("GET /api/v1/notifications", s.auth("", http.HandlerFunc(s.notifications)))
	m.Handle("POST /api/v1/notifications/read", s.auth("", http.HandlerFunc(s.readNotifications)))
	m.Handle("GET /api/v1/patients/{id}/diaries/{entry}/revisions", s.auth("", http.HandlerFunc(s.revisions)))
	m.Handle("GET /api/v1/events", s.auth("", http.HandlerFunc(s.events)))
	return s.middleware(m)
}
func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := token()
		w.Header().Set("X-Request-ID", requestID)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		r = r.WithContext(context.WithValue(r.Context(), requestKey{}, requestID))
		if r.Method != "GET" && r.Method != "HEAD" {
			valid := false
			for _, origin := range s.config.Origins {
				if r.Header.Get("Origin") == origin {
					valid = true
				}
			}
			if !valid {
				writeError(w, 403, "Недопустимый источник запроса")
				return
			}
			audioUpload := r.Method == "POST" && r.URL.Path == "/api/v1/patient/audio" && strings.HasPrefix(r.Header.Get("Content-Type"), "audio/")
			if !audioUpload && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				writeError(w, 415, "Ожидается JSON")
				return
			}
		}
		defer func() {
			if recover() != nil {
				slog.Error("request panic", "request_id", requestID)
				writeError(w, 500, "Внутренняя ошибка")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
func (s *Server) auth(role string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("psitech_session")
		if err != nil {
			writeError(w, 401, "Войдите в аккаунт")
			return
		}
		p := principal{Session: hash(cookie.Value)}
		err = s.db.QueryRow(r.Context(), `SELECT u.id,u.email,u.name,u.role,u.timezone,s.csrf FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires_at>now() AND u.active AND s.auth_version=u.auth_version AND s.auth_role=u.role`, p.Session).Scan(&p.ID, &p.Email, &p.Name, &p.Role, &p.Timezone, &p.CSRF)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				s.fail(w, r, err)
				return
			}
			writeError(w, 401, "Сессия завершена. Войдите снова")
			return
		}
		if role != "" && p.Role != role {
			writeError(w, 403, "Недостаточно прав")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("X-CSRF-Token") != p.CSRF {
			writeError(w, 403, "Обновите страницу и повторите действие")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}
func current(r *http.Request) principal { return r.Context().Value(principalKey{}).(principal) }
func (s *Server) authorized(r *http.Request, patient string) bool {
	if !uuid(patient) {
		return false
	}
	p := current(r)
	if p.Role == "patient" {
		return p.ID == patient
	}
	var ok bool
	if err := s.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM care_links c JOIN users d ON d.id=c.doctor_id WHERE c.doctor_id=$1 AND c.patient_id=$2 AND c.active AND d.active AND d.role='doctor')`, p.ID, patient).Scan(&ok); err != nil {
		return false
	}
	return ok
}
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("request failed", "error_type", fmt.Sprintf("%T", err), "request_id", r.Context().Value(requestKey{}))
	writeError(w, 500, "Не удалось выполнить действие. Повторите попытку")
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Debug("response write failed")
	}
}
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		writeError(w, 400, "Проверьте формат заполненных данных")
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		writeError(w, 400, "Ожидается один JSON объект")
		return false
	}
	return true
}
func token() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func hash(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
func uuid(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}
func audit(ctx context.Context, tx pgx.Tx, p principal, action, target, request string) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit_events(actor_id,action,target_id,request_id) VALUES($1,$2,$3,$4)`, p.ID, action, target, request)
	return err
}
func (s *Server) recordRead(r *http.Request, action, target string) error {
	_, err := s.db.Exec(r.Context(), `INSERT INTO audit_events(actor_id,action,target_id,request_id) VALUES($1,$2,$3,$4)`, current(r).ID, action, target, r.Context().Value(requestKey{}))
	return err
}
func rollback(tx pgx.Tx, ctx context.Context) {
	if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		slog.Debug("transaction rollback failed")
	}
}
func date(s string) bool { _, err := time.Parse("2006-01-02", s); return err == nil }
