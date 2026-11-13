# Changelog

## v1.6.1 — 2026-11-12

### Added

- Login failure lockout on the dashboard: failed logins are counted
  per source IP over a rolling 10-minute window, and 5 failures lock
  that IP out for 30 minutes. While locked, every login from that IP
  is refused (HTTP 429 with an explanatory message) — even with the
  correct password. A successful login clears the counter, different
  IPs never affect each other, established sessions and Bearer-token
  automation are untouched, and lock events are written to the server
  log for audit. The accounting uses the real visitor address: on the
  production path (la-hub nginx -> reverse tunnel) nginx force-sets
  `X-Real-IP`, which the server trusts only when the connection
  arrives via the local tunnel endpoint; direct connections are
  accounted to their true peer address. Counts are kept in memory
  only, so a restart resets them.

## v1.6.0 — 2026-11-12

### Added

- Change the dashboard password from the dashboard itself: a
  "修改密码" link next to logout opens a form (current password, new
  password, confirmation) backed by an authenticated
  `POST /api/password` endpoint. The endpoint verifies the current
  password, enforces an 8–64 character new password, and persists the
  new hash back to the server config atomically (temp file + rename,
  0600, no other field touched). Because the session key derives from
  the password hash, all existing sessions are invalidated by a
  password change and the user is sent back to the login page.
- Rename machines from the dashboard: every machine card gets a
  "改名" link that opens a dialog (display name + optional
  description), backed by an authenticated
  `POST /api/machines/{id}/rename` endpoint. The rename is a
  display-layer override only — stored as `display_name` / `desc` on
  the machine's registry entry in the server config (same atomic
  write-back), it takes effect in `/api/state` immediately, survives
  restarts, and never touches the machine id (the agent's identity
  key) or any agent's own config. Each rename is written to the
  server log as an audit entry.

### Notes

- The two features above were developed as v1.5.1 and v1.5.2 (see
  the entries below) and are released together as v1.6.0.

## v1.5.2 — 2026-11-12

### Added

- Rename machines from the dashboard: every machine card gets a "改名"
  link that opens a dialog (display name + optional description),
  backed by a new authenticated `POST /api/machines/{id}/rename`
  endpoint. The rename is a display-layer override only — it is stored
  as `display_name` / `desc` on the machine's registry entry in the
  server config (written back atomically, temp file + rename, 0600,
  other fields and entries preserved) and takes effect in `/api/state`
  immediately, no restart needed. The machine id — the agent's
  identity key — and every agent's own config are never touched.
  Display values resolve as: rename override → registry name/role →
  agent-reported name/role. Validation: the machine must exist (404),
  display name is required, at most 40 characters, no control
  characters or angle brackets; description at most 80 characters.
  The dialog is a modal overlay outside the auto-refreshed grid, so
  the 30-second refresh cannot wipe a half-filled form.

## v1.5.1 — 2026-11-12

### Added

- Change-password in the dashboard: a "修改密码" link next to logout
  opens a form (current password, new password, confirmation) backed by
  a new authenticated `POST /api/password` endpoint. The endpoint
  verifies the current password against `web_pass_sha256`, enforces an
  8–64 character new password, and persists the new hash back to the
  config file the server was started with — atomically (temp file +
  rename, 0600) and without touching any other field. Because the
  session cookie key is derived from the password hash, all existing
  sessions are invalidated by a password change and the user is sent
  back to the login page.

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
