# 02：Node 自行启动、登记和排空

Status: resolved
Blocked by: None

Node 通过标准 transport 启停，自行发布内部记录；业务无需配对准备 hook、metadata 或就绪 Registrar。

- [x] 构造无监听与租约副作用，Stateful 显式配置。
- [x] 启动登记和 health 与有效 epoch、准入一致。
- [x] 删除旧公开配对协议，保留类型安全 command 注册。
- [x] Stop 与迟到登记正确交接；注销失败继续排空；重复 Stop 可等待完成。
- [x] 请求、Drain、副作用排空与条件释放契约保持。
- [x] 替换旧协议测试并通过 Node 包检查。

## Comments

父规格：[自包含 transport](../spec.md)。目录隔离由 01 提供；本票使用注入的 Registrar，开发不依赖其具体实现。全仓调用方迁移由 05 完成。

`gpt-6.1-sol/xhigh` 完成。登记阻塞时拒绝 RPC、health NOT_SERVING、默认 10s 排空不被短 caller 等待截断、Drain 保留 AppInfo 均有 red→green。Node 全包、race、局部 lint、真实 Redis/etcd 替换保护通过。

最终复审补齐忽略取消的 Drain、迟到登记同时发生的回收顺序；主 agent 运行失败回归后修复。完整记录见 [review](../review.md)。
