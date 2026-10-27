// Package store owns PostgreSQL initialization and versioned schema migrations.
package store

import (
	"context"
	"embed"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Open connects, validates availability and applies embedded migrations under a lock.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) { return open(ctx, url, true) }

// OpenWithoutMigrate connects with runtime credentials without issuing schema changes.
func OpenWithoutMigrate(ctx context.Context, url string) (*pgxpool.Pool, error) {
	return open(ctx, url, false)
}
func open(ctx context.Context, url string, apply bool) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("database configuration: %w", err)
	}
	config.MaxConns = 12
	config.ConnConfig.RuntimeParams["statement_timeout"] = "10000"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if apply {
		if err = migrate(ctx, pool); err != nil {
			pool.Close()
			return nil, err
		}
	}
	return pool, nil
}
func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(7310625)"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations(version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())"); err != nil {
		return err
	}
	files, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	for _, file := range files {
		var exists bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)", file.Name()).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		body, readErr := migrations.ReadFile("migrations/" + file.Name())
		if readErr != nil {
			return readErr
		}
		if _, err = tx.Exec(ctx, string(body)); err != nil {
			return fmt.Errorf("migration %s: %w", file.Name(), err)
		}
		if _, err = tx.Exec(ctx, "INSERT INTO schema_migrations(version) VALUES($1)", file.Name()); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
