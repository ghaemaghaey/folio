# syntax=docker/dockerfile:1
# Image published by CI to: ghcr.io/<owner>/folio-server
#
# Calibre is installed from the official binary tarball (not linux-installer.sh),
# which is more reliable in Docker/CI.

# ── Build stage (pure Go / modernc sqlite — no cgo) ─────────────────────────
FROM golang:1.22-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /out/folio-server .

# ── Runtime: Debian + official Calibre binaries (calibredb) ─────────────────
FROM debian:bookworm-slim

LABEL org.opencontainers.image.title="folio-server" \
      org.opencontainers.image.description="Folio library writer + auth/progress API" \
      org.opencontainers.image.source="https://github.com/ghaemaghaey/folio" \
      org.opencontainers.image.licenses="MIT"

# Pin a known-good Calibre release (override with --build-arg CALIBRE_VERSION=…).
ARG CALIBRE_VERSION=7.26.0
ARG TARGETARCH

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    CALIBRE_LIBRARY_PATH=/library \
    DB_PATH=/data/folio.db \
    LISTEN_ADDR=:8090 \
    PATH="/opt/calibre:${PATH}" \
    # Headless Qt (no X11 required for calibredb)
    QT_QPA_PLATFORM=offscreen \
    QTWEBENGINE_DISABLE_SANDBOX=1

RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        xz-utils \
        libegl1 \
        libopengl0 \
        libgl1 \
        libglib2.0-0 \
        libfontconfig1 \
        libfreetype6 \
        libdbus-1-3 \
        libxcb1 \
        libxcb-cursor0 \
        libxcb-icccm4 \
        libxcb-image0 \
        libxcb-keysyms1 \
        libxcb-render0 \
        libxcb-render-util0 \
        libxcb-shape0 \
        libxcb-shm0 \
        libxcb-xfixes0 \
        libxcb-xinerama0 \
        libxkbcommon0 \
        libxkbcommon-x11-0 \
        libnss3 \
        libnspr4 \
        libx11-6 \
        libx11-xcb1 \
        libxcomposite1 \
        libxdamage1 \
        libxext6 \
        libxfixes3 \
        libxi6 \
        libxrandr2 \
        libxrender1 \
        libxtst6 \
        libasound2 \
        libxkbfile1 \
        python3 \
    ; \
    rm -rf /var/lib/apt/lists/*; \
    \
    case "${TARGETARCH}" in \
      amd64|"") CALIBRE_ARCH=x86_64 ;; \
      arm64)    CALIBRE_ARCH=arm64 ;; \
      *) echo "unsupported TARGETARCH=${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    \
    echo "Installing Calibre ${CALIBRE_VERSION} (${CALIBRE_ARCH}) from official tarball…"; \
    mkdir -p /opt/calibre; \
    curl -fL --retry 5 --retry-delay 3 \
      -o /tmp/calibre.txz \
      "https://download.calibre-ebook.com/${CALIBRE_VERSION}/calibre-${CALIBRE_VERSION}-${CALIBRE_ARCH}.txz"; \
    tar -xJf /tmp/calibre.txz -C /opt/calibre; \
    rm -f /tmp/calibre.txz; \
    \
    # Desktop integration is unnecessary/broken in containers; ignore failures.
    if [ -x /opt/calibre/calibre_postinstall ]; then \
      /opt/calibre/calibre_postinstall --skip-desktop-integration || true; \
    fi; \
    \
    ln -sfn /opt/calibre/calibredb /usr/local/bin/calibredb; \
    ln -sfn /opt/calibre/ebook-meta /usr/local/bin/ebook-meta || true; \
    \
    # Fail the build if calibredb is missing or broken.
    test -x /opt/calibre/calibredb; \
    calibredb --version

WORKDIR /app
COPY --from=builder /out/folio-server /app/folio-server

RUN mkdir -p /data /library /tmp/folio-uploads \
    && chmod +x /app/folio-server

EXPOSE 8090
VOLUME ["/data", "/library"]

ENTRYPOINT ["/app/folio-server"]
