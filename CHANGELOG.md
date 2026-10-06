# Changelog
<!--
    Placeholder for the next version (at the beginning of the line):
    ## **WORK IN PROGRESS**
-->
## **WORK IN PROGRESS**

## 0.6.0
* Added: the Collector can receive syslog (RFC 5424 over UDP) from the satellites. Set `syslog.listen` (for example `:5514`, env `HANNAH_LOGCOLLECTOR_SYSLOG_LISTEN`), it is off by default. Every satellite appears as the component `hannah-esp` with its device ID as the instance, a line without a timestamp gets the time it arrived, and a datagram that is no RFC 5424 message is dropped without stopping the receiver. The port is announced to Hannah along with the gRPC one, so Hannah can tell the satellites where to send their logs; a Hannah Core on `hannah.v1` doesn't get it. Requires `hannah-proto-go` 5.4.0. `hannah-logcollector#12`
* Added: the Collector can pass every entry on to a syslog receiver, for example Alloy's `loki.source.syslog`, so the logs of all components arrive in Loki through the Collector alone. Set `forward.address` (`host:port`, env `HANNAH_LOGCOLLECTOR_FORWARD_ADDRESS`), `forward.protocol` is `tcp` (default) or `udp`; it is off by default. Entries from the components and from the satellites' syslog both go out as RFC 5424 with their own timestamp, the instance as hostname and the component as app name, the severity of the level, and category and logger as structured data. Over TCP the messages are octet-counted, so a message with line breaks stays one. If the receiver is not reachable the entries wait in a queue of 10,000 and the oldest are dropped when it is full — the receiver never slows the Collector down, and the log and the export are not affected. A message longer than about 7,000 bytes is cut. `hannah-logcollector#13`

## 0.5.0
* Added: the Collector ships its own logs like every other component, into its own store. They show up as the source `logcollector` in a log export. While Hannah Core is down they still arrive, because the Collector sends them to itself directly
* Fixed: the Collector now really names itself and its version in every call to Hannah Core and tells Core every 30 seconds that it is running. 0.4.2 announced that, but did not do it, so Core did not know the Collector as a running component

## 0.4.2
* Changed: the Collector names itself and its version in every call to Hannah Core and tells Core every 30 seconds that it is running, so Core still knows it when it holds no open connection. Two Proxies show up as two instances. Needs a Core that knows the call, an older Core is left alone after one log line. Requires `hannah-grpc-lib` 0.8 (Go)

## 0.4.1
* Fixed: the release archive now ships a `hannah.info`, so AutoDeploy keeps `config.example.yaml` and `hannah-logcollector.service` out of a shared install directory such as `/usr/local/bin`. Copies left there by earlier updates are not removed automatically and can be deleted by hand

## 0.4.0
* Changed: the collector registers with Hannah Core over `hannah.v2`. Against a Hannah Core too old for it, it falls back to `hannah.v1` on its own and logs once per connection that Hannah Core should be updated
* Changed: the collector serves its log API under `hannah.v2` and `hannah.v1`, so components keep shipping logs whether their logging library already uses `hannah.v2` or still `hannah.v1`
* Removed: the unversioned log API (`hannah.LogService`). Components that still ship to it need their logging library updated to a version that speaks `hannah.v1` or `hannah.v2`

## 0.3.1
* Chore: updated `hannah-proto-go` to v4.6.3 and `hannah-grpc-lib` to v0.4.0, no functional change

## 0.3.0
* Changed: the collector registers with Hannah Core over its versioned API `hannah.v1`. Against a Hannah Core too old for it, it falls back to the previous API on its own and logs once per connection that Hannah Core should be updated
* Changed: the collector serves its log API under `hannah.v1` as well as under the previous, unversioned name, so components keep shipping logs whether their logging library already uses `hannah.v1` or not
* Changed: the "Hannah connection lost, reconnecting" warning now includes the Hannah address it tried, so a mistyped `hannah.address` is visible without digging out the startup line

## 0.2.4
* Fixed: log exports failed with `disk I/O error (6410)` once the database had grown large enough. SQLite couldn't find a writable directory for its temp files under the systemd unit's filesystem protection (and the container image has none at all). Its temp files now live next to the database, like the export's own temp files

## 0.2.3
* Fixed: `install.sh` aborted with `BASH_SOURCE[0]: unbound variable` when piped into bash (`curl ... | bash`), right before installing the systemd unit. The fallback to a unit file next to the script is now only used when the script runs from a file

## 0.2.2
* Fixed: the container image on quay.io was only published per architecture (`<version>-amd64`/`-arm64`), so `quay.io/m1kad0/hannah-logcollector:latest` and `:<version>` didn't exist. Both are now published as multi-arch images
* Fixed: `install.sh` aborted right after installing the binary on hosts without `TMPDIR` set (the usual case on Linux), so neither the config template nor the systemd unit got installed

## 0.2.1
* Fixed: include config.example.yaml in release

## 0.2.0
* Added: First public release. Mirror to github.com and publishing container to quay.io

## 0.1.2
* Fixed: `install.sh` enabled and started the service even without a `config.yaml`, leaving it in a restart loop while reporting it as running. Like the other Hannah installers, it now stops after installing and tells you to place the config and run `systemctl enable --now hannah-logcollector` (#5)

## 0.1.1
* Fixed: `install.sh` failed on a fresh host when run on its own, because the systemd unit wasn't part of the release archive. The archive now contains the unit, and `install.sh` installs it from there (#4)

## 0.1.0
* Added: first version of the log collector. Hannah components ship their logs to it, it keeps them for a limited time (7 days and at most 256 MB by default, whichever is reached first) and hands them out as a single archive for bug reports: one plain-text file per component plus a `manifest.json` listing versions, time range and any gaps where a component had to drop lines. Speech transcripts and room/presence details can be left out of an export. The collector registers with Hannah automatically, so components find it without any configuration of their own (#1)
* Added: native installation without Docker — release binaries (amd64/arm64) are published on the Hannah Update Server, and `deploy/install.sh` installs or updates the collector as a systemd service (#2)
