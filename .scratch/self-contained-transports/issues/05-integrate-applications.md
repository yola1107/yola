# 05：迁移应用并验证分布式链路

Status: resolved
Blocked by: 01, 02, 03, 04

仓内 examples、Whot、Ludo 与测试统一采用新接入，保持多 Gateway / Node 和普通 Kratos 服务协作。

- [x] 两个 Go module 全部调用方移除旧装配协议。
- [x] Node 推送仍受本代准入保护；游戏长驻留明确执行条件续租。
- [x] 真实 Redis / etcd 下多 Gateway / Node、跨 Gate 重连、TCP / WS、普通 RPC / Sender 验证通过。
- [x] 接入与架构文档描述最终接口，不保留相互冲突规则。

## Comments

父规格：[自包含 transport](../spec.md)。这是跨目录接口迁移的统一集成验收，前置票的局部检查不替代此票。

examples、Whot、Ludo、测试夹具和 Wire 生成文件已迁移。两款游戏复用既有状态循环执行条件 RenewNode；Ludo Session 读写补齐并发保护。原连接 Session.Push 与当前连接 Sender 的语义分别验证。

`make check`、`make race`、两 module 受影响包 lint 通过。真实分布式场景重复 10 次 race 通过；两款游戏默认 30s 的 smoke/robots、真实 WS 推送顺序验证通过。命令、已修复夹具问题和覆盖边界见 [review](../review.md)。
