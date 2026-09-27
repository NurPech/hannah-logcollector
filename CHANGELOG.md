# Changelog
<!--
    Placeholder for the next version (at the beginning of the line):
    ## **WORK IN PROGRESS**
-->
## **WORK IN PROGRESS**

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
