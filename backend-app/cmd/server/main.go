package main

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"psitech/internal/app"
	"psitech/internal/mailer"
	"psitech/internal/store"
	"strings"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata"
)

func secret(name string) (string, error) {
	path := os.Getenv(name + "_FILE")
	if path == "" {
		return "", fmt.Errorf("%s_FILE is required", name)
	}
	b, err := os.ReadFile(path)
	return strings.TrimSpace(string(b)), err
}
func run() error {
	if len(os.Args) > 1 && os.Args[1] == "gmail-authorize" {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		return mailer.Authorize(ctx, os.Getenv("GMAIL_CLIENT_FILE"), os.Getenv("GMAIL_CREDENTIALS_FILE"), os.Getenv("GMAIL_OAUTH_LISTEN"), os.Stdout)
	}
	if len(os.Args) > 1 && os.Args[1] == "hash-password" {
		raw, err := io.ReadAll(io.LimitReader(os.Stdin, 74))
		if err != nil {
			return err
		}
		password := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
		if len(password) < 12 || len(password) > 72 {
			return errors.New("password must contain 12 to 72 bytes")
		}
		digest, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		fmt.Println(string(digest))
		return nil
	}
	if len(os.Args) > 1 && os.Args[1] == "new-totp-secret" {
		raw := make([]byte, 20)
		if _, err := rand.Read(raw); err != nil {
			return err
		}
		fmt.Println(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw))
		return nil
	}

	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		client := http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get("http://127.0.0.1:8000/health/ready")
		if err != nil {
			return err
		}
		defer func() {
			if closeErr := resp.Body.Close(); closeErr != nil {
				slog.Debug("healthcheck response close failed")
			}
		}()
		if resp.StatusCode != 200 {
			return errors.New("not ready")
		}
		return nil
	}
	if len(os.Args) > 1 && os.Args[1] == "totp" {
		key, err := secret("DOCTOR_TOTP")
		if err != nil {
			return err
		}
		fmt.Println(app.TOTP(key, time.Now()))
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	connection := os.Getenv("DATABASE_URL")
	if connection == "" {
		password, err := secret("DB_PASSWORD")
		if err != nil {
			return err
		}
		host := os.Getenv("DB_HOST")
		if host == "" {
			host = "db:5432"
		}
		user := os.Getenv("DB_USER")
		if user == "" {
			user = "psitech"
		}
		connection = "postgres://" + url.QueryEscape(user) + ":" + url.QueryEscape(password) + "@" + host + "/psitech?sslmode=disable"
	}
	start, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var db *pgxpool.Pool
	var err error
	migrating := len(os.Args) > 1 && os.Args[1] == "migrate"
	if migrating || os.Getenv("MIGRATE_ON_START") != "false" {
		db, err = store.Open(start, connection)
	} else {
		db, err = store.OpenWithoutMigrate(start, connection)
	}
	if err != nil {
		return fmt.Errorf("database startup: %w", err)
	}
	defer db.Close()
	if migrating {
		if os.Getenv("APP_DB_PASSWORD_FILE") != "" {
			password, readErr := secret("APP_DB_PASSWORD")
			if readErr != nil {
				return readErr
			}
			if err = store.ProvisionRuntimeRole(start, db, password); err != nil {
				return err
			}
		}
		return nil
	}
	origins := strings.Split(os.Getenv("APP_ORIGINS"), ",")
	if len(origins) == 1 && origins[0] == "" {
		origins = []string{"http://localhost:8080", "http://127.0.0.1:8080"}
	}
	config := app.Config{Origins: origins, SecureCookies: os.Getenv("COOKIE_SECURE") == "true", OperatorEmail: os.Getenv("OPS_EMAIL"), SyntheticDeterioration: os.Getenv("SEED_DEMO") == "true"}
	if pubMedEmail := strings.TrimSpace(os.Getenv("PUBMED_EMAIL")); pubMedEmail != "" {
		pubMedTool := strings.TrimSpace(os.Getenv("PUBMED_TOOL"))
		if pubMedTool == "" {
			pubMedTool = "psitech"
		}
		pubMedAPIKey := ""
		if os.Getenv("PUBMED_API_KEY_FILE") != "" {
			pubMedAPIKey, err = secret("PUBMED_API_KEY")
			if err != nil {
				return err
			}
		}
		config.PubMed = &app.PubMedConfig{Tool: pubMedTool, Email: pubMedEmail, APIKey: pubMedAPIKey}
	}
	if os.Getenv("OPS_TOKEN_FILE") != "" {
		config.OpsToken, err = secret("OPS_TOKEN")
		if err != nil {
			return err
		}
		if len(config.OpsToken) < 32 {
			return errors.New("operator token requires 32 characters")
		}
	}
	for _, value := range strings.Split(os.Getenv("TRUSTED_PROXY_CIDRS"), ",") {
		if value != "" {
			prefix, parseErr := netip.ParsePrefix(strings.TrimSpace(value))
			if parseErr != nil {
				return errors.New("invalid trusted proxy network")
			}
			config.TrustedProxyCIDRs = append(config.TrustedProxyCIDRs, prefix)
		}
	}
	if os.Getenv("EMAIL_ENABLED") == "true" {
		encryption, readErr := secret("EMAIL_ENCRYPTION_KEY")
		if readErr != nil {
			return readErr
		}
		key, decodeErr := hex.DecodeString(encryption)
		if decodeErr != nil {
			return errors.New("invalid email encryption key")
		}
		credentials, readErr := mailer.LoadCredentials(os.Getenv("GMAIL_CREDENTIALS_FILE"))
		if readErr != nil {
			return readErr
		}
		sender, sendErr := mailer.New(mailer.Config{From: os.Getenv("GMAIL_FROM"), Credentials: credentials, Timeout: 10 * time.Second})
		if sendErr != nil {
			return sendErr
		}
		config.Email = &app.EmailConfig{Enabled: true, PublicURL: os.Getenv("PUBLIC_URL"), EncryptionKey: key, Gmail: sender}
		if err = app.ValidateEmailConfig(*config.Email); err != nil {
			return fmt.Errorf("email configuration: %w", err)
		}
	}
	if os.Getenv("AUDIO_URL") != "" {
		config.AudioURL = os.Getenv("AUDIO_URL")
		config.AudioDir = os.Getenv("AUDIO_DIR")
		config.AudioToken, err = secret("AUDIO_TOKEN")
		if err != nil {
			return err
		}
		if config.AudioDir == "" || config.AudioToken == "" {
			return errors.New("audio storage and service credential are required")
		}
		if err = os.MkdirAll(config.AudioDir, 0700); err != nil {
			return err
		}
	}
	if os.Getenv("VOICEFM_URL") != "" {
		config.VoiceFMURL = os.Getenv("VOICEFM_URL")
		config.VoiceFMMode = os.Getenv("VOICEFM_MODE")
		if config.VoiceFMMode == "" {
			config.VoiceFMMode = "mock"
		}
		config.VoiceFMToken, err = secret("VOICEFM_TOKEN")
		if err != nil {
			return err
		}
		if err = app.ValidateVoiceFMConfig(config.VoiceFMURL, config.VoiceFMToken, config.VoiceFMMode); err != nil {
			return err
		}
	}

	config.SupportChatMode = os.Getenv("SUPPORT_CHAT_MODE")
	config.SupportChatURL = os.Getenv("SUPPORT_CHAT_URL")
	config.SupportChatModel = os.Getenv("SUPPORT_CHAT_MODEL")
	if os.Getenv("SUPPORT_CHAT_API_KEY_FILE") != "" {
		config.SupportChatAPIKey, err = secret("SUPPORT_CHAT_API_KEY")
		if err != nil {
			return err
		}
	}
	if err = app.ValidateSupportChatConfig(config); err != nil {
		return err
	}
	config.ClinicalResearchURL = strings.TrimSpace(os.Getenv("CLINICAL_RESEARCH_URL"))
	if config.ClinicalResearchURL == "" {
		config.ClinicalResearchURL = "https://api.openai.com/v1"
	}
	config.ClinicalResearchModel = strings.TrimSpace(os.Getenv("CLINICAL_RESEARCH_MODEL"))
	if config.ClinicalResearchModel == "" {
		config.ClinicalResearchModel = "gpt-6-astra"
	}
	if os.Getenv("CLINICAL_RESEARCH_API_KEY_FILE") != "" {
		config.ClinicalResearchAPIKey, err = secret("CLINICAL_RESEARCH_API_KEY")
		if err != nil {
			return err
		}
	}
	config.ClinicalResearchMode = strings.TrimSpace(os.Getenv("CLINICAL_RESEARCH_MODE"))
	if config.ClinicalResearchMode == "" {
		config.ClinicalResearchMode = "openai"
	}
	if err = app.ValidateClinicalResearchConfig(config); err != nil {
		return err
	}

	api := app.New(db, config)
	if os.Getenv("SEED_DEMO") == "true" {
		password, readErr := secret("SEED_PASSWORD")
		if readErr != nil {
			return readErr
		}
		key, readErr := secret("DOCTOR_TOTP")
		if readErr != nil {
			return readErr
		}
		if err = api.Seed(start, password, key); err != nil {
			return fmt.Errorf("synthetic seed: %w", err)
		}
	}
	server := &http.Server{BaseContext: func(_ net.Listener) context.Context { return ctx }, Addr: ":8000", Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	var workers sync.WaitGroup
	workers.Add(5)
	go func() { defer workers.Done(); api.RunAudio(ctx) }()
	go func() { defer workers.Done(); api.RunBackground(ctx) }()
	go func() { defer workers.Done(); api.RunEmail(ctx) }()
	go func() { defer workers.Done(); api.RunVoiceFM(ctx) }()
	go func() { defer workers.Done(); api.RunClinicalEvidence(ctx) }()
	result := make(chan error, 1)
	go func() { slog.Info("api listening", "port", 8000); result <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
	case err = <-result:
		if !errors.Is(err, http.ErrServerClosed) {
			stop()
			workers.Wait()
			return err
		}
	}
	stop()
	shutdown, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	err = server.Shutdown(shutdown)
	workers.Wait()
	return err
}
func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error_type", fmt.Sprintf("%T", err))
		os.Exit(1)
	}
}
