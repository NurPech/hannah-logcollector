# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`hannah-logcollector` is a standalone Go service that collects the logs of all "Hannah" components and exports them as one archive for bug reports (the "Collect Logs" button, hannah#265). It is a flight recorder, not a log platform: ingest, bounded storage, export — **no search/query API, ever**. It serves its own gRPC `LogService` (`Ship`, `GetSources`, `Export`) and additionally connects *outbound* to Hannah Core via `LogCollectorConnect`, so Core can announce the collector's address to all components via `SubscribeInfrastructure`.

## Commands

```bash
go build ./...
go test ./...
go vet ./...
```

On Leonie's dev PC a group policy blocks executables from `%TEMP%`, which is where `go test` puts its test binaries. Run the tests in the same image the CI uses instead:

```powershell
docker run --rm -v "C:\Users\rene\git\hannah-logcollector:/src" -v "C:\Users\rene\go\pkg\mod:/go/pkg/mod" -w /src golang:1.26-alpine go test ./...
```

Cutting a release works exactly like in hannah-timer (`scripts/release.js`, dry-run first, then `--yes`).

## Architecture

### Proto dependency

The wire schema is the published Go module `github.com/NurPech/hannah-proto-go/v4` (imported as `pb`), generated upstream from `hannah-proto` (`hannah/logging.proto`, `hannah/infrastructure.proto`). Never generate or hand-edit proto code here, only bump the module version.

### Package layout (`internal/`)

- **`config`** — YAML config with env var overrides (`HANNAH_LOGCOLLECTOR_<SECTION>_<KEY>`), defaults in `defaults()`.
- **`store`** — SQLite persistence (`modernc.org/sqlite`, pure Go / no CGO, single connection). Tables `sources` (component/instance/version), `entries`, `gaps`. `auto_vacuum = INCREMENTAL` is set before the first table is created so `Prune` can hand freed pages back to the filesystem. `Prune` deletes by age first, then the oldest entries until the *used* size (page count minus free pages) fits the limit. `Writer` batches entries from all `Ship` streams into one transaction per batch; `Sync` waits until everything queued so far is written (used at the end of a `Ship` stream and before an export).
- **`export`** — builds the `tar.gz`: one `<component>-<instance>.log` per source plus `manifest.json`. Per-source files are staged in temp files because tar needs each file's size before its content.
- **`server`** — the `LogService` implementation. `Export` builds the archive into a temp file first (so a slow client never holds the database), then streams it in 256 KB chunks.
- **`hannah`** — keeps the `LogCollectorConnect` stream open and reconnects with backoff (1s → 30s, reset once a registration was acknowledged). Sends `x-proto-version` like all Hannah clients.

### Invariants

- **Temp files live next to the database** (`filepath.Dir(db.path)`), never in `os.TempDir()` — the container image is `scratch` and has no `/tmp`.
- **Level and category are stored as raw proto enum values**; the store doesn't interpret them.
- **Secrets are filtered by the shipping components**, not here. The collector trusts what it receives.
- **No auth yet** on `LogService`: it comes with Hannah's PSK auth (hannah#28) for all services at once.

## GitLab workflow

GitLab project: `gessinger/voice/hannah-logcollector`, instance `dev.kernstock.net`, reachable via the `mcp__gitlab-private-voice__*` MCP tools. Same rules as hannah-timer:

1. Never commit directly on `master`.
2. Every functional change needs a work item; branch `feature/...`, `bug/...`, `chore/...`.
3. Logical commits, functional ones reference `Refs #<iid>`.
4. `CHANGELOG.md` alongside the change (English, `## **WORK IN PROGRESS**`).
5. MR with `Closes #<iid>`.

## Deployment

Tag pipelines cross-compile static binaries (amd64 + arm64), upload them plus release notes to the **Hannah Update Server** (channels `logcollector-stable-amd64`/`-arm64`, via the `upload-artifact`/`upload-notes` components, which use the group CI/CD variables `HANNAH_UPDATE_BASE_URL`/`HANNAH_UPDATE_UPLOAD_TOKEN`) and build a multi-arch container image into the **GitLab registry**. Deliberately no quay.io and no public mirror sync yet.

Native installs use `deploy/install.sh` + `deploy/hannah-logcollector.service` (user `hannah`, `ProtectSystem=strict`, `ReadWritePaths=/opt/hannah/logcollector` — `db.path` must live there, export temp files are written next to it). The release archive contains `hannah-logcollector` (binary) and `hannah-logcollector.service`, staged by the build job under `dist/<arch>/` and packed in `tar-dir` mode; `install.sh` expects exactly these names and installs the unit from the archive (falling back to one next to the script).
