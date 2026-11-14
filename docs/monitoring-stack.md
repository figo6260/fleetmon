# 四层监控体系设计：FleetMon 在其中的定位

> 本文描述一套面向个人 / 小团队自有机队的四层监控体系设计思路，
> 以及 FleetMon（资源层）如何通过**资产面板**与资产层打通。
> 文中所有地址、令牌均为占位示例。

## 一、问题：一台机器有四个不同的"状态"

管理多台自有机（云服务器、家里的盒子、Windows 工作机）时，"这台机器现在怎么样"
其实是四个不同的问题，需要四种不同的回答：

| # | 层 | 回答的问题 | 典型实现（任选） |
|---|---|---|---|
| 1 | **资产层** | 这是哪台机器？属于哪个云账号？什么时候到期？ | 自建 inventory API / 云厂商 Describe* 接口聚合 |
| 2 | **存活层** | 从外部看，它的服务端口还通吗？ | 任意探测型监控（HTTP/TCP/ping） |
| 3 | **资源层** | 从内部看，CPU / 内存 / 磁盘 / 进程什么样？ | **FleetMon agent** |
| 4 | **入口层** | 我怎么最快登进去操作？ | SSH 网关 / Web 终端 |

四层互相不可替代：探测层发现"443 不通"却不知道磁盘满了；资源层知道磁盘 95%
却说不出服务是不是对外可访问；资产层告诉你这台机器下周到期，别在上面压新业务。

## 二、FleetMon 的位置：资源层

FleetMon 的 agent 跑在每台机器上，每 60 秒上报 CPU、内存、各挂载点磁盘、
Top 进程和受监视服务的存活状态；服务端聚合为卡片式仪表盘。
**它只回答第 3 层的问题，不试图取代其他层。**

### 推荐的分工边界

```
                    ┌──────────────────────────────┐
                    │        资产层（外部）          │
                    │  账号归属 / 区域 / 计费 / 到期   │
                    └──────────────┬───────────────┘
                                   │ assets_url ← 新增的资产面板
┌──────────────┐    ┌──────────────┴───────────────┐    ┌──────────────┐
│  存活层（外部） │◄───│   FleetMon server + dashboard │    │  入口层        │
│  探测服务端口   │    │   agent 上报 → 卡片仪表盘      │    │  Web 终端/网关 │
└──────────────┘    └───────┬──────────┬───────────┘    └──────────────┘
                            │          │
                    agent ──┘          └── agent
                  （每台机器内部采集）
```

## 三、资产面板：把第 1 层接进 FleetMon

### 数据契约（刻意保持零耦合）

server 配置一个可选的 `assets_url`，指向**任何**能返回 JSON 数组的 inventory 接口：

```json
{
  "assets_url": "https://assets.example.internal/api/inventory",
  "assets_headers": {"Authorization": "Bearer <your-token>"},
  "assets_refresh": 300
}
```

- 响应体接受**裸 JSON 数组**，或带 `{"items":[...]}` 包装的对象
- 数组元素是**任意键值对象**——面板按首次出现的键序动态生成列，不预设字段名
- 典型字段约定（可选）：`provider` / `account` / `name` / `status` / `public_ip` /
  `region` / `instance_type` / `charge_type` / `expired_time`
- `status` 值为 `Running`/`RUNNING` 渲染绿色、`Stopped`/`STOPPED` 渲染琥珀色
- 默认每 300 秒后台拉取一次（`assets_refresh` 可调，最小 30 秒），失败保留上次
  好数据并在面板顶部显示错误横幅；服务进程永不因资产源故障退出

### 一个最小的 inventory 接口示例

任何能返回 JSON 数组的 HTTP 端点都可以，例如自建聚合服务把几个云厂商的
Describe* 结果拼成一个数组：

```json
[
  {"provider":"aliyun","account":"billing-a","name":"app-01","status":"Running",
   "public_ip":"203.0.113.10","region":"cn-hangzhou","expired_time":"2027-01-01"},
  {"provider":"aws","account":"sandbox","name":"web-02","status":"Stopped",
   "public_ip":"198.51.100.7","region":"us-east-1","expired_time":"2026-12-31"}
]
```

（IP/域名均为 RFC 5737 文档占位。）

### 与 agent 的关系

资产面板与 agent 是**两条独立的数据通路**：agent 上报机器内部指标（资源层），
资产源提供账面事实（资产层）。一台没有装 agent 的云主机也会出现在资产面板里，
只是没有资源卡片。

## 四、跨层联动的工作流示例

1. **资产层**：面板显示某台机器月底到期 → 决定"清理还是续费"
2. **存活层**：探测告警某服务不可达 → 打开入口层登进去
3. **资源层（FleetMon）**：磁盘环形仪表 95%、Top 进程一目了然 → 定位到
   某个日志文件膨胀
4. 处理完回资产层确认：这台机器值不值得续

## 五、安全边界建议

- FleetMon 的 agent 只**推**不**拉**，可藏在 NAT / mesh VPN（Tailscale 等）后
- 资产源接口（`assets_url`）只建议指向内网或 VPN 可达的地址；面板鉴权沿用
  FleetMon 自身的 dashboard 登录
- 资产源若需要认证，用 `assets_headers` 携带（如 `Authorization` 头），
  不要把令牌写进 URL
