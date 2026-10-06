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

// Package auth resolves the end user and their role. Authentication is
// oauth2-proxy's job: it sits in front of Control Center and sets
// X-Auth-Request-Email. Control Center must only be reachable through it.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/hinskii/kubetest-alt/internal/controlcenter/config"
)

// HeaderEmail is set by oauth2-proxy (--set-xauthrequest).
const HeaderEmail = "X-Auth-Request-Email"

// Role is a privilege level; higher values include lower ones.
type Role int

// Roles, lowest first.
const (
	RoleViewer    Role = iota // browse
	RoleDeveloper             // + run, abort, comment, schedule
	RoleAdmin                 // + delete history, cleanup
)

func (r Role) String() string {
	switch r {
	case RoleAdmin:
		return "admin"
	case RoleDeveloper:
		return "developer"
	default:
		return "viewer"
	}
}

// User is the caller of a request.
type User struct {
	// Email is "" for anonymous requests (no oauth2-proxy header).
	Email string
	Role  Role
}

// Can reports whether the user holds at least min.
func (u User) Can(min Role) bool { return u.Role >= min }

// Resolver maps emails to roles.
type Resolver struct {
	admins, developers []string
	// devUser stands in for the oauth2-proxy header when it's absent.
	devUser string
}

// NewResolver builds a Resolver. devUser (local development without
// oauth2-proxy) is refused in production.
func NewResolver(cfg *config.Config, devUser string) (*Resolver, error) {
	devUser = strings.ToLower(strings.TrimSpace(devUser))
	if devUser != "" && cfg.Environment == config.EnvProduction {
		return nil, errors.New("auth: a development user override is refused in production")
	}
	return &Resolver{admins: cfg.RBAC.Admins, developers: cfg.RBAC.Developers, devUser: devUser}, nil
}

// Resolve returns the user for an email.
func (r *Resolver) Resolve(email string) User {
	email = strings.ToLower(strings.TrimSpace(email))
	switch {
	case email == "":
		return User{Role: RoleViewer}
	case slices.Contains(r.admins, email):
		return User{Email: email, Role: RoleAdmin}
	case slices.Contains(r.developers, email):
		return User{Email: email, Role: RoleDeveloper}
	default:
		return User{Email: email, Role: RoleViewer}
	}
}

// Middleware attaches the User to every request.
func (r *Resolver) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		email := req.Header.Get(HeaderEmail)
		if email == "" {
			email = r.devUser
		}
		next.ServeHTTP(w, req.WithContext(WithUser(req.Context(), r.Resolve(email))))
	})
}

type ctxKey struct{}

// WithUser stores u in ctx.
func WithUser(ctx context.Context, u User) context.Context {
	return context.WithValue(ctx, ctxKey{}, u)
}

// FromContext returns the request's user (anonymous viewer if unset).
func FromContext(ctx context.Context) User {
	u, _ := ctx.Value(ctxKey{}).(User)
	return u
}

// Require wraps h so only users holding min reach it; others get 403.
func Require(min Role, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := FromContext(r.Context())
		if !u.Can(min) {
			http.Error(w, fmt.Sprintf("Forbidden: this action requires the %s role; you are %s.", min, u.Role),
				http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}
