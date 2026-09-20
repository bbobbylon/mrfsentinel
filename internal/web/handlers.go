package web

import (
	"log/slog"

	"github.com/bobbylon127/mrfsentinel/internal/auth"
	"github.com/bobbylon127/mrfsentinel/internal/config"
	"github.com/bobbylon127/mrfsentinel/internal/store"
	"github.com/bobbylon127/mrfsentinel/internal/validation"
)

// Handlers holds everything an HTTP handler method might need — the
// dependency-injection container's job in a Spring app, except it's just a
// plain struct built once by hand in cmd/server/main.go and passed around,
// with every dependency visible as a field rather than resolved by
// annotation scanning.
type Handlers struct {
	store  *store.Store
	mailer auth.Mailer
	worker *validation.Worker
	tmpl   *templates
	cfg    config.Config
	logger *slog.Logger
}

// NewHandlers constructs Handlers, including loading and parsing every
// embedded HTML template up front — so a broken template fails the
// process at startup, not on the first request that happens to render it.
func NewHandlers(st *store.Store, mailer auth.Mailer, worker *validation.Worker, cfg config.Config, logger *slog.Logger) (*Handlers, error) {
	tmpl, err := loadTemplates()
	if err != nil {
		return nil, err
	}
	return &Handlers{store: st, mailer: mailer, worker: worker, tmpl: tmpl, cfg: cfg, logger: logger}, nil
}
