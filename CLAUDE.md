# Rdio Scanner

## Tech Stack
- **Server**: Go 1.26, single `package main` in `server/`, no ORM (raw `database/sql`)
- **Client**: Angular 21 + Angular Material, TypeScript 5.9, compiled to `server/webapp/` and embedded in the Go binary
- **Databases**: SQLite (default), MySQL/MariaDB, PostgreSQL
- **Key server deps**: `gorilla/websocket`, `golang-jwt/jwt`, `bcrypt`, `fsnotify`, `kardianos/service`, `gopkg.in/ini.v1`

## Build & Run
- **Build client**: `cd client && npm ci && npm run build` (outputs to `server/webapp/`)
- **Build server**: `cd server && go build -o rdio-scanner`
- **Full dist build**: `make linux-amd64` (or `macos-arm64`, `windows-amd64`, etc.)
- **Dev (client)**: `cd client && npm run serve` — proxies to a running server
- **Run server**: `./rdio-scanner` (default port 3000; config via INI file or flags)

## Project Structure
```
server/        Go source (all flat, package main)
client/src/    Angular app
  app/
    components/   reusable UI components
    pages/        route-level page components
    shared/       services, models, shared module
docs/          user-facing documentation
Makefile       cross-platform dist builds + container
Containerfile  Podman/Docker image
```

## Server Architecture
`Controller` is the central hub — instantiated once in `main.go` and passed everywhere.

| File | Responsibility |
|------|---------------|
| `main.go` | HTTP route registration, server startup |
| `controller.go` | Orchestrator; holds all subsystem refs + ingest channel |
| `api.go` | REST endpoints for call upload (SDR recorders) |
| `admin.go` | Admin REST API handlers |
| `client.go` | WebSocket client lifecycle |
| `database.go` | DB connection + abstraction |
| `sqlite.go` / `mysql.go` / `postgresql.go` | DB-specific SQL |
| `migrations.go` | Schema migrations |
| `config.go` | INI config + CLI flags |
| `options.go` | Runtime options (stored in DB) |
| `call.go` | Call data model |
| `dirwatch.go` | fsnotify-based auto-ingest |
| `ffmpeg.go` | Audio transcoding |
| `downstream.go` | Forwarding to downstream instances |
| `scheduler.go` | Scheduled cleanup/maintenance |

## Conventions
- **Commits**: conventional commits — `fix:`, `feat:`, `chore:` prefixes
- **Error handling**: return errors up the call stack; `log.Fatal` only in `main()`
- **No test suite** currently exists
- **WebSocket API** is restricted per `API_ACCESS_POLICY.md` — not a public API
- The Angular client is embedded in the Go binary at build time; static files live in `server/webapp/` (generated, not committed)

## Common Tasks
| Task | How |
|------|-----|
| Add a server endpoint | Register in `main.go`, implement handler on `Admin` or `Api` struct |
| Add a DB-backed feature | Add model file + SQL in the appropriate `*sql.go` file |
| Change admin password | `./rdio-scanner -admin_password <pass>` |
| Update version | `make sed ver=X.Y.Z date=YYYY/MM/DD` |
| Build container image | `make container` (requires Podman) |
