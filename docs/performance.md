# 性能基线与容量验收

本文保留成本模型、少量有明确口径的历史数据和复测入口。**历史测量不是当前checkout或生产容量的通过结论。** 完整旧表、实验过程和日志索引可用`git show 61faa25:docs/performance.md`追溯；更新基线须记录commit、工具、配置、机器和负载。

## 热路径成本

已绑定Stateful请求依次执行Gateway查Node binding、Gateway查Node epoch、gRPC Forward、Node再次查binding，再进入handler。正常GET速率约为`3 × 已绑定Stateful QPS + 未绑定Stateful QPS`；Stateless不查Node Locator，不含认证、续租和Push。前两次查询有数据依赖，Node复查承担fencing，不能只为省I/O删去校验或增加缓存。

Gate lease默认60s，只在heartbeat到达且剩余不超过30s时续租；健康时约`在线连接数 / 30s`次脚本/秒，10万连接约3333次。失败不内部循环重试，但后续heartbeat仍可再尝试；Gateway不限制跨Session的续租并发。

| 成本项 | 口径 |
| --- | --- |
| TCP读写buffer | 各4096+4B，每连接约8.0KiB；10万连接理论约782MiB，不含socket/Session/队列 |
| WS buffer | 每连接4KiB read buffer，write buffer从pool借用，不能按固定双buffer估算 |
| 认证后的业务FIFO | 每个HeartbeatHandler连接一个worker，默认等8帧；按最大帧计数据理论约32KiB，另有对象/栈成本 |
| NATS积压 | 订阅数×队列容量×broker实际Payload上限，加连接、结构与runtime余量 |
| Table同步Push | LocateGate及Gateway RPC占用桌worker；多次Push分别受超时约束 |

这些是结构成本，不能换算为实际RSS或安全容量。没有当前benchmark/profile与明确瓶颈，不新增缓存、批量RPC、续租整形或更大队列。

## 本地回归参考

2026-09-25，Windows/amd64、i7-9700K、Go 1.26.6，以下为当时工作树的测量。调度器基于`1ed763b`加I50；观测项基于`346cfaa`加I55、GOMAXPROCS=8，顺序运行三次取中位数。

| 场景 | 时间 | B/op / allocs/op |
| --- | ---: | ---: |
| 认证调度器构造、启动、停止 | 1084～1091ns | 561 / 8 |
| NATS空handler dispatch（带观测） | 38.91ns | 0 / 0 |
| TCP填满32帧队列再拒绝一次（带观测） | 2793ns | 1152 / 7 |
| WS 4000B request往返（带观测） | 88.954µs | 约19.17KB / 20 |

4096个空调度器的历史GC后heap增量约1294B/连接，StackInuse约8192B/连接；不含socket及业务对象，不外推为其他平台的固定栈大小。B/op是累计分配，不是常驻内存。WS数据含client/server；这些基准不包含完整游戏、慢socket或真实连接容量。

<a id="nats-capacity"></a>
## NATS 积压与释放

2026-09-26，`eb182b0`加验收代码，Linux VM 4 vCPU/约7.5GiB、Go1.26.6。专用NATS2.10.29设置max_pending=64MiB，各限0.5CPU/256MiB；接收与独立发布子进程共用2CPU/1536MiB容器、GOMAXPROCS=2。每格独立进程三轮取中位数，业务上限64KiB、队列256；先以primer阻塞handler，再填满队列并确认接收/拒绝计数。

| broker上限 / Payload / 订阅数 / 结束方式 | GC后积压heap增量MiB | 接收进程峰值RSS MiB | queue drop / payload drop |
| --- | ---: | ---: | ---: |
| 64KiB / 256B / 1 / 排空 | 0.099 | 15.617 | 0 / 0 |
| 64KiB / 64KiB / 1 / 排空 | 16.040 | 35.609 | 64 / 0 |
| 64KiB / 64KiB / 4 / 排空 | 64.144 | 96.039 | 256 / 0 |
| 64KiB / 64KiB / 1 / 直接关闭 | 16.039 | 35.730 | 64 / 0 |
| 1MiB / 1MiB / 1 / 出队丢弃 | 256.036 | 310.059 | 64 / 256 |
| 1MiB / 1MiB / 1 / 直接关闭 | 256.037 | 308.012 | 64 / 0 |

RSS包含接收测试进程基线与运行开销，不含发布子进程/broker，不能解释为队列增量。直接Close丢弃排队消息，不经过Payload校验，因此PayloadDropped=0。保留Subscription句柄时，关闭后QueueDepth=0，GC可回收积压，但自然等待100ms或再GC后RSS仍接近峰值；主动FreeOSMemory才约15～17MiB，不能当作Close保证。协作handler下历史关闭中位耗时0.161～0.598ms，无强制中止任意handler的承诺。

业务出队上限不能约束其他publisher已经排队的大消息。需联合配置broker max_payload、订阅数和队列容量；这组数据不替代生产认证/TLS/ACL及任意负载SLO。[基准](../event/nats/capacity_benchmark_test.go)、[边界测试](../event/nats/payload_limits_test.go)

## Table Push 分段基线

2026-09-06，父提交`c9ca9bd`加当批基准，Linux VM 4vCPU/约7.5GiB、Go1.26.6、独占Redis8.6.1；GOMAXPROCS=4，每档3s×3取中位数。每桌4人、256B protobuf，计数接收器代替socket，8worker、队列128、batch64、32个闭环在途任务；每任务串行广播一次，以下均为64桌，单位ms。

| 游戏 / 每条Push注入延迟 | LocateGate均值 | Gateway RPC均值 | fanout均值 | mailbox等待均值 | mailbox完成p99桶上界 |
| --- | ---: | ---: | ---: | ---: | ---: |
| Ludo / 0 | 0.522 | 0.574 | 4.51 | 13.43 | 27.17 |
| Ludo / 5 | 0.628 | 6.315 | 27.94 | 82.37 | 140.21 |
| Whot / 0 | 0.509 | 0.574 | 4.46 | 13.29 | 27.17 |
| Whot / 5 | 0.656 | 6.345 | 28.16 | 82.99 | 140.21 |

fanout从桌广播入口到全部同步Push返回；mailbox等待从接纳到开始，完成包含执行。p99是直方图桶上界，不是精确分位数；注入延迟模拟Gateway处理，未覆盖慢客户端。24个正式样本的投递计数一致、错误为0，但未压满每桌队列，不证明过载无拒绝。

当前Ludo/Whot使用`min(tableNum,16)` worker、队列128、batch64，单桌FIFO、跨桌并行；定时器也回到桌mailbox。旧8worker表仅说明同步外部I/O与排队关系，当前命令不能直接复现旧64桌/8worker条件。固定到达率对照曾显示16改善8worker排队，32未稳定更优；不据此扩大默认并发。[复测命令与计时范围](../test/README.md#table-push-分段基准)

## 游戏链路的验证边界

2026-09-06，`8131705`加测试代码，同一4vCPU VM、Go1.26.6、专用Redis/etcd、GOMAXPROCS=4。client、Gateway、Node和观测器在同进程，经loopback真实WS/gRPC通信；CPU/RSS含客户端及观测，不含Redis/etcd，不代表服务单边容量。

- 固定速率夹具在1000桌、每秒8000条Push、120s样本中数量/顺序匹配；Ludo较短30s正式样本仍有868ms的mailbox/client计划时间p99桶上界，原因未完全定位，较好长样本不能覆盖该长尾。
- 真实游戏100桌曾通过逐UID数量、顺序和摘要核对；500桌启动曾分别出现Ludo入座超时与Whot桌mailbox满，1000桌真实游戏未据此通过。启动失败不能解释为已开局桌的容量上限。
- 2026-09-26原生App与预算夹具（`cf85f6e`加改动）下，两款游戏当前预算的两桌smoke/robots各10s通过；不代表完整YAML部署、每桌完成整局或长期SLO。入口为TestConfiguredGameDelivery。

### Ludo 单 Gateway 参数对照

2026-09-07，同一4vCPU VM，4000玩家定向进入1000桌，每100ms加载400人，单Gateway。业务入座5s、清理2s，但WS/Gateway/Node均为15s、客户端30s；不覆盖Ludo YAML的5s Node handler。先编译后按A→B→B→A运行：

| worker / 队列 / batch | 四轮中的Login成功 / 失败 | Login p99范围 | 入桌排队p99范围 |
| --- | --- | ---: | ---: |
| A：16 / 128 / 64 | 两轮均4000 / 0 | 2.984～4.472s | 2.861～3.858s |
| B：32 / 64 / 64 | 两轮均4000 / 0 | 2.657～2.753s | 2.457～2.610s |

分位数来自每轮全部4000次Login和已执行入桌样本。座位及逐UID投递检查通过；同时改变worker/队列属于组合对照，A轮间波动明显，不宣称最优参数或稳定提升比例。默认仍为A，不要求全员参与同一局，不替代稳态及完整对局验收。当前工具/预算与复现入口见 [测试模块](../test/README.md#固定速率与真实游戏验证)。

## 容量验收

1. 先固定commit、机器/资源限额、独立进程拓扑、TLS、日志级别、配置、消息模型、UID区间及业务SLO。使用 [专用依赖](./README.md#开发与验证)，正式计时不并行编译或其他压测。
2. 广播用test/gateway每秒1条256B事件、Ludo press connect场景；固定5个独立出口IP，分别复测1万至5万总连接，每press分担2000至1万。逐档重启Gateway/press，核对总量/per-IP上限，确认压测端未先饱和。
3. 排除前2分钟预热，每档至少10分钟，5万档30分钟；观察CPU、RSS、GC、goroutine、发送/广播/订阅drop、mailbox wait/reject及业务延迟，同时采集Redis/etcd延迟/连接/错误、socket/FD/丢包/带宽。
4. 分开验证登录突发、稳定对局、慢连接、重连及故障窗口；核对座位、UID和投递数量/顺序。drop、p99或资源增速持续变陡时停止升档，上一档复测后才能作为候选安全容量；瞬时峰值单列。

真实连接RSS、慢连接比例、跨机/TLS、完整游戏和长期SLO仍待完成，状态见 [I41/I45](./issues.md#部署与容量)。

## 复现命令

仓库根目录，本机不带外部依赖的回归入口：

```powershell
go test ./network/internal/inbound -run '^$' -bench '^BenchmarkAuthenticatedDispatcher$' -benchmem -benchtime=1s -count=3
go test ./network/tcp -run '^$' -bench '^BenchmarkTCPSlowConsumerBackpressure$' -benchmem -benchtime=1s -count=3
go test ./network/websocket -run '^$' -bench '^BenchmarkWebSocketServer/request/payload=4000$' -benchmem -benchtime=2s -count=3
go test ./gateway -run '^$' -bench '^BenchmarkBroadcast/sessions=(1000|10000|100000)$' -benchmem -benchtime=1s -count=3
go test ./event/nats -run '^$' -bench '^BenchmarkDispatch$' -benchmem -benchtime=1s -count=3
```

真实Redis需预先注入专用YOLA_REDIS_INTEGRATION（凭据用YOLA_REDIS_PASSWORD）；按 [测试模块](../test/README.md) 装配游戏与gRPC分段基准：

```powershell
go test ./locate/redis -run '^$' -bench '^BenchmarkStatefulForwardRedisLookups$' -benchmem -benchtime=1s -count=3 -cpu=4
```

NATS容量测试仅支持Linux，使用专用YOLA_NATS_URL及相应broker max_payload；每次只选一格、独立进程。small/default/four/close_default使用64KiB broker，oversized/close_oversized使用1MiB broker；分别锚定两段正则以免误选：

```sh
GOMAXPROCS=2 go test ./event/nats -run '^$' -bench '^BenchmarkSubscriptionCapacity$/^default$' -benchtime=1x -count=1 -timeout=60s
```

p99须声明精确样本或桶上界，RSS区分服务单边与整个测试进程。未固定相同代码、配置、工具和环境的数据不能直接比较；本页整理不代表上述命令已重新运行。
