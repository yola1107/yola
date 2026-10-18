# 01：清理全仓手写代码与测试

Status: resolved
Blocked by: None

## 验收

- [x] 覆盖根 module 和 test module 的完整源码范围，记录保留项及原因。
- [x] 仅实施可论证等价且提高清晰度的变换；保留并发、资源、错误和测试覆盖契约。
- [x] 两个 module 的普通测试、必要 race、lint 与仓库检查完成；区分真实依赖执行和跳过项。
- [x] 主 agent 复审全部差异与未跟踪任务文件，记录结果和剩余限制。

## Comments

清单为 321 个 Go 文件（其中 12 个生成文件），另有 7 个 proto 输入。根目录与 test/ 没有额外 AGENTS.md。按根 adapter/transport、根核心与示例、Ludo/共享测试组件、Whot/测试 Gateway 分区检查；子代理沿用用户指定的 gpt-6.1-sol/xhigh。

先以清空外部集成变量的子进程运行两个 module 的普通基线，日志为 `/tmp/yola-simplify-{root,test}-baseline.jsonl`。不改变当前 shell 的环境或 Docker context。

### 覆盖与变换

| 范围 | 原 Go 文件 | 结果 |
| --- | ---: | --- |
| network、event、locate、registry、instance | 84 | 完整审查；仅 TCP 类型分支、排序及测试关闭屏障简化。 |
| gateway、node、push、internal、examples、api | 76 | 完整审查；展开多行字段，简化测试关闭屏障，示例先展示主流程。含 3 个生成文件。 |
| test/ludo、test/internal | 102 | 复用锁内清理、合并重复分支、展平正常路径，整理主次顺序与共用夹具。含 6 个生成文件。 |
| test/whot、test/gateway | 59 | 复用现有配置与玩家查找，合并普通出牌路径，删除前置返回已排除的 AI 死分支。含 3 个生成文件。 |

另审查 7 个 proto 输入、归档架构报告 HTML 及相关配置、Makefile。归档报告保持历史快照；12 个生成 Go 文件及协议未修改。测试包共用夹具集中到两个 `test_helpers_test.go`，未新增生产文件、公开接口、通用层或依赖。

等价依据：

- OnceFunc 仅替代已创建 channel 的幂等关闭屏障；保留关闭、等待、defer 与 cleanup 的调用位置。生产取消和锁协议不合并。
- Heap CancelAll 复用的锁内语句与原实现相同；wheel 在同一锁内合并同一个命中条件，锁外 Stop 顺序保留。
- Ludo 提前返回/continue 保持原求值与日志、推送、计分顺序；删除的私有 color 字段只有赋值，无读取或反射使用。
- Whot 的王牌评分在函数前部已覆盖所有手牌长度并返回，后部王牌分支不可达；链牌加分仍只执行一次。min 保留原整数加法，玩家查找复用原边界规则。
- 文件内排序及共用夹具搬移保持声明和函数体；独立 AST 比较确认 Ludo/共享部分 44 个跨文件声明一致，测试断言和初始化顺序保留。
- 全仓 698 个测试、基准等入口的包和名称集合保持一致，未删除覆盖。

保留项：Gateway/Node fencing、排空和迟到清理；Redis Lua、注册租约、NATS 生命周期；游戏随机策略、消息转换和压测异步时序。它们有独立契约，合并收益不足或会改变可观察顺序。

### 已完成验证

- 两个 module 的 `go test -json -count=1 -timeout=240s ./...` 基线通过：根 module 26 个含测试包，test module 31 个含测试包；另有 6/12 个无测试包。外部集成变量显式清空，跳过项保存在 JSON 日志。
- 子代理定向测试及 network 8 个包的 race 通过。
- `make lint` 覆盖两个 module，均为 0 issues。日志：`/tmp/yola-simplify-lint.log`。
- `make check`、`make race` 均通过，根 module 26 个含测试包、test module 31 个含测试包。check 包含 buf lint、两个 module 的依赖差异、vet、staticcheck、测试及 Git diff 检查。日志：`/tmp/yola-simplify-{check,race}.log`。
- 独立复审 Ludo/共享部分与根核心/Whot 部分均无发现；主 agent 复核 network 变换及全部其他差异，包含两个未跟踪夹具文件。最后仅按 AGENTS 拆开多行字面量字段，复用功能检查，并重新执行 gofmt/diff 检查。
- 隔离 Redis/etcd 下根部 5 个包的回归通过，实际执行 Gateway/Node 多实例、TCP/WS 与独立 Sender 场景；只跳过 Redis Cluster 的迁槽、切主和两个 cluster 子场景。
- 真实 Socket 顺序/重连及 WebSocket、Forward、Node 三层请求超时在 race 下通过，三个包没有跳过项。
- Ludo/Whot 的历史、当前配置两套预算下，真人和机器人场景在默认 30s 观察窗口及 race 下全部通过：8 个子场景，无跳过。

真实依赖验证命令（第二、三条在 test module 运行；依赖变量只指向任务自建实例）：

```sh
go test -json -count=1 -timeout=180s ./examples/env ./registry/etcd ./locate/redis ./gateway ./node
go test -race -json -p=1 -count=1 -timeout=180s ./internal/pushbench ./ludo/internal/biz/table ./whot/internal/biz/table -run '^(TestGameRequestTimeoutsAcrossWebSocketAndGRPC|TestTablePushSocketOrdering)$'
go test -race -json -p=1 -count=1 -timeout=300s ./ludo/tools/press ./whot/tools/press -run '^(TestGameDelivery|TestConfiguredGameDelivery)$/(smoke|robots)$'
```

未运行容量基准、100/500/1000 桌场景或 Redis Cluster 管理用例；本轮不提出性能或容量结论。

### 集成采样对照

首次将游戏观察窗口临时设为 3s：真人场景通过，机器人动作/座位断言失败。隔离复制基线 `3f59422` 后，Ludo 的两个机器人场景在相同窗口下也失败，均未观察到机器人动作；未修改测试断言或业务时序。最终恢复测试默认 30s 窗口，全部通过。

真实依赖日志位于 `/var/folders/dz/h_3sbvgs2yd5bt20y9dxvg8m0000gn/T/yola-simplify-z5jpq2y1/`。两个自建容器、基线复制和临时格式工具已回收，日志保留。最终 gofmt 与 diff 检查通过；未提交或推送。

复盘：等价清理优先复用已有行为证据和 AST 对照；并发/所有权代码收益不足就保留。机器人场景沿用既有观察窗口，避免把缩短采样造成的断言失败归因于重构。
