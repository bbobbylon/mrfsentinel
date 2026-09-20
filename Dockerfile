# Multi-stage build: compile the Go binary in a full Go image, then copy
# just that binary into a minimal runtime image. This is the Go-world
# equivalent of DeleteBoard's Dockerfile (a JDK build stage, a slim-JRE
# runtime stage) — except the runtime stage here needs no language runtime
# at all, because `go build` already produced a single static executable
# with the Go runtime compiled INTO it. There's no JVM to start, no
# framework to boot — just that one file.

# --- build stage --------------------------------------------------------
FROM golang:1.24-alpine AS build
WORKDIR /src

# Copy go.mod/go.sum first and download dependencies before copying the
# rest of the source, so Docker's layer cache only re-downloads modules
# when go.mod/go.sum actually change — not on every source edit. Same
# reasoning as DeleteBoard's backend Dockerfile copying pom.xml before the
# rest of the Maven project.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 produces a truly static binary (no libc dependency), which
# is what lets the runtime stage below be as small as it is — lib/pq (this
# project's one dependency) is pure Go and has no C bindings to lose by
# disabling cgo, unlike some other Postgres drivers.
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/mrfsentinel ./cmd/server

# --- runtime stage -------------------------------------------------------
FROM alpine:3.24
# ca-certificates: needed because this app makes outbound HTTPS requests
# (fetching a hospital's own published MRF, see internal/mrf/fetch.go) —
# without it, every HTTPS fetch would fail TLS verification.
# wget: used only by the HEALTHCHECK below.
RUN apk add --no-cache ca-certificates wget

COPY --from=build /out/mrfsentinel /usr/local/bin/mrfsentinel

EXPOSE 8080

HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -q -O - http://localhost:8080/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/mrfsentinel"]
