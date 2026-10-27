package store

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ProvisionRuntimeRole prepares the fixed non-DDL role used by the local/pilot API.
// Only the separate migration process receives the administrator credentials.
func ProvisionRuntimeRole(ctx context.Context, pool *pgxpool.Pool, password string) error {
	if len(password) < 24 {
		return errors.New("runtime database password requires at least 24 bytes")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7310627)`); err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='psitech_app')`).Scan(&exists); err != nil {
		return err
	}
	command := "ALTER ROLE"
	if !exists {
		command = "CREATE ROLE"
	}
	var statement string
	if err = tx.QueryRow(ctx, `SELECT $1||' psitech_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD '||quote_literal($2)`, command, password).Scan(&statement); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, statement); err != nil {
		return err
	}
	var schema, database string
	if err = tx.QueryRow(ctx, `SELECT current_schema(),current_database()`).Scan(&schema, &database); err != nil {
		return err
	}
	schemaID := pgx.Identifier{schema}.Sanitize()
	databaseID := pgx.Identifier{database}.Sanitize()
	for _, query := range []string{
		"REVOKE CREATE ON SCHEMA " + schemaID + " FROM PUBLIC",
		"REVOKE CREATE ON SCHEMA " + schemaID + " FROM psitech_app",
		"REVOKE TEMPORARY ON DATABASE " + databaseID + " FROM PUBLIC",
		"GRANT CONNECT ON DATABASE " + databaseID + " TO psitech_app",
		"GRANT USAGE ON SCHEMA " + schemaID + " TO psitech_app",
		"GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA " + schemaID + " TO psitech_app",
		"GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA " + schemaID + " TO psitech_app",
	} {
		if _, err = tx.Exec(ctx, query); err != nil {
			return err
		}
	}
	for _, table := range []string{"diary_entries", "current_entries", "risk_signals", "consents", "reviews", "alert_actions", "assignment_history", "audio_recordings", "audio_chunks", "outbox_events", "audit_events", "event_deliveries", "weekly_review_reminders", "reminder_runs", "notifications", "message_reads", "notification_reads", "clinical_context_versions", "clinical_audio_reports", "voicefm_results", "clinical_report_conclusions", "support_chat_turns"} {
		if _, err = tx.Exec(ctx, "REVOKE UPDATE,DELETE ON "+pgx.Identifier{schema, table}.Sanitize()+" FROM psitech_app"); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, "REVOKE ALL ON "+pgx.Identifier{schema, "schema_migrations"}.Sanitize()+" FROM psitech_app"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
