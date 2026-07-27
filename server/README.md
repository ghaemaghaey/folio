# folio-server

Combined **library writer** + **v1 backend API** for Folio.

One binary / one container that:

1. Accepts book uploads and writes them into a shared **Calibre** library via `calibredb`
2. Manages **user accounts** (bcrypt + JWT) and **reading positions** in SQLite

## Endpoints

| Method | Path | Auth | Description |
| --- | --- | --- | --- |
| `GET` | `/health` | no | Liveness |
| `POST` | `/register` | no | `{username,password}` → JWT |
| `POST` | `/login` | no | `{username,password}` → JWT |
| `POST` | `/books/upload` | yes | multipart `file` (+ optional `title`, `author`) |
| `GET` | `/books` | yes | All local books (join against OPDS client-side) |
| `GET` | `/books/{fingerprint}` | yes | Single book by SHA-256 fingerprint |
| `POST` | `/progress` | yes | `{fingerprint, position}` upsert |
| `GET` | `/progress` | yes | All positions for current user |
| `GET` | `/progress/{fingerprint}` | yes | One position |

Auth header: `Authorization: Bearer <token>`.

## Environment

| Variable | Default | Description |
| --- | --- | --- |
| `LISTEN_ADDR` | `:8090` | HTTP listen address |
| `DB_PATH` | `./data/folio.db` | SQLite file path |
| `JWT_SECRET` | `change-me-in-production` | HS256 signing secret |
| `CALIBRE_LIBRARY_PATH` | _(empty)_ | Calibre library dir for `calibredb --with-library` |
| `CALIBREDB_BIN` | `calibredb` | Path to calibredb if not on `PATH` |

When `CALIBRE_LIBRARY_PATH` is empty, uploads still store metadata + fingerprint but skip `calibredb` (useful for local API testing).

## Run locally (no Docker)

```bash
cd server
go run .

# or
go build -o folio-server .
./folio-server
```

```bash
export JWT_SECRET=dev-secret
export DB_PATH=./data/folio.db
# optional:
export CALIBRE_LIBRARY_PATH=/path/to/Calibre\ Library
```

## Docker

```bash
cd server
export JWT_SECRET=super-secret
export CALIBRE_LIBRARY_HOST_PATH=/host/path/to/calibre/library
docker compose up -d --build
```

Volumes:

- `/data` — SQLite (`folio.db`)
- `/library` — shared Calibre library (same as calibre-web)

The image installs Calibre via the official linux installer so `calibredb` is available. The Go binary is built with `CGO_ENABLED=0` and `modernc.org/sqlite` (no cgo).

## curl smoke test

```bash
BASE=http://127.0.0.1:8090

# Register
curl -sS -X POST "$BASE/register" -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"secret12"}' | tee /tmp/reg.json

TOKEN=$(python -c "import json;print(json.load(open('/tmp/reg.json'))['token'])")
# PowerShell: $TOKEN = (Invoke-RestMethod ...).token

# Login
curl -sS -X POST "$BASE/login" -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"secret12"}'

# Upload a book (field name: file)
curl -sS -X POST "$BASE/books/upload" \
  -H "Authorization: Bearer $TOKEN" \
  -F "file=@./testdata/sample.epub" \
  -F "title=Sample Book"

# List books
curl -sS "$BASE/books" -H "Authorization: Bearer $TOKEN"

# Progress
FP=<fingerprint from upload response>
curl -sS -X POST "$BASE/progress" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"fingerprint\":\"$FP\",\"position\":\"page:12\"}"

curl -sS "$BASE/progress" -H "Authorization: Bearer $TOKEN"
curl -sS "$BASE/progress/$FP" -H "Authorization: Bearer $TOKEN"
```

After a successful upload with `CALIBRE_LIBRARY_PATH` set, the book should show up in calibre-web’s OPDS feed (refresh / newest).

## Schema

See migrations in [`internal/db/db.go`](internal/db/db.go) — `users`, `books` (fingerprint PK), `reading_positions`.

## Dedup

Upload computes **SHA-256** of the file. If that fingerprint already exists, the handler returns the existing row with `"deduped": true` and does **not** call `calibredb add` again.
