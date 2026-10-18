# 自包含 transport：最终复审与验收

日期：2026-10-18。范围：`62847d5` 之后的全部任务改动，含未跟踪文件；集成分支为 `refactor/self-contained-transports`。

Node、Gateway、Sender 实施及两个独立 reviewer 均使用 `gpt-6.1-sol/xhigh`。主 agent 复核调用方、资源交接和分布式路径，并亲自补失败回归、修复与集成检查。官方 Kratos 与 kratos-layout 工作区保持干净，未修改底座。

## 结果

公开接入收敛为构造 transport、注册 handler、传入 `kratos.Server`。Node 自管内部登记与就绪；普通服务保持独立目录和标准 RPC，并可独立使用 Sender。最终接口见 [接入文档](../../docs/project/integration.md)，生命周期边界见 [架构契约](../../docs/project/architecture.md)。

code-simplifier 删除了失效的 epoch `retryRelease` 分支，将剩余释放状态收敛为 bool；删除旧就绪 Registrar 测试文件，将仍有效的用例并入生命周期测试。Gateway 的新增生命周期用例也集中到原文件。保留 epoch、连接代次、条件写入和分阶段排空的必要保护。

## Standards 复审

依据仓库 AGENTS、`.golangci.yml`、领域与本地任务约定。未发现未处理的规范问题。

| 发现 | 处理与证据 |
| --- | --- |
| P3：epoch `retryRelease` 只剩测试引用 | 删除冗余入口和无行为差异的状态，保留固定凭据、幂等释放；`node/epoch.go:227` |
| lint：新回收 helper 的 error 返回位置、目录测试变量遮蔽 | 修正签名和局部命名，最终受影响包 lint 为 0 issues |
| 收尾修复是否再次增加冗余 | reviewer 按 code-simplifier 复核，确认 prepared 交接边界和迟到结果汇合有独立职责 |

## Spec 复审

依据 [父规格](spec.md) 与六张任务票。最终无未关闭的 P1/P2，也未发现范围扩张。

| 发现 | 最终行为 | 回归 |
| --- | --- | --- |
| P1：Drain 忽略取消时阻塞网络关闭和停止续租 | 预算耗尽后先关闭已知资源，再汇合唯一 Drain；不主动释放 epoch | `node/lifecycle_test.go:340` |
| P1：Gateway 后续准备阻塞时，先前客户端 listener 未回收 | 按 prepared 前缀回收已交接资源；迟到 transport 继续由同一 owner 接收 | `gateway/lifecycle_test.go:250` |
| P1 组合路径：迟到登记被迟到 Drain 挡住 | 登记一经交接即注销，再等待 Drain | `node/lifecycle_test.go:190` |
| 自定义 transport.Stop 忽略取消 | 其余已知网络先关闭，最后等待该次 Stop 返回 | `gateway/lifecycle_test.go:288` |

前三项均实际运行失败用例后修复。定向 race 全部通过。实现入口为 `node/lifecycle.go:219`、`gateway/lifecycle.go:243`。

## 验证

专用依赖为 Redis 7.4.9（`127.0.0.1:16379`）与 etcd 3.5.30（`127.0.0.1:12389`），通过现有 Colima socket 启动。下列集成命令需设置 `YOLA_REDIS_INTEGRATION` 与 `YOLA_ETCD_INTEGRATION` 指向可丢弃实例。

| 检查 | 结果 |
| --- | --- |
| `make check` | 根与 test module 的 buf、tidy diff、vet、staticcheck、测试和 diff 检查通过 |
| `make race` | 两个 module 全包通过；环境门控集成另行运行 |
| 受影响包 `golangci-lint run` | 根与 test module 均为 0 issues |
| 两 Gateway、两 Stateful Node、TCP/WS、跨 Gate 重连、绑定精确路由、普通 RPC 与 Sender | 真实依赖下 `-race -count=10` 通过 |
| Node 替换保护、Gateway/Node 原集成、Node/普通目录隔离 | 与上述真实依赖场景一同重复 10 次通过 |
| Whot/Ludo `TestConfiguredGameDelivery/(smoke|robots)` | 默认 30s 采样，真实依赖及 race 通过 |
| 两款游戏 `TestTablePushSocketOrdering` | 真实 Redis/WS、独立 Sender、race 通过 |
| WS/Forward/Node 三层预算 | `TestGameRequestTimeoutsAcrossWebSocketAndGRPC` 通过 |
| Whot/Ludo Wire | 重新生成；源文件与生成文件同步 |

分布式复验命令（根 module）：

```sh
go test -race ./node ./gateway ./registry/etcd \
  -run 'TestDistributedGatewaysKeepNodeOwnershipAndIndependentSender|TestGatewayNodeIntegration|TestAppPreparedNodeCannotOverwriteReplacement|TestNodeDirectorySeparatesApplicationAndEnvironment' \
  -count=10 -timeout=180s
```

游戏复验命令（test module，采用默认采样时长）：

```sh
go test -race -p=1 ./ludo/tools/press ./whot/tools/press \
  -run '^TestConfiguredGameDelivery$/(smoke|robots)$' -count=1 -timeout=180s
go test -race -p=1 ./ludo/internal/biz/table ./whot/internal/biz/table \
  -run '^TestTablePushSocketOrdering$' -count=1 -timeout=90s
```

最终生产代码跑过完整 check/race。之后仅修正新测试的分布式错误码断言、变量命名及文档，并对涉及包补跑 lint 与真实依赖 race。新断言接受发现摘除返回的 Unavailable 和 epoch 已撤销返回的 Aborted，继续严格检查存活 Node 未收到请求、原绑定保留、Sender 可推送。

本机会话日志保存在 `/tmp/yola-final-{check,race}.log`、`/tmp/yola-final-distributed-green.log`、`/tmp/yola-final-{lint-green,test-lint}.log`、`/tmp/yola-review-{red,combined-red,final-green}.log`、`/tmp/yola-real-games-default.log` 和 `/tmp/yola-sender-sockets.log`。这些是临时证据，复验以上命令即可重新取得结果。

## 已区分的夹具问题与边界

- 真实游戏夹具曾只注入 loopback listener，却保留默认通配 Address，导致发布错误 host。已显式配置 `node.Address("127.0.0.1:0")`，实际网络验证通过。
- `YOLA_GAME_DURATION=1s` 的机器人场景，在基线 `62847d5` 与本轮均因第二个机器人尚未入座而失败。默认 30s 场景通过；未改变生产机器人策略。证据为 `/tmp/yola-real-games-baseline-1s.log`。
- 本轮验证接口迁移、生命周期和分布式正确性；未重跑大规模容量/SLO、真实跨主机网络分区或生产部署。原 Redis Cluster 切主证据沿用前序提交，未把单机多实例验收解释为性能收益。
- 不响应 context 的业务或依赖仍可能使最终汇合等待；已知资源回收、epoch TTL 与业务状态恢复分别遵循架构契约。
