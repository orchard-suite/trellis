// Package door reads who a sign-in proxy let in. trellis keeps no sign-in
// of its own: a proxy in front of it (a front door, oauth2-proxy, an
// identity-aware proxy) signs the person in and sets two headers, the
// person's email and their roles on the app. trellis trusts them only on a
// port its network keeps to that proxy. The header names are settings.
package door

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
)

// Config names the headers and the roles that may open the board.
type Config struct {
	// Mode is "headers", trusting the proxy's headers, or "open", admitting
	// anyone, for a board on a laptop.
	Mode string `yaml:"mode"`
	// EmailHeader and GroupsHeader default to X-Forwarded-Email and
	// X-Forwarded-Groups.
	EmailHeader  string `yaml:"email_header"`
	GroupsHeader string `yaml:"groups_header"`
	// Roles are the roles, as the proxy sends them, that may open the
	// board. Empty admits anyone the proxy signed in.
	Roles []string `yaml:"roles"`
}

// Person is who the proxy let in.
type Person struct {
	Email string
	Roles []string
}

type personKey struct{}

// From returns the person a gate admitted, or the zero Person.
func From(ctx context.Context) Person {
	p, _ := ctx.Value(personKey{}).(Person)
	return p
}

// Gate admits requests the proxy signed in.
type Gate struct {
	cfg Config
	log *slog.Logger
}

// New makes a gate.
func New(cfg Config, log *slog.Logger) (*Gate, error) {
	if cfg.Mode == "" {
		cfg.Mode = "headers"
	}
	if cfg.Mode != "headers" && cfg.Mode != "open" {
		return nil, fmt.Errorf("sign-in mode %q is not headers or open", cfg.Mode)
	}
	if cfg.EmailHeader == "" {
		cfg.EmailHeader = "X-Forwarded-Email"
	}
	if cfg.GroupsHeader == "" {
		cfg.GroupsHeader = "X-Forwarded-Groups"
	}
	for _, r := range cfg.Roles {
		if r == "" || strings.ContainsAny(r, " ,") {
			return nil, fmt.Errorf("role %q is not one role", r)
		}
	}
	return &Gate{cfg: cfg, log: log}, nil
}

func (g *Gate) read(r *http.Request) (Person, bool) {
	if g.cfg.Mode == "open" {
		return Person{}, true
	}
	email := strings.TrimSpace(r.Header.Get(g.cfg.EmailHeader))
	if email == "" {
		return Person{}, false
	}
	var roles []string
	for _, role := range strings.Split(r.Header.Get(g.cfg.GroupsHeader), ",") {
		if role = strings.TrimSpace(role); role != "" {
			roles = append(roles, role)
		}
	}
	return Person{Email: email, Roles: roles}, true
}

// Wrap admits a request to next with the person in its context. Wrap each
// route, not the mux, so the log names the route.
func (g *Gate) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := g.read(r)
		if !ok {
			g.log.WarnContext(r.Context(), "door refused: no identity", "route", r.Pattern)
			http.Error(w, "Sign in first.", http.StatusUnauthorized)
			return
		}
		if g.cfg.Mode == "headers" && len(g.cfg.Roles) > 0 && !slices.ContainsFunc(g.cfg.Roles, func(want string) bool { return slices.Contains(p.Roles, want) }) {
			g.log.WarnContext(r.Context(), "door refused: no role", "route", r.Pattern, "want", g.cfg.Roles, "held", p.Roles)
			http.Error(w, "This is not yours to open.", http.StatusForbidden)
			return
		}
		g.log.DebugContext(r.Context(), "door admitted", "route", r.Pattern, "held", p.Roles)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), personKey{}, p)))
	})
}

// SameOrigin admits a change only from the board's own pages.
func SameOrigin(origin string, log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Origin"); got != origin {
			log.WarnContext(r.Context(), "change refused: foreign origin", "route", r.Pattern, "origin", got)
			http.Error(w, "This change must come from the board's own pages.", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
