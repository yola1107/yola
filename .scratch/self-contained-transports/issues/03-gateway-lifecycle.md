# 03：Gateway 自包含接入与内部路由

Status: resolved
Blocked by: None

Gateway 仅作为 transport 接入，在内部 RPC 可用后开放 TCP / WS 认证；无需为内部回程登记自身。

- [x] 公开准备 hook 与 Endpointer 配对删除，失败回收收进 Start / Stop。
- [x] 重复 Stop、准备期间停止和认证排空保持正确。
- [x] 内部 Node 记录严格要求单一匹配的 gRPC endpoint。
- [x] 保留 WRR、精确 Node 路由、Gate token 与心跳并发契约。
- [x] 通过 Gateway 生命周期、路由及网络相关回归；旧 Node 装配调用由 05 统一迁移。

## Comments

父规格：[自包含 transport](../spec.md)。只在能收拢行为时整理内部路由调用面，不重写 grpc-go 连接管理。

`gpt-6.1-sol/xhigh` 完成。直接 Start、内部 RPC 先于客户端准入、唯一 endpoint、Stop 短等待及有界回收均有 red→green。Gateway 全包、race、真实 Redis/etcd 集成及 App 停止回归重复 20 次通过。

最终复审补齐后续准备阻塞时已交接 listener 的回收，以及自定义 Stop 迟到时其余网络先关闭。主 agent 修复并通过定向 race，见 [review](../review.md)。
