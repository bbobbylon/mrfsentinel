-- Initial schema for MRF Sentinel.
--
-- gen_random_uuid() is used throughout for primary keys: it has been a
-- built-in core PostgreSQL function since version 13 (no CREATE EXTENSION
-- needed) — verified against PostgreSQL's own release notes before relying
-- on it here, since docker-compose.yml pins postgres:16-alpine.

CREATE TABLE users (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email      TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One row per magic-link email sent. token_hash, never the raw token, is
-- stored — the same reason a password column holds a bcrypt hash, not the
-- password: if this table ever leaked, a stolen row shouldn't be a usable
-- sign-in link. See AUTH.md.
CREATE TABLE magic_links (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash  TEXT NOT NULL UNIQUE,
    email       TEXT NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX magic_links_email_idx ON magic_links (email);

-- One row per signed-in browser session. Same token_hash reasoning as
-- magic_links above.
CREATE TABLE sessions (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash TEXT NOT NULL UNIQUE,
    user_id    UUID NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX sessions_user_idx ON sessions (user_id);

-- A hospital a compliance officer is tracking, identified by the public URL
-- of its own published MRF.
CREATE TABLE hospitals (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_user_id UUID NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    mrf_url       TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX hospitals_owner_idx ON hospitals (owner_user_id);

CREATE TYPE validation_status AS ENUM ('pending', 'running', 'succeeded', 'failed');

-- One row per attempt to fetch and check one hospital's MRF against the
-- CY2026 checklist. "succeeded" means the run completed, not that the file
-- passed every rule — see overall_passed for that; a run can succeed and
-- still report a non-compliant file, which is the entire point of the tool.
CREATE TABLE validation_runs (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    hospital_id    UUID NOT NULL REFERENCES hospitals (id) ON DELETE CASCADE,
    status         validation_status NOT NULL DEFAULT 'pending',
    format         TEXT NOT NULL DEFAULT '',
    rows_processed BIGINT NOT NULL DEFAULT 0,
    overall_passed BOOLEAN,
    parse_err      TEXT NOT NULL DEFAULT '',
    error_message  TEXT NOT NULL DEFAULT '',
    started_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at    TIMESTAMPTZ
);

CREATE INDEX validation_runs_hospital_idx ON validation_runs (hospital_id, started_at DESC);

-- One row per hospital-level checklist rule (HOSP-NAME, CY26-ATTESTATION,
-- ...) for one run — see internal/rules.CheckResult, which this mirrors
-- column for column.
CREATE TABLE validation_hospital_checks (
    id          BIGSERIAL PRIMARY KEY,
    run_id      UUID NOT NULL REFERENCES validation_runs (id) ON DELETE CASCADE,
    rule_id     TEXT NOT NULL,
    description TEXT NOT NULL,
    passed      BOOLEAN NOT NULL,
    detail      TEXT NOT NULL DEFAULT ''
);

CREATE INDEX validation_hospital_checks_run_idx ON validation_hospital_checks (run_id);

-- One row per item-level checklist rule (ITEM-GROSS-CHARGE,
-- CY26-PERCENTILES, ...) for one run — mirrors
-- internal/rules.ItemRuleSummary.
CREATE TABLE validation_item_checks (
    id              BIGSERIAL PRIMARY KEY,
    run_id          UUID NOT NULL REFERENCES validation_runs (id) ON DELETE CASCADE,
    rule_id         TEXT NOT NULL,
    description     TEXT NOT NULL,
    rows_checked    BIGINT NOT NULL DEFAULT 0,
    rows_failed     BIGINT NOT NULL DEFAULT 0,
    sample_failures TEXT[] NOT NULL DEFAULT '{}'
);

CREATE INDEX validation_item_checks_run_idx ON validation_item_checks (run_id);
