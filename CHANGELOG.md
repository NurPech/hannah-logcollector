# Changelog
<!--
    Placeholder for the next version (at the beginning of the line):
    ## **WORK IN PROGRESS**
-->
## **WORK IN PROGRESS**

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
