/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	"github.com/pressly/goose/v3"

	// Register pgx as an sql.DB driver. Goose talks to sql.DB; the runtime
	// store uses pgxpool directly (see postgres.go), but migrations are
	// one-shot at startup so the extra driver dep is fine.
	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// advisoryLockKey is a stable 64-bit int used with pg_advisory_lock so that
// concurrent operator replicas (e.g. rolling update) serialize their
// migration attempts instead of racing on CREATE TABLE. The value is
// arbitrary — the only property that matters is stability across builds.
// crc32("kubetest-alt.migrations") + 0x_00000001 padded to int64.
const advisoryLockKey int64 = 0x_6b75_6265_7465_7374

// ApplyMigrations runs `goose up` against the given DSN using the embedded
// migrations. Wraps the whole run in a session-level pg_advisory_lock so
// only one operator replica migrates at a time; the others wait on the
// lock, then re-check (no-op if the first replica already brought us to
// head).
//
// dsn accepts any format pgx/stdlib understands
// (postgres://user:pass@host/db?sslmode=disable, or key=value pairs).
func ApplyMigrations(ctx context.Context, dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("store: open pg: %w", err)
	}
	defer func() { _ = db.Close() }()

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("store: ping pg: %w", err)
	}

	// Acquire the advisory lock on a dedicated connection. pg_advisory_lock
	// is session-scoped, so the lock lives on THIS conn only; when we
	// release it (or the conn closes), followers wake up.
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("store: acquire migration conn: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, advisoryLockKey); err != nil {
		return fmt.Errorf("store: pg_advisory_lock: %w", err)
	}
	defer func() {
		// Best-effort unlock — the conn.Close above releases the lock
		// anyway, but the explicit UNLOCK plays nicer with connection
		// pools that recycle instead of close.
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, advisoryLockKey)
	}()

	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(gooseSilentLogger{}) // controller-runtime handles logs; goose noise is unhelpful
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("store: goose dialect: %w", err)
	}

	if err := goose.UpContext(ctx, db, "migrations"); err != nil {
		return fmt.Errorf("store: goose up: %w", err)
	}
	return nil
}

// gooseSilentLogger discards goose's chatty per-migration output. Real errors
// still surface via the UpContext return.
type gooseSilentLogger struct{}

func (gooseSilentLogger) Fatalf(format string, v ...any) {}
func (gooseSilentLogger) Printf(format string, v ...any) {}
