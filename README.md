# hannah-logcollector

Optional log collector for the [Hannah](https://dev.kernstock.net/gessinger/voice/hannah) voice assistant — a flight recorder, not a log platform.

Hannah's components ship their log lines to the collector; it keeps them for a limited time and, on request (e.g. the "Collect Logs" button in the WebUI), hands them out as a single archive you can attach to a bug report. Everything stays on your own installation.

Deliberately small: ingest, bounded storage, export. No search, no query UI, no dashboards — if you want those, use a real log platform such as Loki.

## How it works

1. On startup the collector registers with Hannah Core (`LogCollectorConnect`).
2. Hannah announces its address to all connected components (`SubscribeInfrastructure`), so they need no configuration of their own.
3. Components stream their log lines to the collector (`LogService.Ship`). Lines logged before the collector was reachable are buffered by the component and sent with their original timestamps.
4. An export (`LogService.Export`) is a `tar.gz` with one plain-text file per component and a `manifest.json` (versions, time range, gaps where a component had to drop lines, excluded categories).

Speech transcripts and metadata (room/device names, presence) are tagged per line and can be left out of an export — useful before posting logs publicly. Secrets are filtered out by the components before they ever reach the collector.

Without a collector, nothing changes: Hannah works exactly as before.

## Retention

Whichever limit is reached first wins:

| Setting | Default | Env var |
|---|---|---|
| `retention.days` | 7 | `HANNAH_LOGCOLLECTOR_RETENTION_DAYS` |
| `retention.max_size_mb` | 256 | `HANNAH_LOGCOLLECTOR_RETENTION_MAX_SIZE_MB` |

For orientation: a typical installation produces a few MB of logs per day, so 7 days usually take well below 100 MB. The size limit is a safety net against a component flooding the log.

## Running it

### Docker

```bash
docker run -d --name hannah-logcollector \
  -p 50060:50060 \
  -v hannah-logcollector-data:/app/data \
  -e HANNAH_LOGCOLLECTOR_HANNAH_ADDRESS=hannah:50051 \
  registry.dev.kernstock.net/gessinger/voice/hannah-logcollector:latest
```

If the components can't reach the collector under the address it connects to Hannah from (e.g. because of a Docker bridge network), set `HANNAH_LOGCOLLECTOR_SERVER_ADVERTISE_HOST` to an address they can reach.

### Native (systemd)

Release binaries for amd64 and arm64 are published on the Hannah Update Server. [deploy/install.sh](deploy/install.sh) downloads the matching one, installs it to `/usr/local/bin` and sets it up as a systemd service running as user `hannah`:

```bash
sudo UPDATE_SERVER_URL=... UPDATE_SERVER_TOKEN=... ./deploy/install.sh
```

Put your config at `/etc/hannah-logcollector/config.yaml` with `db.path` under `/opt/hannah/logcollector` — the service unit only allows writes there. Running the script again updates to the latest release; `--uninstall` removes service and binary but keeps config and logs.

### Configuration

Every setting can be given in a YAML file (`--config path/to/config.yaml`, see [config.example.yaml](config.example.yaml)) and overridden via environment variable. The config file is optional.

## Development

```bash
go build ./...
go test ./...
go vet ./...
```
