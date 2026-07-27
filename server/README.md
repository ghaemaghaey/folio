# Folio Server

Lightweight HTTP API for **multi-device reading progress** and library metadata.

This is intentionally small: JSON file storage, no database required. Desktop and Android clients can sync positions here later.

## Run

```bash
cd server
go run .
# listens on :8787 by default
```

Options:

| Flag / env | Default | Description |
| --- | --- | --- |
| `-addr` / `FOLIO_ADDR` | `:8787` | Listen address |
| `-data` / `FOLIO_DATA` | `./data` | JSON store directory |

## API

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/health` | Liveness |
| `GET` | `/v1/meta` | Service metadata |
| `GET` | `/v1/books` | List books |
| `GET` | `/v1/books/{id}` | Get book |
| `PUT` | `/v1/books/{id}` | Upsert book metadata |
| `DELETE` | `/v1/books/{id}` | Remove book + progress |
| `GET` | `/v1/books/{id}/progress` | Get reading position |
| `PUT` | `/v1/books/{id}/progress` | Save reading position |

### Progress body example

```json
{
  "page": 12,
  "chapter": 2,
  "subPage": 0,
  "scroll": 0.35,
  "device": "android"
}
```

## Build

```bash
go build -o folio-server .
```
