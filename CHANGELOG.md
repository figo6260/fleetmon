# Changelog

## v1.1.0 — 2026-10-24

### Added

- Dashboard search by machine name, ID, or role.
- Dashboard filters: all / online / offline / alert, with alert machines
  sorted first.
- Dashboard summary row: machines online, machines in alert, average CPU,
  average memory, fullest disk, and data time.
- Dashboard trend charts: a fleet CPU chart plus per-machine CPU and memory
  charts (accumulated while the page is open, up to 48 refreshes).
- Dashboard disk panel showing each machine's fullest mount.
- Dashboard one-click refresh (auto-refresh every 30 seconds is unchanged).

## v1.0.0 — Initial public version

- Dependency-free Go agent + server + mobile-first web dashboard.
- Per-machine CPU, memory, disk, top processes, and watched software status.
- Offline alerts after 5 minutes; history kept as daily JSONL, pruned
  after 14 days.
