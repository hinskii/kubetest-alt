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
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// AuditEntry is one user action recorded by the API server.
type AuditEntry struct {
	ID        int64     `json:"id"`
	At        time.Time `json:"at"`
	Actor     string    `json:"actor,omitempty"`
	Action    string    `json:"action"`
	Namespace string    `json:"namespace,omitempty"`
	// Target is the object acted on: a run name/UID or a Test name.
	Target  string            `json:"target,omitempty"`
	Details map[string]string `json:"details,omitempty"`
}

// AuditFilter narrows ListAudit. Zero fields don't filter.
type AuditFilter struct {
	Namespace string
	Actor     string
	Action    string
	// BeforeID pages backwards: only entries with ID < BeforeID.
	BeforeID int64
}

// AppendAudit records e. At defaults to the database's now(); ID is
// assigned by the database.
func (p *Postgres) AppendAudit(ctx context.Context, e AuditEntry) error {
	var at any
	if !e.At.IsZero() {
		at = e.At
	}
	_, err := p.pool.Exec(ctx, `
		INSERT INTO audit_log (at, actor, action, namespace, target, details)
		VALUES (COALESCE($1, now()), $2, $3, $4, $5, $6)`,
		at, e.Actor, e.Action, e.Namespace, e.Target, jsonbOrNil(e.Details))
	if err != nil {
		return fmt.Errorf("store: append audit %s: %w", e.Action, err)
	}
	return nil
}

// ListAudit returns entries newest first (by ID). limit follows the
// DefaultPageLimit / MaxPageLimit rules of List.
func (p *Postgres) ListAudit(ctx context.Context, f AuditFilter, limit int) ([]AuditEntry, error) {
	var (
		where []string
		args  []any
	)
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if f.Namespace != "" {
		add("namespace = $%d", f.Namespace)
	}
	if f.Actor != "" {
		add("actor = $%d", f.Actor)
	}
	if f.Action != "" {
		add("action = $%d", f.Action)
	}
	if f.BeforeID > 0 {
		add("id < $%d", f.BeforeID)
	}
	q := `SELECT id, at, actor, action, namespace, target, details FROM audit_log`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += fmt.Sprintf(" ORDER BY id DESC LIMIT %d", clampLimit(limit))

	rows, err := p.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list audit: %w", err)
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var (
			e       AuditEntry
			details []byte
		)
		if err := rows.Scan(&e.ID, &e.At, &e.Actor, &e.Action, &e.Namespace, &e.Target, &details); err != nil {
			return nil, err
		}
		e.At = e.At.UTC()
		if len(details) > 0 {
			if err := json.Unmarshal(details, &e.Details); err != nil {
				return nil, err
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
