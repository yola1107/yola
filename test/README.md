# Yola 游戏测试模块

`test` 是独立 Go module，只单向依赖仓库根部的 `yola`：

```text
yola/test --replace--> yola
yola                  -X-> yola/test
```

## 配置来源

| 组件 | 配置入口 | 当前默认值 |
| --- | --- | --- |
| Gateway | 命令行参数，见 `test/gateway/conf.go` | WebSocket `:3102`、内部 gRPC `:9010`、Redis/etcd/NATS 均为本机；TCP `:3101` 配置当前未启用 |
| Ludo Server | `-conf` 指向文件或目录，默认 `ludo/configs` | `ludo/configs/config.yaml`：gRPC `:9001`、本机 Redis/etcd |
| Whot Server | `-conf` 指向文件或目录，默认 `whot/configs` | `whot/configs/config.yaml`：gRPC `:9002`、配置内的 Redis/etcd 地址 |

当前没有全局 `YOLA_TEST_REDIS_*` 或 `YOLA_TEST_ETCD_*` 覆盖逻辑。Gateway 使用 `-redis-*` 和 `-etcd-*` 参数；游戏 Node 使用配置文件的 `data.redis` 和 `data.registry`。本地凭据应放在仓库外的配置副本或启动参数中，不得提交。

`test/gateway/main.go` 通过 `Transport` 拥有 WebSocket transport；当前测试入口不启动 TCP transport。

Redis 客户端创建时会立即执行 `PING`（`test/internal/xredis/redis.go`）。地址必须使用完整的 `host:port` 形式，非法地址和连接参数会在创建客户端前返回错误，不会回退到本机默认值。密码留空只表示客户端不发送密码；如果 Redis 开启了认证，Gateway 必须传 `-redis-password`，Ludo/Whot 必须设置 `data.redis.password`，否则启动会返回 `ping Redis: NOAUTH Authentication required`。该错误是依赖配置不匹配，不是允许忽略的空密码状态。

## 本地启动

先启动 Redis、etcd 和 Core NATS，再从 `test` module 根目录启动 Gateway：

```powershell
go build -o ./bin/gateway.exe ./gateway
./bin/gateway.exe `
  -redis-addr 127.0.0.1:6379 `
  -redis-db 0 `
  -etcd-endpoints 127.0.0.1:2379 `
  -etcd-prefix /yola/test `
  -advertise-host 127.0.0.1 `
  -rpc-timeout 5s
```

Redis 无认证时省略 `-redis-password`；开启认证时从本地 secret 来源注入。再启动游戏 Node：

```powershell
go build -o ./bin/ludo-server.exe ./ludo/cmd/ludo-server
go build -o ./bin/whot-server.exe ./whot/cmd/whot-server
./bin/ludo-server.exe -conf ./ludo/configs -id ludo-1
./bin/whot-server.exe -conf ./whot/configs
```

上述 Gateway 请求预算为 5s，Ludo YAML 的 Node handler 也是 5s；Whot 保持自身配置。复现本轮 Ludo 突发入座对照时，须在仓库外的配置副本中把 `server.grpc.timeout` 设为 15s，并将 Gateway 改用 `-rpc-timeout 15s`。入座等待仍为 5s；[超时职责](../docs/architecture.md#63-请求预算与超时职责) 说明了这些预算的边界，不能把压测夹具结果直接当作默认配置容量。

Gateway 的 `-ws-max-connections` 默认 10,000，`-ws-max-connections-per-ip` 默认 **100**（与框架 `DefaultMaxConnPerIP` 一致）。单机压测通常需显式提高 per-IP；总量超过 10,000 时必须同时调整两项，并按每个 Gateway 的实际分流目标设置。硬上限未对齐时，连接拒绝不能作为服务容量结论。

真实广播容量探针默认连接 `nats://127.0.0.1:4222`；`-nats-url` 可覆盖地址。每个 Gateway 实例订阅 `yola.gateway.press.broadcast.v1`，并在 Kratos `AfterStart` 后每秒发布一个 256B 时间戳 Payload；本地 EventBus 和广播接纳队列容量均固定为 256。Gateway 每 15s 输出广播 accepted/completed/drop、队列深度、Go heap、GC 和 goroutine。多个 Gateway 会分别发布并接收彼此的事件，使单连接到达率按 Gateway 数量倍增；I41 的单机基线只启动一个 Gateway。该 Topic、command 与 Payload 只属于测试协议，不是生产业务契约。

```bash
go build -o ./bin/gateway ./gateway
./bin/gateway \
  -nats-url nats://127.0.0.1:4222 \
  -ws-max-connections 52000 \
  -ws-max-connections-per-ip 12000
```

单 Gateway 真实容量先按 10,000、20,000、30,000、40,000、50,000 五档独立运行；固定使用 5 个直连且出口 IP 不同的 press，每档每个 press 分别维持 2,000、4,000、6,000、8,000、10,000 条连接。不得让多个 press 共用同一 SNAT 出口，也不得把 Gateway 与 press 同机数据作为单边容量结论。每档以相同二进制和配置重新启动 Gateway/press，连接稳定后预热 2 分钟、采集至少 10 分钟，50,000 档延长至 30 分钟；档位之间确认在线状态和 press 主机 `TIME_WAIT` 已回落。

只有一台 macOS 时，可为 `lo0` 增加多个目标 IP alias，并让现有 press 轮询这些 URL，在本机完成五档相对曲线，不需要修改 client；同机结果包含客户端资源竞争，不替代上述分机验收。真实连接不能仅看 Go heap：还需从进程和主机侧采集 RSS、CPU、FD、socket、带宽与丢包。Gateway 和压测客户端必须同步系统时钟，否则客户端广播端到端延迟无效。完整客户端配置、loopback 设置、档位和指标见 [Ludo press](./ludo/README.md#单-gateway-阶梯容量)。

仓库当前没有 `ludo-smoke` 或 `whot-smoke` 入口。Ludo 的运行、完整流程测试和压测入口见 [ludo/README](./ludo/README.md)。

## Table Push 分段基准

Ludo/Whot 的 `BenchmarkTablePush` 共用 [pushbench](./internal/pushbench/benchmark.go)，通过真实 Redis、Node、Gateway 和桌广播函数测量 mailbox 等待、fanout、`PushToUID`、`LocateGate` 与 Gateway gRPC。基准使用独立 OTel MeterProvider 和 Kratos metrics client middleware，未配置 `YOLA_REDIS_INTEGRATION` 时跳过。

从 `test` 目录运行，Redis 必须是可丢弃专用实例；每个子基准使用随机 service 隔离 binding/epoch 和可丢弃 UID：

```powershell
$env:YOLA_REDIS_INTEGRATION = '<dedicated-redis>:6379'
$env:GOMAXPROCS = '4'
go test ./ludo/internal/biz/table -run '^$' -bench '^BenchmarkTablePush$' -benchtime=3s -count=3 -timeout=180s
go test ./whot/internal/biz/table -run '^$' -bench '^BenchmarkTablePush$' -benchtime=3s -count=3 -timeout=180s
```

每桌四人、Payload 为 256B bytes 的 protobuf 消息，8/64 桌分别对照无注入延迟和每条 Push 注入 5ms Gateway 处理延迟。worker 跟随 Ludo/Whot 的 `min(tableNum, 16)` 默认规则，8/64 桌分别为 8/16；闭环并发固定为 `8 × GOMAXPROCS`，上述命令下为 32。接收器只计数、不使用客户端 socket；每轮要求成功投递数等于广播数的四倍。输出各段调用数、均值及直方图分位数所在桶的上界（`p99_le_us`），任何 Push 或 mailbox 调用失败都会使基准失败。历史 8 worker 结果、队列和计时边界见 [性能基线](../docs/performance.md#table-push-分段基线)。

## 固定速率与真实游戏验证

`BenchmarkTableCadence` 使用真实 WebSocket 客户端，分 100、500、1,000 桌运行。每桌每秒一次任务，同一任务内串行广播两次，每次四名玩家；消息携带桌号、序号和计划时间。发生器直接按计划入队，不等待前一次完成；`generator_lag`、`mailbox_wait`、`scheduled_complete` 和 `client_scheduled` 分别区分发生器迟到、排队、任务完成和客户端回调延迟。

从 `test` 目录运行，必须使用 `-benchtime=1x`，每个 iteration 表示整个场景；`YOLA_CADENCE_DURATION` 默认为 30s，允许 1s～30m 的整秒数：

```powershell
$env:YOLA_REDIS_INTEGRATION = '<dedicated-redis>:6379'
$env:GOMAXPROCS = '4'
$env:YOLA_CADENCE_DURATION = '30s'
go test -p=1 ./ludo/internal/biz/table ./whot/internal/biz/table -run '^TestTablePushSocketOrdering$' -bench '^BenchmarkTableCadence$' -benchtime=1x -count=1 -timeout=15m
```

每名接收者必须按序收到全部消息，队列拒绝、缺失、重复、串桌或乱序均使测试失败。`TestTablePushSocketOrdering` 额外覆盖读循环每帧延迟 10ms，以及旧连接关闭后的同 UID 重连，校验 binding token 已更换且序号连续；它不覆盖断线期间的消息重放、并发接管或发送队列溢出。

`TestGameDelivery` 装配真实游戏 Usecase、Redis 玩家仓库、Node、Gateway 和独立 etcd namespace，使用原压测玩家驱动 Login、Scene 和游戏操作。Ludo 保持同步操作，Whot 保持异步请求与响应 mailbox；测试连接接入观测 codec，不修改业务协议。场景包括两桌烟测、一名真人与两名服务器机器人，以及 100、500、1,000 桌各四名真人。

Ludo 测试按 `tableID = (UID - uidStart) / 4 + 1` 指定入座，每四个连续 UID 对应一桌，1,000 桌对应 4,000 名真人；验证 Login 返回桌号与分配一致，单独输出启动耗时和 Connect/Login/Scene/Ready 指标。每 10ms 新增 400 人，4,000 人计划约 100ms 发起完，实际发起与完成受调度和依赖处理速度影响。启动池容量按本轮玩家总数设置，避免有限 UID 在压测端因池满而漏发；等待全部启动成功或失败后统一统计，拒绝、掉线或响应错误仍使测试失败。普通 press 入口与 Whot 保留自动选桌。

`TestTableAdmission` 只验收 4,000 人进入指定的 1,000 桌，查询每桌 Scene 并核对座位与消息投递；它不要求所有人已经参与本局。`TestGameDelivery` 先检查入座，再等待全员收到开局推送后采集对局指标。两种验收都保留自动准备和既有客户端动作，入座阶段仍可能与已开局桌的操作重叠。Ready 阶段在 Scene 已表明玩家准备或游戏中时直接成功，阶段成功数不等于 Ready RPC 数。

Ludo 夹具的 WebSocket handler、Gateway Forward 和 Node handler 预算均显式设为 15s，配合业务入座/重连等待上限 5s 和独立清理预算 2s；Whot 夹具保持 3s。客户端请求默认 30s。夹具不读取游戏 YAML 的 Node handler 配置；独立运行服务时按 [本地启动](#本地启动) 装配，职责见 [超时预算](../docs/architecture.md#63-请求预算与超时职责)。测试参数和历史 2s、10s 入座预算不同，跨版本比较时须注明超时链路。

```powershell
$env:YOLA_ETCD_INTEGRATION = '<dedicated-etcd>:2379'
$env:YOLA_GAME_DURATION = '30s'
go test -p=1 ./ludo/tools/press ./whot/tools/press -run '^TestGameDelivery$' -count=1 -v -timeout=20m
# 只验证小规模真人和机器人；Linux 可补 -race，race 数据不用于性能比较。
go test -p=1 ./ludo/tools/press ./whot/tools/press -run '^TestGameDelivery/(smoke|robots)$' -count=1 -v -timeout=5m
# 只验证 Ludo 定桌入座与对局，按 100、500、1,000 桌升档。
go test ./ludo/tools/press -run '^TestGameDelivery/tables=' -count=1 -v -timeout=10m
# 单独验证 4,000 人进入指定的 1,000 桌，核对座位与消息投递，不要求全员已参与本局。
go test ./ludo/tools/press -run '^TestTableAdmission$' -count=1 -v -timeout=5m
```

真实游戏测试等待所有玩家收到开局推送后计时，默认 30s、允许 1s～30m；按已有游戏反馈立即操作，属于闭环负载，与每桌每秒一次的固定速率基准分别解释。结束后查询每桌 Scene，核对人数、UID 归属、座位唯一性和在线状态；停止服务生产者后，对每个 UID 的 Node Push 与客户端回调做数量及 SHA-256 流摘要比对，摘要包含 command、长度与内容边界。机器人场景还要求观察到机器人实际动作；结算消息单独计数，短样本不保证每桌完成整局。每款游戏遇到首个失败场景即停止升档；延长采样时间时须按所选场景总时长增大 `-timeout`。

`client_wire_request` 从客户端出站帧编码计至响应解码，包含出站队列、Forward、业务执行和返回；它不包含业务 payload 首次序列化，也不代表 `Client.Request` 返回值观测。编码前失败和超时后迟到的响应仍需结合压测客户端日志判断。`handler` 是 Node command middleware 内部耗时，不能直接当作 mailbox 等待；真实游戏的 mailbox wait/reject 仍未独立采集。所有延迟分位数都是直方图桶上界。

固定速率基准使用随机 service；真实游戏使用随机可丢弃 UID 和 etcd namespace，并写入玩家数据，因此 Redis 必须独占且可丢弃。认证 token 只是测试 UID。服务、压测客户端和观测器同进程，CPU/RSS 包含三者及观测开销；正式计时期间不要并行编译、跑其他检查或压测。结果与限制见 [性能基线](../docs/performance.md)。

## 日志

- Gateway 和 Ludo 压测客户端使用 console zapslog，级别分别由 `-log-level` 控制。
- Ludo Server 使用配置文件 `log` 段；压测时应关闭普通文件、error 文件和 `room.logCache`，避免 I/O 污染结果。
- Whot Server 当前使用标准 `slog` text handler，不与 Ludo 的 `log` 配置共用。
- `YOLA_TEST_LOG_DIR` 只决定 Ludo/Whot 桌日志根目录，不会覆盖 Redis、etcd 或普通服务日志。

## 安全与协议限制

Gateway 的测试认证只接受正 `int64` 的规范十进制 token，并直接作为 UID；它不是生产凭据。生产环境必须替换为可校验、可过期且防重放的认证，并补齐限流、ACL 和 secret 管理。

客户端和压测工具必须使用 `yola/api/protocol/v1` envelope 及 Protobuf body。Gateway 提供 best-effort Node `Disconnect`，但旧连接通知仍可能晚于重连；业务必须在 actor/mailbox 内比较 `Session.BindingToken()`，并结合显式 Logout、租约或业务超时维护在线状态，见 [断线与重连边界](../docs/architecture.md#43-pushkick-与-disconnect)。
