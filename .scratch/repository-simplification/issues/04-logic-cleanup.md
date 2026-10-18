# 04：复查剩余逻辑冗余

Status: resolved
Blocked by: 03

## 验收

- [x] 复查两个 module 的生产代码和全部测试。
- [x] 仅实施能确认等价且提高清晰度的变换。
- [x] 保留本轮起点的全部已有修改。
- [x] 完成必要验证和差异复审。

## Comments

基线为 `8711c10`。
上一轮的 28 份源码顺序调整已不在当前工作区。
AGENTS.md 的排列规则也已更新。
本轮按当前代码审查，不恢复上一轮顺序调整。

已有 AGENTS.md、docs/README.md 和任务 03 记录保持原样。
范围包含全部 `*_test.go` 和 `test/`。
重点检查重复逻辑、嵌套、无效中间变量及测试准备。
保留公开契约、错误身份、nil/empty、求值顺序、随机调用和并发时序。
不删除测试断言或行为覆盖。
不为制造差异重排函数或新增抽象。
不提交或推送。

复查 311 个手写 Go 文件和 12 个生成文件。
根 adapter/transport 的 84 份代码未发现有收益的剩余候选。
非 Go 配置、协议输入和归档报告未变，复用前两轮检查。

本轮仅改 12 个已有 Go 文件：5 份生产代码、7 份测试。

- Ludo 两处移动校验直接透传已有结果；calcNextPos 全部成功分支均返回 MoveOK。
- Ludo 删除重复的起点特判；相等时原公式也恒为零，其余位置、颜色和边界路径不变。
- Whot 直接透传房间校验结果，删除 NewUser 成功后不可达的 nil 判断。
- Whot 展平解绑流程，保留保存失败后继续解绑、context 检查次数及错误汇总结构。
- Gateway/Node 的 7 处端口轮询复用包内 helper；单次监听、立即关闭、等待预算及失败消息保持一致。
- 11 处房间配置和 4 处机器人创建复用现有测试夹具；字段和零值逐项相同，配置均在启动前完成。

仅在现有 test_helpers_test.go 中新增两个私有端口探测 helper。
不新增源码文件、类型、接口或通用层。
没有恢复上一轮顺序调整。

保留真实所有权、排空、取消和随机策略中的重复校验。
不替换牌堆复制：已有复制 helper 的 nil/empty 结果不同。
立即断言端口复用的 helper 检查 Close 错误，与轮询探测契约不同，保持原样。

AST 核对现有函数的签名和顺序不变。
常量、类型、顶层变量、注释与生成文件保持原样。
698 个测试、基准等入口及文件内顺序不变。
4,737 条断言、测试和清理调用记录保持一致。
主 agent 已复核全部差异和等价依据。

证据目录：`/var/folders/dz/h_3sbvgs2yd5bt20y9dxvg8m0000gn/T/yola-simplify-pass4-vjd_cz45/`。
修改前的 Gateway/Node、Ludo、Whot 定向基线均通过。
两个 module 的 make check、make lint、make race 全部通过。
命令退出码和日志路径记录在 checks.json。
独立复审全部 12 份 Go 增量，零发现。
隔离 Redis/etcd 下，Ludo/Whot 的真人、机器人共 4 个对局场景在 race 下通过，无跳过。
沿用默认 30s 观察窗口，未缩短采样时间。
命令和子场景结果在 integration.json，完整输出在 game-integration.jsonl。

```sh
cd test
go test -race -json -p=1 -count=1 -timeout=300s ./ludo/tools/press ./whot/tools/press -run '^TestGameDelivery$/(smoke|robots)$'
```

根 Gateway/Node 生产路径未变，复用任务 01 的真实分布式、TCP/WS、Sender 和超时验证。
Redis Cluster 管理用例和容量基准未运行，不提出性能或容量结论。
两个自建容器已回收，临时 AST 工具已归档到证据目录。
最终源码哈希与已复审、验证的版本一致。
未暂存、提交或推送。

AGENTS.md 在执行期间又被用户调整了文档措辞，已读取并保留。
本轮未改写 AGENTS.md、docs/README.md 或任务 03。

复盘：测试准备优先复用现有夹具，只显式设置场景差异。
删除条件前先证明成功返回、代数边界或所有权不变量。

后续可读性复审撤回 11 处房间配置抽取，见[任务 05](05-fixture-readability.md)。
