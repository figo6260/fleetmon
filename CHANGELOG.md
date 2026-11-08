# Changelog

## v1.5.0 — 2026-11-08

### Changed

- Dashboard is now a fluid widescreen layout: the page no longer caps
  at 1180 px — machine cards reflow with `repeat(auto-fill,
  minmax(370px, 1fr))`, so a wide monitor shows 4–7 columns instead of
  3, and phones still get a single column.
- Fixed the horizontal overflow that forced sideways scrolling on wide
  screens: disk-usage values may now wrap instead of forcing their card
  wider, grid items and toolbar/search inputs can shrink (`min-width:
  0`), and `overflow-x: clip` on the page guarantees no horizontal
  scrollbar remains.
- The dashboard header shows the running version (v1.5.0) next to the
  logout link, so the deployed version is visible at a glance.

## v1.4.0 — 2026-10-29

### Added

- Agent now reports recently started processes ("最近启动的任务"):
  anything started within the last 15 minutes, with command line and
  age — installers, builds, and code runs are visible even when they are
  not CPU-heavy (Linux + Windows).
- Top-process list expanded from 5 to 12 entries.

### Changed

- Dashboard: "主要工作任务" shows the top 12 by CPU; a new
  "最近启动的任务" section lists recently started processes with their
  command line and start age.

## v1.3.0 — 2026-10-24

### Added

- Dashboard sort: CPU high to low, memory high to low, fullest disk
  high to low (machines with no data or offline sort last).
- Dashboard drag-to-reorder: drag any machine card to move it;
  dragging switches to manual sort, and both the manual order and the
  sort choice are saved in the browser (localStorage).

## v1.2.0 — 2026-10-24

### Changed

- Dashboard restyled to gauge panels, replacing the v1.1.0 chart wall:
  each machine shows CPU, memory, and fullest-disk ring gauges (blue /
  purple / orange; red at 90% or above, gray when offline), a taller CPU
  trend line, and the same disk, top-process, and service sections.
- Dashboard search, filters (all / online / offline / alert, alert first),
  summary row, and one-click refresh are unchanged.

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
