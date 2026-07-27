# syntax=docker/dockerfile:1
# Default image: Go API only (~15–25 MB). No Calibre bundled.
# Library writes use a pure-Go Calibre metadata.db adder (no Qt).
#
# Optional fat image with full calibredb: Dockerfile.calibre → tag :with-calibre

# ── Build ───────────────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /out/folio-server .

# ── Runtime (static, minimal) ───────────────────────────────────────────────
FROM alpine:3.20

LABEL org.opencontainers.image.title="folio-server" \
      org.opencontainers.image.description="Folio auth/progress API + Calibre library writer (slim)" \
      org.opencontainers.image.source="https://github.com/ghaemaghaey/folio" \
      org.opencontainers.image.licenses="MIT"

RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 1000 -g 1000 folio

ENV LANG=C.UTF-8 \
    CALIBRE_LIBRARY_PATH=/library \
    DB_PATH=/data/folio.db \
    LISTEN_ADDR=:8090 \
    # Prefer pure-Go library writer in slim image (no calibredb binary).
    LIBRARY_WRITER=native

WORKDIR /app
COPY --from=builder /out/folio-server /app/folio-server

RUN mkdir -p /data /library /tmp/folio-uploads \
    && chown -R folio:folio /data /library /tmp/folio-uploads /app \
    && chmod +x /app/folio-server

USER folio
EXPOSE 8090
VOLUME ["/data", "/library"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8090/health >/dev/null || exit 1

ENTRYPOINT ["/app/folio-server"]
