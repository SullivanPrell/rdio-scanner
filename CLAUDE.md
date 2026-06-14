# Rdio Scanner

## Tech Stack
- **Server**: Go 1.26, single `package main` in `server/`, no ORM (raw `database/sql`)
- **Client**: Nuxt 4 + Nuxt UI v4 + Vue 3 + Tailwind CSS v4, TypeScript, compiled to `server/webapp/` and embedded in the Go binary
- **Databases**: SQLite (default), MySQL/MariaDB, PostgreSQL
- **Key server deps**: `gorilla/websocket`, `golang-jwt/jwt`, `bcrypt`, `fsnotify`, `kardianos/service`, `gopkg.in/ini.v1`
- **SDR integration**: SDRangel (analog/digital via REST+bridge), trunk-recorder (P25 trunked)

## Build & Run
- **Build client**: `cd client-nuxt && yarn install && yarn build` (outputs to `server/webapp/`)
- **Build server**: `cd server && go build -o rdio-scanner`
- **Full dist build**: `make linux-amd64` (or `macos-arm64`, `windows-amd64`, etc.)
- **Dev (client)**: `cd client-nuxt && yarn dev` — proxies `/api` and WebSocket to a running server on :3000
- **Run server**: `./rdio-scanner` (default port 3000; config via INI file or flags)

## Project Structure
```
server/           Go source (all flat, package main)
  trunkrecorder.go  trunk-recorder config gen + RTL dongle detection
  setup.go          SDRangel REST provisioning
  bridge.go         SDRangel UDP audio bridge
  radioreference.go CHIRP/RR/TRS CSV importers + RR SOAP API
client-nuxt/      Nuxt 4 app (replaces client/)
  app/
    composables/  useRdioScanner.ts (WS+audio), useAdmin.ts (REST)
    components/   Scanner/* (display/controls/select/search), Admin/* (config forms)
    pages/        index.vue (scanner), admin/* (admin panel)
    layouts/      default.vue, admin.vue
  nuxt.config.ts
docs/             user-facing documentation
Makefile          cross-platform dist builds + container
Containerfile     Podman/Docker image
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
- The Nuxt client is embedded in the Go binary at build time; static files live in `server/webapp/` (generated, not committed)
- `client/` (Angular) is superseded — do not add to it; all new frontend work goes in `client-nuxt/`

## Common Tasks
| Task | How |
|------|-----|
| Add a server endpoint | Register in `main.go`, implement handler on `Admin` or `Api` struct |
| Add a DB-backed feature | Add model file + SQL in the appropriate `*sql.go` file |
| Add a frontend page | Create `client-nuxt/app/pages/...vue`, use `useRdioScanner` or `useAdmin` composables |
| Add a frontend component | Create `client-nuxt/app/components/...vue`, use Nuxt UI (`UButton`, `UInput`, etc.) |
| Change admin password | `./rdio-scanner -admin_password <pass>` |
| Update version | `make sed ver=X.Y.Z date=YYYY/MM/DD` |
| Build container image | `make container` (requires Podman) |
