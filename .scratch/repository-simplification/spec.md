# 全仓行为等价清理

基线：`3f59422`，开始时工作区干净。范围包含根 module、`test/` 独立 module、全部手写代码与 `*_test.go`。

使用 code-simplifier：逐目录检查重复流程、嵌套、命名、文件内主次顺序和测试准备。仅实施有明确清晰度收益且能说明等价性的变换。

保留公开接口、协议、配置、业务规则、错误身份、nil/empty 区别、求值及随机调用顺序、锁/defer 范围、I/O 和清理顺序。测试覆盖和失败断言不减少。生成代码及协议输入纳入核查，不手改生成产物。不修改官方 Kratos，不升级依赖。提交与推送按用户授权执行。

实施与证据见 [任务 01](issues/01-repository-cleanup.md)。

继续全仓检查与结构体定义整理见 [任务 02](issues/02-second-pass.md)。

当前工作区的逻辑清理见 [任务 04](issues/04-logic-cleanup.md)。

测试抽取的可读性修正见 [任务 05](issues/05-fixture-readability.md)。

调用关系和测试因果链清理见 [任务 06](issues/06-call-flow-clarity.md)。

声明布局与全仓清晰度清理见 [任务 07](issues/07-declaration-clarity.md)。

按职责组织代码与阅读流程见 [任务 08](issues/08-style-cohesion.md)。

参照 Kratos 的全仓布局调整见 [任务 09](issues/09-kratos-layout.md)。

并发测试失败后的清理修复见 [任务 10](issues/10-test-cleanup.md)。
