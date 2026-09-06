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
go run ./gateway `
  -redis-addr 127.0.0.1:6379 `
  -redis-db 0 `
  -etcd-endpoints 127.0.0.1:2379 `
  -etcd-prefix /yola/test `
  -advertise-host 127.0.0.1
```

Redis 无认证时省略 `-redis-password`；开启认证时从本地 secret 来源注入。再启动游戏 Node：

```powershell
go run ./ludo/cmd/ludo-server -conf ./ludo/configs -id ludo-1
go run ./whot/cmd/whot-server -conf ./whot/configs
```

Gateway 的 `-ws-max-connections` 默认 10,000，`-ws-max-connections-per-ip` 默认 **100**（与框架 `DefaultMaxConnPerIP` 一致）。单机压测通常需显式提高 per-IP；总量超过 10,000 时必须同时调整两项，并按每个 Gateway 的实际分流目标设置。硬上限未对齐时，连接拒绝不能作为服务容量结论。

真实广播容量探针默认连接 `nats://127.0.0.1:4222`；`-nats-url` 可覆盖地址。每个 Gateway 实例订阅 `yola.gateway.press.broadcast.v1`，并在 Kratos `AfterStart` 后每秒发布一个 256B 时间戳 Payload；本地 EventBus 和广播接纳队列容量均固定为 256。Gateway 每 15s 输出广播 accepted/completed/drop、队列深度、Go heap、GC 和 goroutine。多个 Gateway 会分别发布并接收彼此的事件，使单连接到达率按 Gateway 数量倍增；I41 的单机基线只启动一个 Gateway。该 Topic、command 与 Payload 只属于测试协议，不是生产业务契约。

```bash
go run ./gateway \
  -nats-url nats://127.0.0.1:4222 \
  -ws-max-connections 52000 \
  -ws-max-connections-per-ip 12000
```

单 Gateway 真实容量先按 10,000、20,000、30,000、40,000、50,000 五档独立运行；固定使用 5 个直连且出口 IP 不同的 press，每档每个 press 分别维持 2,000、4,000、6,000、8,000、10,000 条连接。不得让多个 press 共用同一 SNAT 出口，也不得把 Gateway 与 press 同机数据作为单边容量结论。每档以相同二进制和配置重新启动 Gateway/press，连接稳定后预热 2 分钟、采集至少 10 分钟，50,000 档延长至 30 分钟；档位之间确认在线状态和 press 主机 `TIME_WAIT` 已回落。

只有一台 macOS 时，可为 `lo0` 增加多个目标 IP alias，并让现有 press 轮询这些 URL，在本机完成五档相对曲线，不需要修改 client；同机结果包含客户端资源竞争，不替代上述分机验收。真实连接不能仅看 Go heap：还需从进程和主机侧采集 RSS、CPU、FD、socket、带宽与丢包。Gateway 和压测客户端必须同步系统时钟，否则客户端广播端到端延迟无效。完整客户端配置、loopback 设置、档位和指标见 [Ludo press](./ludo/README.md#单-gateway-阶梯容量)。

仓库当前没有 `ludo-smoke` 或 `whot-smoke` 入口。Ludo 的运行、完整流程测试和压测入口见 [ludo/README](./ludo/README.md)。

## 日志

- Gateway 和 Ludo 压测客户端使用 console zapslog，级别分别由 `-log-level` 控制。
- Ludo Server 使用配置文件 `log` 段；压测时应关闭普通文件、error 文件和 `room.logCache`，避免 I/O 污染结果。
- Whot Server 当前使用标准 `slog` text handler，不与 Ludo 的 `log` 配置共用。
- `YOLA_TEST_LOG_DIR` 只决定 Ludo/Whot 桌日志根目录，不会覆盖 Redis、etcd 或普通服务日志。

## 安全与协议限制

Gateway 的测试认证只接受正 `int64` 的规范十进制 token，并直接作为 UID；它不是生产凭据。生产环境必须替换为可校验、可过期且防重放的认证，并补齐限流、ACL 和 secret 管理。

客户端和压测工具必须使用 `yola/api/protocol/v1` envelope 及 Protobuf body。Gateway 提供 best-effort Node `Disconnect`，但旧连接通知仍可能晚于重连；业务必须在 actor/mailbox 内比较 `Session.BindingToken()`，并结合显式 Logout、租约或业务超时维护在线状态，见 [断线与重连边界](../docs/architecture.md#43-pushkick-与-disconnect)。
