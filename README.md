# FleetScope

面向多节点环境的原生可观测与变化中心。FleetScope 自带 Agent、可靠传输、时序/事件存储、查询、告警和处置界面，不要求部署 Prometheus、OpenTelemetry Collector、InfluxDB 或其他采集与存储服务。中心不需要 SSH 登录被监控机器。

[![CI](https://github.com/GeneJie199/fleet-observability/actions/workflows/ci.yml/badge.svg)](https://github.com/GeneJie199/fleet-observability/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

## 可交付能力

- 单二进制同时提供 Center、常驻 Agent 和兼容用的一次性 `push`。
- 原生采集 CPU、内存、磁盘、负载、运行时间、网络、进程、Nginx、Redis、PostgreSQL、MySQL 和 Docker Engine；采集器支持独立周期、并发上限、超时和确定性错峰。
- 指标与事件分别使用磁盘队列，断网后按序重传，Center 通过节点/来源序列去重。
- 自带分段时序存储、范围查询、步长聚合、目录、容量上限和运行期保留清理。
- 直接接收原生批次协议，并兼容 Prometheus 文本、Influx Line Protocol 和 OTLP/HTTP JSON；兼容入口只做格式转换，不依赖对应服务。
- 文件日志支持 JSON/文本自动解析、偏移持久化、轮转/截断恢复、结构化属性和分页检索。
- HTTP、TCP、TLS 证书、PostgreSQL、MySQL 探针；数据库凭据只从环境变量读取。
- 接收 InfraScout inventory/drift，聚合主机、进程、端口、服务、容器、网络和卷。
- 用户可配置任意指标的阈值、持续时间、节点/来源/标签范围和级别；规则状态、确认和恢复持久化。
- 自动生成节点健康、失联状态、变化事件和跨节点资源拓扑。
- 资源组贯穿总览、节点、数据库、告警、变化、拓扑和监控覆盖筛选。
- 拓扑支持部署、服务依赖、网络暴露、数据库和故障影响视角；关系保留置信度、证据、发现时间与人工确认审计。
- 监控覆盖页明确显示主机指标、InfraScout 清单/漂移和服务探针空白。
- 告警确认/解决；变化可标记预期、批准、临时允许、禁止或退回审核。
- 指标分析页直接查询内置时序存储；事件流检索原生日志和结构化事件。
- 首次注册使用管理令牌，后续由节点绑定凭证上报；支持凭证撤销/显式轮换、TLS、可选 mTLS、读 API 保护和告警 Webhook。
- 响应式中文 Web 控制台，无 Node/CDN 运行依赖。

## 快速开始

```bash
go test ./...
go build -trimpath -o fleetctl ./cmd/fleetctl

export FLEET_TOKEN='replace-with-a-long-random-token'
./fleetctl serve --addr 127.0.0.1:8770 --data ./fleet-data
```

在另一终端启动 Agent：

```bash
export FLEET_TOKEN='replace-with-a-long-random-token'
./fleetctl agent \
  --center http://127.0.0.1:8770 \
  --node host-01 \
  --once \
  --label environment=test \
  --label version=1.2.3
```

首次启动时，Agent 使用 `FLEET_TOKEN` 注册并在 spool 目录创建权限为 `0600` 的 `agent-credential.json`。之后的指标、事件和节点报告只使用该节点凭证；管理令牌不再随每次上报发送。

打开 `http://127.0.0.1:8770/`。控制台包含总览、节点、指标分析、数据接入、事件流、数据库、告警规则、告警、变化、拓扑和覆盖视图。

## 联动 InfraScout

Agent 可以在每次上报前运行 InfraScout。第一次建立基线，之后检查漂移并上报最新 inventory/drift：

```bash
./fleetctl agent \
  --center http://127.0.0.1:8770 \
  --node host-01 \
  --infrascout /usr/local/bin/infrascout \
  --state-dir /var/lib/infrascout \
  --interval 30s
```

已有文件也可以一次性推送：

```bash
./fleetctl push --center http://127.0.0.1:8770 \
  --node host-01 --inventory inventory.json --drift drift.json
```

## 服务探针

复制并修改 [examples/probes.example.json](examples/probes.example.json)：

```bash
export FLEET_ORDERS_DSN='postgres://monitor:...@127.0.0.1/orders?sslmode=require'
./fleetctl agent --center URL --node host-01 --probes ./probes.json
```

探针结果写入节点 metrics 的 `checks` 数组；数据库摘要同时出现在“数据库”视图。TLS 探针按 `warning_days` / `critical_days` 判断证书剩余时间。

## 原生应用采集

复制并修改 [examples/applications.example.json](examples/applications.example.json)，让 Agent 直接采集应用运行指标：

```bash
export FLEET_REDIS_PASSWORD='replace-with-a-read-only-secret'
export FLEET_ORDERS_DSN='postgres://monitor:...@127.0.0.1/orders?sslmode=require'
./fleetctl agent --center URL --node host-01 \
  --applications /etc/fleetscope/applications.json
```

Nginx 使用 `stub_status`，Redis 使用原生 RESP `INFO`，PostgreSQL/MySQL 使用只读系统统计视图，Docker 直接访问 Engine Unix socket，进程采集读取 Linux procfs。密码、DSN 和认证请求头只能通过 `*_env` 字段引用环境变量；未知字段和配置内明文密码会被拒绝。某个目标失败时 Agent 仍保留同轮其他目标的数据，并写入 `application_target_up=0` 与结构化事件。

## 原生日志

复制并修改 [examples/logs.example.json](examples/logs.example.json)，然后把配置交给同一个 Agent：

```bash
./fleetctl agent --center URL --node host-01 \
  --logs /etc/fleetscope/logs.json \
  --spool-dir /var/lib/fleetscope-agent
```

Agent 会保存每个文件的读取偏移，完整行才会进入独立事件队列；文件被截断或轮转后会从新文件继续读取。`format` 可设为 `json`、`text` 或 `auto`。

## 安全模型

- loopback Center 可用于本机或 SSH 隧道。
- 非 loopback Center 强制要求 `FLEET_TOKEN` 和 `--tls-cert` / `--tls-key`。
- `--client-ca` 开启 mTLS，Agent 通过 `--cert` / `--key` 提交客户端证书。
- 非 loopback 监听会自动保护所有读 API；控制台中的“管理凭据”仅把令牌保存在当前浏览器会话。
- 管理令牌用于管理操作、兼容入口和 Agent 首次注册。原生 Agent 凭证只允许写入其绑定的 `node_id`。
- 用 `--reenroll` 显式轮换当前节点凭证；通过 `DELETE /api/v1/agents/{node}` 撤销离线或失窃节点。
- 数据库监控账户必须只读，DSN 不进入报告 JSON。

## 数据与 API

中心在 `--data` 下保存：

```text
nodes/<node>.json       最新原子节点报告
history/<node>.ndjson   指标历史
telemetry/*.ndjson      原生指标分段
events/*.ndjson         原生日志与事件分段
agents.json             节点凭证哈希、注册和最后活动时间
alerts.json             告警及处理状态
metric-rules.json       用户告警规则
metric-rule-states.json 持续时间和触发状态
changes.json            漂移及人工分类
groups.json             资源组及节点成员
topology-reviews.json   关系确认、处理人和审核说明
```

API 还包括 `/telemetry/query`、`/telemetry/catalog`、`/events`、`/rules`、`/agents` 和三个兼容接入入口。完整协议、应用采集指标与容量语义见 [docs/native-monitoring.md](docs/native-monitoring.md)。

## 安装与运维

```bash
sudo sh ./scripts/install.sh ./fleetctl
```

Center/Agent systemd 单元、TLS、环境文件和备份说明见 [docs/operations.md](docs/operations.md)。

## 产品边界

- 不执行远程命令，不自动修复机器，不自动批准漂移。
- 当前内置分段存储面向单 Center 的小中型节点集；容量上限、保留周期和备份窗口必须按 [运维指南](docs/operations.md) 配置。兼容主流数据格式不表示运行时依赖对应采集器或存储。

Apache-2.0。贡献流程见 [CONTRIBUTING.md](CONTRIBUTING.md)，安全问题见 [SECURITY.md](SECURITY.md)。
