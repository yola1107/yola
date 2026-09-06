# Ludo Kratos Service

Ludo 是运行在 Yola Node transport 上的 Stateful Kratos 游戏服务。当前依赖方向为：

```text
cmd -> server -> service -> biz <- data
                              |
                              v
                       table.Manager
                              |
                     mailbox(tableID)
```

`api/v1` 定义 `GameServer` 和 command 注册映射；`server` 在一个构造函数内创建 `node.Server`、注入 service，并把它作为唯一 Kratos transport；内部 gRPC 不进入应用装配。`service` 统一安装 command、Push 和 Disconnect adapter，把 `node.Session` 收窄为业务需要的能力；`biz` 不依赖 `yola/node`，只编排玩家生命周期。`table.Manager` 持有 Table 集合和 mailbox；单桌规则只由对应 mailbox 写入。

## 启动与配置

从 `test` module 根目录运行：

```bash
go run ./ludo/cmd/ludo-server -conf ./ludo/configs -id ludo-1
```

`-conf` 可指向 YAML 文件或目录；默认配置是 `ludo/configs/config.yaml`。Redis、etcd、gRPC、日志和房间参数分别位于 `data`、`server.grpc`、`log`、`room`。Tracked YAML 不保存口令；Redis 开启认证时，在仓库外的配置副本中设置 `data.redis.password`，再用 `-conf` 加载。客户端会在启动阶段执行 `PING`，缺少密码会直接返回 `NOAUTH`。

同一集群中的 `-id` 必须在线唯一，且多实例需要分别配置 gRPC 地址。该值同时作为 Kratos instance ID 与桌日志实例目录标识。

tracked 配置为 100 张桌、每桌 4 个座位，压测目标为 20。`loadTest.press` 同时声明 Gateway 总连接、单来源 IP 与 Node 可用座位；加载配置时先做容量 preflight，目标超过任一声明上限就拒绝启动。`nodeSeatCapacity` 必须填写扣除 Robot、残留玩家和保留容量后的实际可用座位。

## 当前游戏流程

```text
Login -> Scene -> Ready -> SendCard -> Dice -> Move
      -> ResultPush -> Result -> Wait -> 下一局
Logout -> Table mailbox 移出 -> Save -> Unbind -> 删除 Player
```

每桌 FIFO、跨桌并行；Table mailbox 由固定 worker 共享。`FastMode` 下 Dice/Move 阶段各 5s，SendCard/Result 各 3s（`test/ludo/internal/biz/table/define.go:23`）。同步 Push 由 Node 统一限制，默认最多等待 3s；多个慢 Push 可能占用共享 worker，需在真实网络压测中观测。

## 当前 `press` 入口

压测客户端读取同一配置源中的 `loadTest.press`：

```bash
go run ./ludo/cmd/ludo-client -conf /absolute/path/to/ludo-pressure.yaml -log-level info
```

建议使用仓库外的最小配置，不要为了压测修改或提交 tracked 配置：

```yaml
room:
  game:
    min_money: 100.0
    max_money: 10000.0

loadTest:
  press:
    open: true
    scenario: play
    url: ws://127.0.0.1:3102/
    num: 1000
    batch: [100, 100]
    interval: 1000
    startID: 600000
    uidCount: 100000
    concurrency: 1000
    actionConcurrency: 1000
    gatewayLimit: 10000
    gatewayPerIPLimit: 10000
    nodeSeatCapacity: 40000
    minMoney: 100.0
    maxMoney: 10000.0
    logoutRate: 0
    offlineRate: 0
```

`interval` 单位为毫秒；每个周期从 `batch` 区间随机取新增量。`num` 是场景目标，`open: false` 时进程仍运行监控任务但不会创建用户。三个场景互斥：`connect` 只保持已认证连接；`play` 完成 Login→Scene→Ready 并驱动对局；`reconnect-churn` 在 `play` 基础上启用 `logoutRate/offlineRate`，另外两个场景必须把流失率设为 0。

`url` 支持用逗号分隔多个 Gateway 地址，压测用户按 UID 轮询选择。`gatewayLimit` 和 `gatewayPerIPLimit` 是每个去重 Gateway endpoint 的部署声明；`connect` 以外的场景还校验 `nodeSeatCapacity`。这些声明不会远程读取部署配置，填写错误会使结果失真。`concurrency` 限制初始化流程并发数。压测客户端不会读取 `room.game`，`minMoney/maxMoney` 独立配置；Ludo 服务端仍会校验 pressure Login 范围。

`play` 流程是 WebSocket Auth → Login → Scene → Ready → 根据 Push 自动 Dice/Move。监控口径如下：

| 字段 | 含义 |
| --- | --- |
| `authenticated` | WebSocket Auth 已成功且连接仍被跟踪 |
| `seated` / `ready` / `playing` | 客户端从 Login、Scene、Ready 响应和对局 Push 可证明的业务阶段 |
| `stages` | Connect/Login/Scene/Ready 的 attempts、succeeded、failed、successRate 与最近 1024 次 p50/p95/p99 |
| `commands` | Gateway 请求成功且 body 可解码；业务阶段成功率以 `stages` 为准 |
| `broadcasts` | 测试广播累计 received/invalid，以及每 128 条采样、最近 1024 个样本的端到端 p50/p95/p99 |
| `uid_start` / `uid_end` | 本轮只增不减且不回绕的 UID 区间；耗尽后停止补充用户 |
| Action 并发 | Push callback 内等待全局 slot 并同步请求；slot 饱和时会阻塞该用户后续 callback |

每轮压测必须选择未使用过的 `startID + uidCount` 区间；Redis 清理只能在专用测试实例按明确前缀执行。preflight 校验的是配置声明而非远端实时容量，因此结果仍需结合服务端指标解释。热路径成本、最近 benchmark 和容量口径见 [性能基线](../../docs/performance.md)。

### 单 Gateway 阶梯容量

Gateway 默认使用本机 NATS 容量探针，`-nats-url` 可覆盖地址；`connect` 场景即可验证 NATS → Gateway → 已认证 WebSocket Session 的广播链路，不需要启动 Ludo Node 或执行游戏命令。当前先验收单 Gateway 10,000～50,000 条连接：所有档位固定使用 5 个直连且出口 IP 不同的 press，避免压测端拓扑变化干扰曲线。

| Gateway 总连接 | 每个 press 的 `num` |
| ---: | ---: |
| 10,000 | 2,000 |
| 20,000 | 4,000 |
| 30,000 | 6,000 |
| 40,000 | 8,000 |
| 50,000 | 10,000 |

Gateway 每档都使用 `-ws-max-connections 52000 -ws-max-connections-per-ip 12000`；每个 press 声明 `gatewayLimit: 52000`、`gatewayPerIPLimit: 12000`，并使用互不重叠的 UID 区间。当前 Runner 只维护一个固定 `num`，因此五档使用相同二进制和配置分别重启运行；不要把连续建连途中经过目标值时的瞬时指标当作稳态数据。每档连接稳定后预热 2 分钟、采集至少 10 分钟，50,000 档采集 30 分钟；两档之间确认 Gateway 在线状态和 press 主机 `TIME_WAIT` 已回落。

单 Gateway 时 `broadcasts.received` 的增量应接近在线连接数/秒，`invalid`、Gateway queue/send drop 必须为 0。每档同时记录 CPU、RSS/Go heap、GC、goroutine、FD、socket、带宽和端到端 p99，并计算每新增 10,000 条连接的增量；任一档出现 drop、压测端先饱和、p99 持续跳升或 CPU/RSS 增速明显变陡时停止升档，上一档作为候选安全容量并至少复测一次。Gateway 与 press 必须分机并同步系统时钟，不能用客户端计数替代服务端和主机指标。

#### 本机 loopback 对比

只有一台 macOS 时，可以给 `lo0` 临时增加 4 个目标 IP alias，再由现有 press 轮询 5 个 Gateway URL。TCP 连接的目标 IP 不同后，同一源 IP 可以复用临时端口，不需要修改 client；该模式只用于 10,000～50,000 档的同机相对曲线，不能关闭 I41 的 Gateway 单边容量验证。alias 修改需要管理员权限，测试结束后显式删除：

```bash
for suffix in 2 3 4 5; do
  sudo ifconfig lo0 inet "127.0.0.${suffix}/32" alias
done
```

测试完成后清理：

```bash
for suffix in 2 3 4 5; do
  sudo ifconfig lo0 inet "127.0.0.${suffix}" -alias
done
```

本机五档分别把同一份仓库外配置的 `num` 设为 10,000、20,000、30,000、40,000、50,000，并使用：

```yaml
url: ws://127.0.0.1:3102/,ws://127.0.0.2:3102/,ws://127.0.0.3:3102/,ws://127.0.0.4:3102/,ws://127.0.0.5:3102/
gatewayLimit: 52000
gatewayPerIPLimit: 52000
```

这些 URL 是同一 Gateway listener 的别名，不是 5 个 Gateway 实例；本机模式启动 Gateway 时也要把 `-ws-max-connections-per-ip` 设为 `52000`，因为服务端仍可能看到同一个 peer IP。preflight 对 URL 数量的计算仅用于阻止明显错误，不能把别名数量解释为服务端容量。

## 当前 `press` 边界

`runner.go` 使用一个 worker pool 编排用户初始化、周期维护、用户集合和计数；`user.go` 负责 WebSocket 与单用户事件串行化；`play.go` 负责 Ludo 登录、准备和对局行为。Move 合法性复用服务端 `internal/model` 的纯规则，避免维护第二套棋盘常量。

当前已拆分 connect/play/reconnect-churn，并补齐阶段在线口径、成功率、延迟分位数、UID 不回绕和容量 preflight。远端容量声明、专用 Redis 与稳定观察窗口仍由运行者负责验收，不能只凭客户端峰值给出生产容量。

## 开发与验证

从 `test/ludo` 目录按改动选择 `make test`、`make build`；`make all` 会生成 API、配置和 Wire 文件，仅在需要重新生成且核对源定义与目标后运行。`make init` 会通过 `go install ...@latest` 更新用户工具，仅用于初始化或明确升级。AI 的必需检查见 [AGENTS.md](../../AGENTS.md#验证)。

从独立 `test` module 根目录运行包测试，例如：

```bash
go test ./ludo/...
go test -race ./ludo/tools/press ./ludo/internal/biz/... ./ludo/internal/service ./ludo/internal/server
```

race 范围按实际受影响包选择，已由仓库 `make check` 覆盖的同配置测试无需重复。

Docker build context 必须是 `yola` 仓库根目录：

```bash
docker build -f test/ludo/Dockerfile -t yola-ludo .
```
