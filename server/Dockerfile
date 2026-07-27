# syntax=docker/dockerfile:1
# Image published by CI to: ghcr.io/<owner>/folio-server

# ── Build stage (pure Go / modernc sqlite — no cgo) ─────────────────────────
FROM golang:1.22-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /out/folio-server .

# ── Runtime: Debian + Calibre (for calibredb) ───────────────────────────────
FROM debian:bookworm-slim

LABEL org.opencontainers.image.title="folio-server" \
      org.opencontainers.image.description="Folio library writer + auth/progress API" \
      org.opencontainers.image.source="https://github.com/ghaemaghaey/folio" \
      org.opencontainers.image.licenses="MIT"

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    CALIBRE_LIBRARY_PATH=/library \
    DB_PATH=/data/folio.db \
    LISTEN_ADDR=:8090 \
    PATH="/opt/calibre:${PATH}"

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        wget \
        xz-utils \
        libegl1 \
        libopengl0 \
        libxcb-cursor0 \
        libxkbcommon0 \
        libgl1 \
        libnss3 \
        libxcomposite1 \
        libxdamage1 \
        libxrandr2 \
        libxtst6 \
        libasound2 \
        python3 \
    && rm -rf /var/lib/apt/lists/*

# Official Calibre linux installer (provides calibredb)
RUN wget -nv -O- https://download.calibre-ebook.com/linux-installer.sh | sh /dev/stdin install_dir=/opt isolate=y \
    && ln -sf /opt/calibre/calibredb /usr/local/bin/calibredb \
    && calibredb --version

WORKDIR /app
COPY --from=builder /out/folio-server /app/folio-server

RUN mkdir -p /data /library /tmp/folio-uploads \
    && chmod +x /app/folio-server

EXPOSE 8090
VOLUME ["/data", "/library"]

# JWT_SECRET must be provided at runtime
ENTRYPOINT ["/app/folio-server"]
