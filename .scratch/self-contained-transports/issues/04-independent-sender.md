# 04：普通 Kratos 服务独立推送

Status: resolved
Blocked by: None

普通服务使用目标游戏与 UID 推送当前连接，无需创建 Node 或申请 epoch。

- [x] Sender 只依赖 Gate 查询和既有 gRPC 回程实现。
- [x] 显式目标游戏命名空间，参数、错误和超时契约清楚。
- [x] 当前连接定位、错误、取消和关闭可通过公开 interface 验证。
- [x] 与 Node 受准入、epoch 保护的推送语义兼容，复用底层实现。

## Comments

父规格：[自包含 transport](../spec.md)。Node 调用方迁移及端到端由 05 验收。

`gpt-6.1-sol/xhigh` 完成。公开用例覆盖目标 namespace、重连、TTL/路由、TLS、middleware、取消、默认 3s 总预算及并发 Close。caller cancel 错误与关闭竞争有 red→green。push 包、race、lint 通过。Node 与 Sender 复用 gateclient；Node 保持单一连接池和原有准入/epoch。
