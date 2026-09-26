# 当前问题与验收缺口

这里只保留未闭环问题和可选维护项。已实现契约见 [架构](./architecture.md)、[EventBus](./eventbus.md)，历史修复与清理过程由 Git 追溯；以下条目不自动授权实施或运行外部测试。

## 问题总览

按两个维度记录：**Spec** 是行为契约或验收缺口，**Standards** 是维护性判断；可选清理不等于已确认缺陷。优先顺序表示处理时机，不把不同维度合并成缺陷等级。

| ID / 维度 | 问题与影响 | 位置 / 依据 | 优先顺序 | 状态 / 完成条件 |
| --- | --- | --- | --- | --- |
| [I46](#i46) / Spec | Redis caller deadline与网络timeout分类未闭环，请求可能超过调用方预算 | [client创建](../examples/env/env.go)、[Locator](../locate/redis/locator.go) | 优先修复 | 未解决；真实Redis验证deadline、取消及错误身份 |
| [I03](#i03) / Spec | Node binding固定6h，重复Bind是覆盖写，不能安全保活 | [Bind/Unbind](../locate/redis/node.go)、[修改权契约](./architecture.md#node-binding) | 先定契约 | 未解决；明确业务所有权、条件保活及重启继承 |
| [I40](#i40) / Spec | 开发broker结果不能证明生产认证/TLS/ACL及异步拒绝行为 | [NATS生命周期](./eventbus.md#4-nats-生命周期) | 生产部署前 | 待验收；使用实际安全配置及异步错误观测 |
| [I41](#i41) / Spec | 真实连接、慢连接RSS、跨机/TLS与长期SLO缺少容量结论 | [容量口径](./performance.md#容量验收) | 容量准入前 | 待验收；固定拓扑/负载、逐档复测并核对客户端投递 |
| [I45](#i45) / Spec | 同步Table Push和完整对局存在排队/长尾边界，入座通过不代表稳态达标 | [分段基线](./performance.md#table-push-分段基线)、[游戏边界](./performance.md#游戏链路的验证边界) | 按业务SLO验收 | 待验收；相同预算下分离启动突发与稳定对局 |
| [Cluster管理场景](#cluster-tests) / Spec | 独立场景曾通过，连续组合race切主仍超时 | [管理回归](../locate/redis/cluster_transition_test.go) | 连续管理操作前 | 组合未通过；仅在专用集群重现并核验 |
| [P2维护项](#可选清理p2) / Standards | 零散文件、单点装配和重复测试增加阅读/维护成本 | 下方按范围列候选 | 可选，未排期 | 先复核净收益，保持行为、错误和测试覆盖 |

## 正确性与业务契约

<a id="i46"></a>
### I46 · Redis deadline 与错误分类

**未解决。** go-redis v9.22.0 默认未开启 `ContextTimeoutEnabled`，当前装配未普遍启用 caller deadline，网络 timeout 分类也未闭环。[client 装配](../examples/env/env.go)、[Locator](../locate/redis/locator.go)

历史诊断（`eb182b0`，2026-09-26）中，已预热连接在 caller 100ms deadline 后约 301ms 才成功返回；开启该选项后约 100ms 返回 `net.OpError` timeout，但不匹配 `context.DeadlineExceeded`。纯 cancel 无 deadline 时，两种配置都可能完成已开始读写；不能据此承诺回滚。本次文档整理未重跑诊断。

完成条件：在 client 创建方明确 deadline 策略，修正 Locator 网络错误分类，以专用真实 Redis 验证 deadline、取消和 fencing 错误身份；不修改调用方注入的共享 client，不覆盖已成功结果。完整业务仍须按 [同配置请求预算](./architecture.md#63-请求预算与超时职责) 验收。

<a id="i03"></a>
### I03 · Node binding 的业务保活

**未解决。** binding 固定 6h TTL，`BindNode` 是覆盖写；有效旧进程在玩家改绑后再次 Bind 仍可抢回定位，不能作为安全续租。[当前存储实现](../locate/redis/node.go)

完成条件：先明确业务所有权、条件保活和原 ID 重启继承契约，再设计与现有进程 epoch fencing 相容的实现。进程仍有效、物理连接仍有效、玩家仍归本业务 owner 是不同事实，见 [Node binding 修改权](./architecture.md#node-binding)。

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

## 可选清理（P2）

剩余收益有限，实施前复核当前调用方和净收益，不能为减少行数改变行为或删覆盖：

| 范围 | 候选 | 必须保留 |
| --- | --- | --- |
| Node / network 文件组织 | 将 Node 入站 adapter、command context 集中到 dispatch；Connection 可选能力集中到 connection.go | 协议边界、fencing、声明及执行顺序 |
| 单点装配 | Node unary、Locator 条件删除、WS HTTP handler 的单点转交；删除已无读取者的 endpoint 回写及冗余分支 | nil/error/短路顺序、Lua、Upgrade/关闭时机；不删输入配置 |
| NATS 生命周期 | closeOnContext 等待既有 ctx.Done，移除重复 closeDone 通道 | 仅父 context 可取消时启动 watcher，保留 closeOnce 完成屏障和取消/显式 Close 竞争语义 |
| 测试 | 简化 NATS setter 表与单调用装配；合并相邻小文件、重复 TLS clone / handler 释放断言 | 全部输入、同步屏障、错误身份、释放时点和独特覆盖 |
| 局部表达 | host 的冗余 IP 判断、无冲突的 grpc import 别名 | 地址选择顺序与现有公开标识符 |

不恢复已否决的统一 TCP/WS 运行器、通用 BaseServer、直接替换条件 Registrar、合并两阶段排空等方向。强单活、在线 sticky 模式迁移、代理来源信任和业务执行模型需要独立需求；无实测瓶颈时不新增查询缓存或续租整形。
