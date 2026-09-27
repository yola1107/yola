# 当前问题与验收缺口

这里只保留未解决问题和待验收事项。已实现契约见 [架构](./architecture.md)、[EventBus](./eventbus.md)，完成记录由 Git 追溯；以下条目不自动授权实施或运行外部测试。

## 问题总览

当前条目均为 **Spec**（行为契约或验收缺口），优先顺序表示处理时机。

| ID / 维度 | 问题与影响 | 位置 / 依据 | 优先顺序 | 状态 / 完成条件 |
| --- | --- | --- | --- | --- |
| [I40](#i40) / Spec | 开发broker结果不能证明生产认证/TLS/ACL及异步拒绝行为 | [NATS生命周期](./eventbus.md#4-nats-生命周期) | 生产部署前 | 待验收；使用实际安全配置及异步错误观测 |
| [I41](#i41) / Spec | 真实连接、慢连接RSS、跨机/TLS与长期SLO缺少容量结论 | [容量口径](./performance.md#容量验收) | 容量准入前 | 待验收；固定拓扑/负载、逐档复测并核对客户端投递 |
| [I45](#i45) / Spec | 同步Table Push和完整对局存在排队/长尾边界，入座通过不代表稳态达标 | [分段基线](./performance.md#table-push-分段基线)、[游戏边界](./performance.md#游戏链路的验证边界) | 按业务SLO验收 | 待验收；相同预算下分离启动突发与稳定对局 |
| [Cluster管理场景](#cluster-tests) / Spec | 独立场景曾通过，连续组合race切主仍超时 | [管理回归](../locate/redis/cluster_transition_test.go) | 连续管理操作前 | 组合未通过；仅在专用集群重现并核验 |

## 部署与容量

<a id="i40"></a>
**I40：** 生产NATS须用实际认证、TLS、ACL和订阅容量配置验收；Subscribe成功不是broker接受或权限通过的证明，必须收集异步拒绝。

<a id="i41"></a>
**I41：** 真实连接、慢连接和跨机/TLS负载按 [容量验收](./performance.md#容量验收) 逐档验证，区分服务单边RSS与测试进程总量。

<a id="i45"></a>
**I45：** 在相同请求预算下分别验证登录突发、完整对局与长期稳态；入座通过不等于全员开局，较好长样本不能覆盖较差的正式长尾样本。

<a id="cluster-tests"></a>
### Redis Cluster 管理场景

Node binding 的存储 epoch 校验已实现；独立新建 3 主 3 从集群的 Migration、CooperativeFailover 各三轮 race 样本曾通过，但连续组合 race 在第二轮切主超时，组合稳定性仍未通过。该历史结果不证明异步复制无数据回退，也不证明当前 checkout 已通过外部验收。管理测试须遵守 [专用环境要求](./README.md#开发与验证)。
