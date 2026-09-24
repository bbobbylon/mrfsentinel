package config

import (
	"strings"
	"testing"
	"time"
)

// allEnvKeys is every variable Load consults. Tests clear the whole set
// before asserting anything, because the process running them may already
// have some of these set — CI, for one, exports DATABASE_URL for the
// Postgres service container, which would otherwise make the defaults test
// fail for reasons that have nothing to do with the code.
var allEnvKeys = []string{
	"HTTP_ADDR",
	"DATABASE_URL",
	"PUBLIC_BASE_URL",
	"SMTP_HOST",
	"SMTP_PORT",
	"SMTP_FROM",
	"SESSION_COOKIE_NAME",
	"SESSION_TTL_HOURS",
	"MAX_MRF_MEBIBYTES",
	"FETCH_TIMEOUT_MINUTES",
}

// clearEnv blanks every variable in allEnvKeys for the duration of one
// test. It sets them to "" rather than unsetting them because t.Setenv has
// no unset, and because getEnv deliberately treats "set but empty" as
// absent — so this exercises that path at the same time. Note t.Setenv makes
// a test incompatible with t.Parallel, which is why nothing here runs in
// parallel.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range allEnvKeys {
		t.Setenv(k, "")
	}
}

// TestLoad_Defaults asserts the values a developer gets from a bare
// checkout with no environment set up at all. These defaults are what make
// `go run ./cmd/server` work against the docker-compose stack without a
// .env file, so changing one is a change to the local-dev contract.
func TestLoad_Defaults(t *testing.T) {
	clearEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want %q", cfg.HTTPAddr, ":8080")
	}
	if !strings.HasPrefix(cfg.DatabaseURL, "postgres://") {
		t.Errorf("DatabaseURL = %q, want a postgres:// connection string", cfg.DatabaseURL)
	}
	if cfg.PublicBaseURL != "http://localhost:8080" {
		t.Errorf("PublicBaseURL = %q, want %q", cfg.PublicBaseURL, "http://localhost:8080")
	}
	if cfg.SMTPHost != "localhost" {
		t.Errorf("SMTPHost = %q, want %q", cfg.SMTPHost, "localhost")
	}
	if cfg.SMTPPort != 1025 {
		t.Errorf("SMTPPort = %d, want 1025 (Mailhog)", cfg.SMTPPort)
	}
	if cfg.SessionCookieName != "mrfsentinel_session" {
		t.Errorf("SessionCookieName = %q, want %q", cfg.SessionCookieName, "mrfsentinel_session")
	}
	if want := 14 * 24 * time.Hour; cfg.SessionTTL != want {
		t.Errorf("SessionTTL = %v, want %v (14 days)", cfg.SessionTTL, want)
	}
	if want := int64(4096) * 1024 * 1024; cfg.MaxMRFBytes != want {
		t.Errorf("MaxMRFBytes = %d, want %d (4 GiB)", cfg.MaxMRFBytes, want)
	}
	if want := 15 * time.Minute; cfg.FetchTimeout != want {
		t.Errorf("FetchTimeout = %v, want %v", cfg.FetchTimeout, want)
	}
}

// TestLoad_OverridesFromEnvironment checks that every setting is actually
// reachable from the environment. A field that silently ignored its
// variable would only show up in production, where it is least convenient
// to notice.
func TestLoad_OverridesFromEnvironment(t *testing.T) {
	clearEnv(t)
	t.Setenv("HTTP_ADDR", ":9999")
	t.Setenv("DATABASE_URL", "postgres://u:p@db:5432/app?sslmode=require")
	t.Setenv("PUBLIC_BASE_URL", "https://sentinel.example.com")
	t.Setenv("SMTP_HOST", "smtp.example.com")
	t.Setenv("SMTP_PORT", "587")
	t.Setenv("SMTP_FROM", "alerts@example.com")
	t.Setenv("SESSION_COOKIE_NAME", "custom_session")
	t.Setenv("SESSION_TTL_HOURS", "1")
	t.Setenv("MAX_MRF_MEBIBYTES", "2")
	t.Setenv("FETCH_TIMEOUT_MINUTES", "3")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.HTTPAddr != ":9999" {
		t.Errorf("HTTPAddr = %q, want %q", cfg.HTTPAddr, ":9999")
	}
	if cfg.DatabaseURL != "postgres://u:p@db:5432/app?sslmode=require" {
		t.Errorf("DatabaseURL = %q, not the value from the environment", cfg.DatabaseURL)
	}
	if cfg.PublicBaseURL != "https://sentinel.example.com" {
		t.Errorf("PublicBaseURL = %q, not the value from the environment", cfg.PublicBaseURL)
	}
	if cfg.SMTPHost != "smtp.example.com" {
		t.Errorf("SMTPHost = %q, not the value from the environment", cfg.SMTPHost)
	}
	if cfg.SMTPPort != 587 {
		t.Errorf("SMTPPort = %d, want 587", cfg.SMTPPort)
	}
	if cfg.SMTPFrom != "alerts@example.com" {
		t.Errorf("SMTPFrom = %q, not the value from the environment", cfg.SMTPFrom)
	}
	if cfg.SessionCookieName != "custom_session" {
		t.Errorf("SessionCookieName = %q, not the value from the environment", cfg.SessionCookieName)
	}
}

// TestLoad_UnitConversions covers the four settings whose environment
// variable is in different units from the struct field it lands in. These
// conversions are the easiest thing in this file to get wrong by a factor
// of 60 or 1024 and the hardest to notice afterwards.
func TestLoad_UnitConversions(t *testing.T) {
	clearEnv(t)
	t.Setenv("SESSION_TTL_HOURS", "48")
	t.Setenv("MAX_MRF_MEBIBYTES", "1")
	t.Setenv("FETCH_TIMEOUT_MINUTES", "90")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if want := 48 * time.Hour; cfg.SessionTTL != want {
		t.Errorf("SESSION_TTL_HOURS=48 gave SessionTTL %v, want %v", cfg.SessionTTL, want)
	}
	if want := int64(1024 * 1024); cfg.MaxMRFBytes != want {
		t.Errorf("MAX_MRF_MEBIBYTES=1 gave MaxMRFBytes %d, want %d (one mebibyte)", cfg.MaxMRFBytes, want)
	}
	if want := 90 * time.Minute; cfg.FetchTimeout != want {
		t.Errorf("FETCH_TIMEOUT_MINUTES=90 gave FetchTimeout %v, want %v", cfg.FetchTimeout, want)
	}
}

// TestLoad_RejectsUnparseableIntegers is the behavior getEnvInt's doc
// comment promises: a typo stops the process at boot instead of silently
// running with the default. Each variable is checked separately, since it
// would be easy to add a fifth integer setting and forget to propagate its
// error.
func TestLoad_RejectsUnparseableIntegers(t *testing.T) {
	intKeys := []string{
		"SMTP_PORT",
		"SESSION_TTL_HOURS",
		"MAX_MRF_MEBIBYTES",
		"FETCH_TIMEOUT_MINUTES",
	}

	for _, key := range intKeys {
		t.Run(key, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(key, "not-a-number")

			_, err := Load()
			if err == nil {
				t.Fatalf("Load() accepted %s=%q instead of failing", key, "not-a-number")
			}
			// The message has to name the offending variable, or whoever
			// made the typo has to go looking for which one it was.
			if !strings.Contains(err.Error(), key) {
				t.Errorf("error %q does not name the variable %s", err, key)
			}
		})
	}
}

// TestLoad_EmptyValuesFallBackToDefaults pins the getEnv behavior that
// exists specifically for container runtimes: Docker Compose and ECS task
// definitions both pass a declared-but-unset variable through as "", and
// taking that literally would replace a working default with nothing. An
// empty integer variable likewise falls back rather than erroring.
func TestLoad_EmptyValuesFallBackToDefaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("SMTP_PORT", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error on empty values: %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("empty HTTP_ADDR gave %q, want the default %q", cfg.HTTPAddr, ":8080")
	}
	if cfg.SMTPPort != 1025 {
		t.Errorf("empty SMTP_PORT gave %d, want the default 1025", cfg.SMTPPort)
	}
}
