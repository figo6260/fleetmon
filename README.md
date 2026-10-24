# FleetMon

A tiny, dependency-free fleet monitor for your own machines: one Go binary that
works as a lightweight **agent** on each computer and as a **central server**
with a mobile-first web dashboard.

## Features

**Monitoring (agent, every 60 seconds by default)**

- CPU, memory, and disk usage per mount
- Top 5 processes by CPU — the machine's "main tasks"
- Watched service checks: process name, TCP port, HTTP status, systemd
  unit / Windows service
- One config file per machine; Linux and Windows agents share one codebase

**Dashboard (mobile-first, Basic Auth)**

- One card per machine: online/offline status with "last seen" age, CPU /
  memory / fullest-disk ring gauges (blue / purple / orange; red at 90%
  or above, gray when offline), and a CPU trend line
- Per-machine disk, top-process, and watched-service sections
- Search by machine name, ID, or role; filter by all / online / offline /
  alert, with alert machines sorted first
- Sort by CPU, memory, or fullest disk (high to low); machines with no
  data or offline sort last
- Drag any machine card to reorder it manually; dragging switches back
  to manual sort, and the order and the sort choice are saved in the
  browser (localStorage) so they survive refresh
- Summary row: machines online, machines in alert, average CPU, fullest
  disk, and data time
- One-click refresh; the page still auto-refreshes every 30 seconds

**Alerts, history, and API**

- Machines offline for more than 5 minutes raise an alert: a dashboard
  banner plus an `alert` flag in the state API, so an external notifier
  (email, chat bot, script) can pick it up
- Machines without an agent can still be watched with an external HTTP
  probe
- History kept as daily JSONL files, auto-pruned after 14 days
- `GET /api/state` returns the same JSON the dashboard renders

**Deployment**

- No third-party dependencies — Go standard library only; one static
  binary (~9 MB), ~15 MB RAM for the agent
- Agents only push to the server (Bearer token), so they work behind NAT;
  run over a LAN or a mesh VPN (Tailscale / Headscale / WireGuard)
- systemd units for agent and server in `examples/`

中文说明见下方。

## Architecture

```
 agent (each machine)                server (one machine)
 ┌──────────────────┐   POST /api/report   ┌────────────────────┐
 │ CPU / mem / disk │ ───── Bearer token ─▶│ state + history    │
 │ top processes    │      (over your      │ probe loop         │
 │ service checks   │   LAN / VPN / mesh)  │ dashboard + API    │
 └──────────────────┘                      └────────────────────┘
                                                  ▲
                                     phone/browser ┘ (Basic Auth)
```

Agents only *push*; the server never connects to agents, so agents can live
behind NAT. Use it over a private network or mesh VPN (e.g. Tailscale /
Headscale / WireGuard) — do not expose the report port to the public internet
without additional protection.

## Build

```sh
go build -o fleetmon .               # current platform
GOOS=windows go build -o fleetmon.exe .   # Windows agent
```

Requires Go 1.23+.

## Run the server

```sh
cp examples/server.example.json server.json   # edit: token, password hash, machines
./fleetmon server server.json
```

- `token`: shared secret agents must present (`openssl rand -hex 16`).
- `web_pass_sha256`: hex SHA-256 of the dashboard password
  (`printf %s 'your-password' | sha256sum`).
- `machines`: the fleet registry. Entries with a `probe` URL get agentless
  HTTP probing as a fallback (skipped while a real agent is reporting).

Dashboard: `http://<server>:8931/` (Basic Auth). For public access, put it
behind a reverse proxy with HTTPS, e.g. nginx `location /fleet/` →
`proxy_pass http://127.0.0.1:8931/;`.

## Run an agent

```sh
cp examples/agent.example.json agent.json     # edit per machine
./fleetmon agent agent.json
```

Service check kinds:

| kind      | fields            | meaning                                  |
|-----------|-------------------|------------------------------------------|
| `process` | `match`           | a process whose name/cmdline contains it |
| `port`    | `port`            | TCP connect to 127.0.0.1:port succeeds  |
| `http`    | `url`, `expect`   | GET returns the expected status (200)    |
| `systemd` | `unit`            | unit is active (Windows: service Running)|

systemd units for both modes are in `examples/`.

## State API

`GET /api/state` (Basic Auth) returns `{now, machines:[{info, report,
last_seen, online, alert}]}` — the same JSON the dashboard renders.

## 中文说明

FleetMon 是一个极简的自有机队监控：一份 Go 代码、零第三方依赖，同一个
二进制既当探针（装在每台电脑上）又当中央服务（带一个手机优先的网页
管理台）。

每台电脑一张卡，直观看到：在线状态、CPU、内存、各盘可用空间、占用
最高的 5 个程序（主要工作任务）、关键软件在线情况。管理台（v1.3.0）
有仪表盘、搜索与筛选、CPU／内存／磁盘最满三种排序、卡片拖动手动排序
（顺序和排序选择都存在浏览器里，刷新还在）、顶部统计、CPU 走势。没装
探针的机器可用外部 HTTP 探测兜底。离线超 5 分钟触发告警标记（管理台
横幅＋状态接口的 alert 标记，可接外部邮件／机器人通知）；历史数据按天
存 JSONL，14 天自动清理。

构建与运行见上方英文部分；示例配置在 `examples/` 目录。

## Author

figo6260 — https://github.com/figo6260

## License

MIT — see [LICENSE](LICENSE).
