# 当前待办与验收条件

项目尚未上线。这里仅保留当前代码问题，以及选定部署和目标后才需要执行的验收；不为旧版本兼容或历史数据迁移建立待办。当前实现见[架构契约](./architecture.md)和[EventBus](./eventbus.md)。

## 现在需要处理

<a id="publish-budget"></a>
### P2 · NATS Publish 调用方预算

[Publish](../event/nats/event.go#L182) 只在入口检查 context，随后同步调用原生 Publish，锁等待和 socket 写可能超过 caller deadline。[Whot 示例](../examples/whot/service.go#L57) 在 handler 内同步发布，因此会影响请求完成和停机排空。这与是否上线无关。

2026-09-27 在当前代码上使用嵌入 NATS 和可控写屏障复核：50ms caller deadline 到期后，约201ms时仍未返回；放行写操作后返回 nil，连续3次成立。探针在临时 overlay 中运行，未修改生产代码；这是预算缺口的证据，不是网络性能基准。

下一步直接确定并实现首版发布语义：caller 的等待受 context 限制；尚未开始的取消请求不得再发布；已开始 I/O 的结果可能不确定，不能承诺撤回。新增的后台工作必须有界、由 Bus 持有并可等待回收，不能用无归属 goroutine 或固定写超时冒充 caller deadline。

完成条件：阻塞写反例通过，取消/Close/并发发布无泄漏和竞态，明确成功与取消后的投递边界，运行相关 race。此处仅完成评审，修复尚未实施。

## 按部署选择验收

<a id="i40"></a>
### 使用 NATS 时：安全配置

已有凭据/TLS 配置入口及[异步 ACL、重连回归](../event/nats/async_error_test.go)。选择实际 broker、账号、证书和 ACL 后，再验证授权/拒绝、异步错误观测及 payload/订阅上限；上线前完成。当前没有证据要求新增认证抽象或改协议。

<a id="i41"></a>
<a id="i45"></a>
### 确定业务目标后：容量与延迟

先给出目标连接数、消息大小/速率、机器、拓扑和 SLO，再按[性能验证](./performance.md#容量验收)测量登录突发、稳态、慢连接、重连与完整业务。历史游戏入座或长尾样本不能直接证明当前框架缺陷，也不能给出首版安全容量。

原 I41/I45 合并为此项。尚无目标和瓶颈证据时，不增加缓存、队列、并发层或批量 RPC；`test/` 游戏架构不作为根框架重构的前提。

<a id="cluster-tests"></a>
### 选择 Redis Cluster 时：拓扑变更

三主 Cluster 的绑定、epoch、续租及迟到写回归已执行，但不覆盖三主三从的槽重分配和主从切换。[管理用例](../locate/redis/cluster_transition_test.go)历史连续组合曾超时，原因尚未确定，不能据此判定为生产实现缺陷。

这里的 Migration 指 Redis 槽重分配，**不是旧版本数据迁移**。首版使用单机 Redis 时不阻塞开发；选择 Cluster 并要求扩缩容/切主时，再用全新六节点专用环境重现、区分夹具与实现问题，并补齐 RenewNode 的管理场景覆盖。隔离要求见[开发与验证](./README.md#开发与验证)。
