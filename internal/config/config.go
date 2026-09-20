// Package config loads MRF Sentinel's runtime configuration from environment
// variables. There is deliberately no config file format (YAML/properties) to
// parse here — for a project this size, reading env vars directly with sane
// defaults is the whole job, and adding a config-file library would be a
// dependency earning its keep only at much larger scale.
//
// If you're coming from Spring Boot: this file is the equivalent of
// application.yml plus the @ConfigurationProperties class that binds it —
// except there's no framework auto-wiring it in. Load() is called once, by
// hand, in main(), and the returned Config is passed to whatever needs it.
// That's normal in Go: dependencies are passed as explicit function
// arguments ("dependency injection" without a container), not resolved by a
// framework scanning for annotations.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds every setting MRF Sentinel needs to run. All fields are
// resolved once, at startup, from environment variables — see Load.
type Config struct {
	// HTTPAddr is the address the web server listens on, e.g. ":8080".
	HTTPAddr string

	// DatabaseURL is a standard Postgres connection string
	// (postgres://user:pass@host:port/dbname?sslmode=disable).
	DatabaseURL string

	// PublicBaseURL is this app's own externally-reachable URL, used to
	// build the magic-link URLs emailed to users (e.g. https://app.example.com).
	PublicBaseURL string

	// SMTP settings for sending magic-link emails. In local dev these point
	// at Mailhog (see docker-compose.yml), which never sends real mail and
	// just catches it for you to read at http://localhost:8025.
	SMTPHost string
	SMTPPort int
	SMTPFrom string

	// SessionCookieName is the name of the cookie holding a signed session
	// token. See internal/auth/session.go for how sessions actually work.
	SessionCookieName string

	// SessionTTL is how long a signed-in session stays valid without
	// activity before the user has to request a new magic link.
	SessionTTL time.Duration

	// MaxMRFBytes caps how much of a hospital's published MRF this app will
	// read before giving up. Real hospital MRFs can run into the multiple
	// gigabytes; this bound exists so a single malicious or misconfigured
	// URL can't exhaust this server's memory or disk. See internal/mrf/fetch.go.
	MaxMRFBytes int64

	// FetchTimeout bounds how long downloading + validating one MRF is
	// allowed to take before the background job is marked failed.
	FetchTimeout time.Duration
}

// Load reads Config from environment variables, applying the defaults noted
// per field below. It returns an error rather than calling os.Exit itself,
// so callers (main, and tests) decide how to handle a bad config.
func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:          getEnv("HTTP_ADDR", ":8080"),
		DatabaseURL:       getEnv("DATABASE_URL", "postgres://mrfsentinel:mrfsentinel_local_dev@localhost:5432/mrfsentinel?sslmode=disable"),
		PublicBaseURL:     getEnv("PUBLIC_BASE_URL", "http://localhost:8080"),
		SMTPHost:          getEnv("SMTP_HOST", "localhost"),
		SMTPFrom:          getEnv("SMTP_FROM", "no-reply@mrfsentinel.local"),
		SessionCookieName: getEnv("SESSION_COOKIE_NAME", "mrfsentinel_session"),
	}

	smtpPort, err := getEnvInt("SMTP_PORT", 1025)
	if err != nil {
		return Config{}, err
	}
	cfg.SMTPPort = smtpPort

	sessionTTLHours, err := getEnvInt("SESSION_TTL_HOURS", 24*14) // 14 days
	if err != nil {
		return Config{}, err
	}
	cfg.SessionTTL = time.Duration(sessionTTLHours) * time.Hour

	maxMRFMebibytes, err := getEnvInt("MAX_MRF_MEBIBYTES", 4096) // 4 GiB
	if err != nil {
		return Config{}, err
	}
	cfg.MaxMRFBytes = int64(maxMRFMebibytes) * 1024 * 1024

	fetchTimeoutMinutes, err := getEnvInt("FETCH_TIMEOUT_MINUTES", 15)
	if err != nil {
		return Config{}, err
	}
	cfg.FetchTimeout = time.Duration(fetchTimeoutMinutes) * time.Minute

	return cfg, nil
}

// getEnv reads a string setting, treating both "unset" and "set but empty"
// as absent. That second case is not hypothetical: a Docker Compose file or
// an ECS task definition that declares a variable without giving it a value
// passes it through as "", and accepting that literally would override the
// intended default with nothing.
func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

// getEnvInt is getEnv for integer settings. A variable that is present but
// unparseable returns an error rather than falling back to the default: a
// typo'd MAX_MRF_MEBIBYTES should stop the process at startup with a clear
// message, not quietly run with a different limit than whoever set it
// intended. Load turns that error into a failed boot.
func getEnvInt(key string, fallback int) (int, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s must be an integer, got %q: %w", key, v, err)
	}
	return n, nil
}
