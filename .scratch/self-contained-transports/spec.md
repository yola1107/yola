# 自包含 Gateway / Node

Status: resolved

## Problem Statement

应用必须配对 Metadata、BeforeStart、Server 和就绪 Registrar 才能正确接入。Node 的启动与回收跨多个所有者，Gateway 还有未被内部路由使用的注册流程。测试重复构造这些内部状态，增加重构成本。

## Solution

保留标准 Kratos App，将 Gateway / Node 的内部生命周期收回各自 transport。Node 自管内部目录登记；Gateway 只发现 Node。独立 Sender 支持普通无状态 Kratos 服务推送。仓内应用整体迁移，不维护旧接入兼容层。

## User Stories

1. 作为应用开发者，我希望使用标准 Kratos App 接入长连接，以复用已有工程结构。
2. 作为维护者，我希望官方 Kratos 源码保持不动，以继续使用官方版本。
3. 作为部署者，我希望多个 Gateway 与 Node 独立运行，以分别扩容。
4. 作为玩家，我希望换 Gateway 重连后仍路由到原 Node，以继续房间会话。
5. 作为业务开发者，我希望普通无状态服务独立使用 Kratos RPC，以避免被迫成为 Node。
6. 作为应用开发者，我希望构造 Node 不创建监听或申请租约，以安全装配依赖。
7. 作为运维人员，我希望就绪状态反映有效租约、内部登记与准入，以避免误报可服务。
8. 作为维护者，我希望启动失败和迟到登记由同一所有者回收，以免留下资源或错误记录。
9. 作为业务开发者，我希望 Drain 期间仍可解绑和推送，以完成业务清理。
10. 作为部署者，我希望失权实例停止工作，以保护新实例和玩家归属。
11. 作为服务开发者，我希望按目标游戏和 UID 推送，以避免创建有状态 Node。
12. 作为维护者，我希望测试描述公开行为，以减少实现重构导致的测试改动。
13. 作为部署者，我希望环境目录隔离且内部 endpoint 唯一，以避免错误发现业务 RPC。
14. 作为业务开发者，我希望长驻留有明确的条件续租，以避免归属在房间仍活跃时过期。

## Implementation Decisions

- Gateway / Node 仅通过 transport Start / Stop 与 App 协作，公开 BeforeStart、Metadata 配对和就绪 Registrar 删除。handler 在 Start 前注册。
- Node 从 Kratos context 读取唯一 App identity；有状态模式显式选择，与 Gate 查询和推送能力分开。
- Node 目录从环境 prefix 派生固定子空间。复用现有 Registrar / Discovery 和 etcd adapter，各登记拥有独立租约；注册错误回收本次租约，注销不能删除其他代记录。
- 内部 Node 记录恰有一个 grpc/grpcs endpoint。Gateway 拒绝缺失、多 endpoint 和 TLS 不符的记录。
- Node Start 准备监听、申请与核验 epoch、启动 RPC、登记记录，复核停止状态和租约后开放准入。自管 gRPC health 初始为 NOT_SERVING，准入开放才报告 SERVING。
- Stop 先关闭准入与 health，协调在途启动和登记交接，再注销、等待请求、执行业务 Drain、等待副作用并关闭网络。注销失败继续清理；未排空不得主动释放 epoch。
- 调用 Stop 的等待超时不能永久缓存为清理终态；重复调用可继续等待同一回收过程。业务 Drain 不重复执行，迟到资源仍由创建者回收。依赖不响应取消时不保证强制终止其 goroutine。
- Gateway 在内部 RPC 可用后才开放客户端认证；准备和失败回收进入 Start。GateBinding 提供回程地址，Gateway 默认不登记 etcd。
- 复用现有 epochLease、两阶段 admission、Gate token、条件存储修改、grpc-go resolver 和精确 picker。内部路由只在能收拢调用方知识时整理，不增加通用运行时。
- Sender 明确目标游戏命名空间与 UID，复用 Gate 查询与 gateclient；Node 的推送入口继续经过本代准入和 epoch 保护。
- Session 的原连接推送与 Sender 的当前连接推送保持不同语义。绑定时机和房间事务仍归业务；长驻留使用条件 RenewNode，不用 BindNode 充当续租。
- 应用保留 Wire cleanup，以处理 Kratos hook / 普通 Registrar 早退。共享房间状态的其他入口仍需业务统一排空。

## Testing Decisions

- 已确认的 seam 是 transport 生命周期、Locator / Registry adapter 和客户端端到端链路。优先复用既有测试和夹具。
- 生命周期通过启动、发现、RPC、health、停止及资源可重用结果验证；删除已消失的装配协议测试，不批量删除真实回归场景。
- 每个行为先运行失败用例，再完成实现。大范围接口迁移在同一集成分支完成，全部调用方迁移后要求两个 Go module 同时通过。
- 存储契约以真实 Redis / etcd 验证；快速用例使用现有本地替身。覆盖迟到登记、失权、重复 Stop、注销失败、排空超时、环境隔离和唯一 endpoint。
- 端到端覆盖两 Gateway、两 Stateful Node、跨 Gateway 重连、绑定精确路由、TCP / WS，以及普通服务 RPC 和独立 Sender。
- 最后执行 code-simplifier、受影响检查、双轴 code-review 和 retro。测试不得只断言实现字段或调用转发。

## Out of Scope

修改官方 Kratos / kratos-layout、全局 Host、自动房间恢复、强单活 actor、恰好一次投递、一致性哈希迁房、自动驻留框架、生产发布和 push。

## Further Notes

用户已授权实施与仓内迁移，现有 Redis 改动已单独提交为 `62847d5`。本轮重构在 `refactor/self-contained-transports` 完成复审，并获用户授权提交。临时设计报告提供背景，持久契约以领域词汇、ADR 与架构文档为准。

## 执行记录

Matt 工作流：领域建模与方案质询 → to-spec → to-tickets → implement-spec / TDD → code-simplifier → Standards / Spec 双轴复审 → retro。实施和独立复审使用 `gpt-6.1-sol/xhigh` subagent；主 agent 负责集成、最终深审、修复和验收。

| 顺序 | 任务 | 分工 |
| --- | --- | --- |
| 01 | [内部 Node 目录](issues/01-node-directory.md) | 主 agent |
| 02 | [Node 生命周期](issues/02-node-lifecycle.md) | Node subagent |
| 03 | [Gateway 生命周期](issues/03-gateway-lifecycle.md) | Gateway subagent |
| 04 | [独立 Sender](issues/04-independent-sender.md) | Sender subagent |
| 05 | [应用迁移与分布式验收](issues/05-integrate-applications.md) | 主 agent 集成，各 agent 按目录迁移 |
| 06 | [精简、复审与复盘](issues/06-simplify-review-retro.md) | 两个独立 reviewer，主 agent 最终修复 |

验收证据见 [review](review.md)，环境改进见 [retro](retro.md)。

后续内部流程与文件职责整理单独记录在 [任务 07](issues/07-module-locality.md)，基线为已提交的 `30ba7aa`。

在任务 07 的工作区基础上继续收紧职责与测试依赖，见 [任务 08](issues/08-cohesion-and-tests.md)。

主流程阅读层次、职责归属与后续测试合并见 [任务 09](issues/09-flow-and-locality.md)。

后续局部冗余与嵌套清理见 [任务 10](issues/10-redundancy-cleanup.md)。

测试夹具与准备流程清理见 [任务 11](issues/11-test-fixtures.md)。

排除应用 module 后的根 module 全量测试精简见 [任务 12](issues/12-root-tests.md)。

Gateway 连接与认证准入收拢、Whot 装配减法见 [任务 13](issues/13-gateway-admission.md)。

Node 依赖具名接口与 nil 校验见 [任务 14](issues/14-node-dependencies.md)。
