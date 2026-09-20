# Deployment

This mirrors the same AWS ECS Fargate + ALB + CloudFront shape used for DeleteBoard, simplified
for the fact that MRF Sentinel is **one binary, one container, one ECS service** — there's no
separate frontend to run as its own service, since `internal/web`'s templates and static assets
are compiled into the Go binary at build time (`go:embed` — see `ARCHITECTURE.md`). Same caveat as
DeleteBoard's `DEPLOY.md`: this sandbox has no access to any existing Terraform/CDK/CLI scripts you
may already use, so what follows is a from-scratch deployment built on well-established, documented
AWS practice — a solid starting point to diff against your own conventions, not a guaranteed match.

## Target architecture

```
                          ┌────────────────────┐
                          │      Route 53        │
                          │  mrfsentinel.<you>    │
                          └──────────┬───────────┘
                                     │
                          ┌──────────▼───────────┐
                          │      CloudFront         │   TLS termination (ACM cert),
                          │  (edge cache + TLS)     │   caches /static/* at the edge
                          └──────────┬───────────┘
                                     │  origin
                          ┌──────────▼───────────┐
                          │  Application Load        │   public subnets
                          │  Balancer (ALB)          │
                          └──────────┬───────────┘
                                     │
                          ┌──────────▼───────────┐
                          │  ECS Fargate service     │   private subnets
                          │  "mrfsentinel"           │
                          │  (single Go binary,      │
                          │   frontend + backend)    │
                          └──────────┬───────────┘
                                     │
                    ┌────────────────┼────────────────┐
                    │                                  │
         ┌──────────▼───────────┐          ┌──────────▼───────────┐
         │  Managed Postgres        │          │  SES (or other SMTP)     │
         │  (RDS)                   │          │  for magic-link emails   │
         └──────────────────────┘          └──────────────────────┘

         ECR: one repo (mrfsentinel) holds the built image.
         Secrets Manager: DB credentials, SMTP credentials.
         CloudWatch Logs: the one service's stdout/stderr.
```

The container this repo already builds (`Dockerfile`, multi-stage `golang:1.24-alpine` →
`alpine:3.24`) is deployed as-is — nothing about it needs to change for ECS versus `docker
compose`. There's no `/api/*` vs `/*` routing split to configure on the ALB like DeleteBoard needs,
because this is one service serving both the dashboard pages and their data — one target group,
one listener rule (`/*` → that target group).

## Prerequisites

- An AWS account, and the AWS CLI configured locally (`aws configure` or SSO) for one-time setup
  steps.
- A container registry: one ECR repository, `mrfsentinel`.
- A managed Postgres instance (RDS), on a Postgres 16.x-compatible version (matching
  `postgres:16-alpine` used locally — see `internal/store/migrations/0001_init.sql`, which relies
  on `gen_random_uuid()`, built into core Postgres since v13, so no `pgcrypto` extension needed).
- A domain and an ACM certificate (in `us-east-1` specifically, if it'll front CloudFront) if you
  want this on a real domain rather than the raw ALB/CloudFront hostname.
- A real SMTP relay for production magic-link emails. Mailhog (`docker-compose.yml`) is local-dev
  only — point `SMTP_HOST`/`SMTP_PORT`/`SMTP_FROM` (see `internal/config/config.go`) at something
  real, e.g. Amazon SES.

## One-time infrastructure setup

These are one-time (or infrequent) steps — run manually via the AWS CLI or console, or translated
into Terraform/CDK if that's your convention; this doc gives the shape and the CLI form since
there's no existing IaC in this repo yet to extend.

### 1. ECR repository

```bash
aws ecr create-repository --repository-name mrfsentinel
```

### 2. Secrets in AWS Secrets Manager

```bash
aws secretsmanager create-secret --name mrfsentinel/db-password --secret-string '<generate one>'
aws secretsmanager create-secret --name mrfsentinel/smtp-credentials --secret-string '{"username":"...","password":"..."}'
```

There's no JWT signing secret here to generate, unlike DeleteBoard — MRF Sentinel's sessions are
opaque random tokens looked up in the `sessions` table (`internal/auth/session.go`,
`internal/store/users.go`), not self-contained signed tokens, so there's no shared secret to
provision or rotate. That's a deliberate simplicity trade-off explained in `AUTH.md`.

### 3. Managed Postgres

Provision an RDS Postgres 16.x instance reachable from the ECS task's private subnets. Put its
connection details where the ECS task definition's environment expects them — `DATABASE_URL` as a
single connection string (see `internal/config/config.go`'s `Load()` and `docker-compose.yml`'s
`app` service for the exact format), with the password substituted from Secrets Manager rather
than embedded in a plain environment variable.

`Migrate()` (`internal/store/migrate.go`) runs automatically against this database every time the
task starts — there is no separate "run migrations" step to remember, and no Flyway/golang-migrate
dependency to install anywhere in the deploy pipeline. It's a small hand-rolled runner (see
`ARCHITECTURE.md` for why) that walks `//go:embed migrations/*.sql` in filename order and records
what's applied in a `schema_migrations` table — the same idempotent-on-restart behavior Flyway
gives DeleteBoard, just without the extra dependency.

### 4. ECS cluster, task definition, service

```bash
aws ecs create-cluster --cluster-name mrfsentinel
```

The task definition references the ECR image and the environment variables / secrets above — its
environment mirrors `docker-compose.yml`'s `app` service almost exactly, with two differences:
`DATABASE_URL`'s host points at the RDS endpoint instead of the `postgres` service name, and the
DB password / SMTP credentials come from `secrets` (Secrets Manager ARNs) in the task definition
rather than plain `environment` entries — task definition JSON distinguishes these as two
different keys, and only `secrets` entries are safe for anything sensitive, since plain
`environment` values are visible to anyone who can read the task definition.

Start with `desiredCount: 1`. See `ARCHITECTURE.md`'s note on the background validation worker
before scaling past one task — right now a validation run is processed in-process by whichever
task instance received the `TriggerRun` request, with no distributed queue, so multiple tasks
would each independently be fine handling *different* runs concurrently, but there's no
cross-instance locking if you needed exactly-once semantics for some future scheduled/batch
validation feature.

### 5. Application Load Balancer

- Target group `mrfsentinel-tg`: port 8080, health check path `/healthz` (same endpoint the local
  Docker healthcheck already uses — see `Dockerfile`'s `HEALTHCHECK`).
- Listener rule: `/*` (default, and only) → that target group.

### 6. CloudFront

Create a distribution with the ALB as its origin. Unlike DeleteBoard's split (cache the frontend's
static assets at the edge, never cache `/api/*`), here **only `/static/*` is safe to cache** —
everything else (`/`, `/hospitals/*`, `/auth/*`) is server-rendered HTML that depends on the
signed-in user and current data, and must never be cached. Give `/static/*` its own cache behavior
with caching enabled and forward all headers/cookies/query strings uncached for every other path.
Attach the ACM certificate for TLS. Point the domain's Route 53 record at the CloudFront
distribution.

## CI/CD

`.github/workflows/ci.yml` already runs on every push and PR — `go build`, `go vet`, `go test`
against a real Postgres service container, and a `docker build`. `.github/workflows/cd.yml` (added
alongside this doc) extends that: on a push to `main`, it builds the image, pushes it to ECR, and
forces a new ECS deployment.

It authenticates to AWS via **OIDC** (a GitHub-issued short-lived token exchanged for temporary AWS
credentials) rather than long-lived access keys stored as secrets — the current AWS-recommended
pattern specifically because it avoids a static credential sitting in GitHub that has to be
rotated and can leak. Verified current as of this writing: `aws-actions/configure-aws-credentials`
at `v6.2.4` and `aws-actions/amazon-ecr-login` at `v2.1.6` (checked against their GitHub releases
pages, not assumed).

**Required setup before `cd.yml` will run successfully:**

1. An IAM role trusted for GitHub OIDC (`token.actions.githubusercontent.com` as the identity
   provider), scoped to this repository, with permission to push to the ECR repo and update the
   ECS service.
2. Repository variables/secrets in GitHub (Settings → Secrets and variables → Actions):
   - `AWS_REGION` (variable)
   - `AWS_DEPLOY_ROLE_ARN` (secret) — the OIDC role's ARN from step 1
   - `ECS_CLUSTER` (variable) — `mrfsentinel`

Until that IAM role exists, `cd.yml` will fail at the "configure AWS credentials" step — that's
expected on a fresh clone; it's infrastructure this doc can describe but can't provision for you
from inside a repo.

### Rolling back

ECS keeps previous task definition revisions. The fastest rollback is pointing the service at the
last-known-good revision rather than deploying a fixed-forward image:

```bash
aws ecs update-service \
  --cluster mrfsentinel \
  --service mrfsentinel \
  --task-definition mrfsentinel:<previous-revision-number>
```

Migrations here only ever add a new numbered file (see `ARCHITECTURE.md` — never edit
`0001_init.sql` once it's run anywhere outside your own machine), so rolling the application back
is safe as long as no rolled-forward migration is destructive to data the previous code version
expects — the same caveat DeleteBoard's `DEPLOY.md` gives, worth a moment's thought before rolling
back across a release that changed the schema.

## Local dev, for reference

Full local setup lives in `README.md` — this file is cloud deployment only, to keep the two
concerns in the document that's actually about each.
