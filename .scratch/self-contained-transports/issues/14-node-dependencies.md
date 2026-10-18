# 14：统一 Node 依赖定义与 nil 校验

Status: resolved
Blocked by: 13

用户要求明确 Stateful(nil) 的行为并统一依赖定义。保留任务 13 的全部未提交改动。

## 范围与验收

- [x] 在现有 Node 文件定义 Store、GateLookup，供 options、Server、Stateful/GateDirectory 共用；不增加具体存储耦合或包装层。
- [x] 未配置 Stateful 保持无状态；显式传 nil 或带类型 nil 时构造失败，不能静默降级或延迟到运行期出错。GateDirectory 保持相同依赖校验。
- [x] 复用构造参数测试，先验证带类型 nil 的失败，再修复；原有有效存储、注册模式与推送测试通过。
- [x] 必要检查、code-simplifier、独立复审与 retro 完成。

仅调整 Node 依赖类型、对应校验及必要文档。`test/` 排除；不改 Kratos、推送生命周期或提交约定。后续获授权提交并合并到本地 main。

## Comments

测试入口为 NewServer；无需为接口提取增加测试文件。既有注册测试覆盖未配置和配置 Stateful 的两种模式。

RED：在既有构造参数表中增加两个依赖的普通 nil 与带类型 nil。使用合法 Registration 隔离目标校验；`go test ./node -run '^TestConstructionRejectsInvalidConfiguration$' -count=1 -timeout=30s` 仅两个带类型 nil 用例失败，实际得到 nil error。日志：`/tmp/yola-node-dependencies-red.log`。

结果：在 options.go 定义 `Store`、`GateLookup`，保留原方法集；删除测试中的重复接口，复用生产定义。nil 检查仅用于这两个 Option，值类型实现仍可用。未新增生产文件、包装结构或 lint 豁免。

GREEN 与必要验证：

```sh
go test ./node -run '^TestConstructionRejectsInvalidConfiguration$' -count=1
go test ./node -count=1
go test -race ./node -count=1
```

三项通过，包测试耗时依次为 0.900s、1.060s、2.487s；九个改动文件的 gopls 检查无诊断。最后将暂定 NodeStore 改为 Store，消除包名重复；仅改名后复用普通/race 结果，重新检查 Node、Gateway、根 examples 编译及 Node lint、格式，全部通过。日志为 `/tmp/yola-node-dependencies-store-{build,lint,format}.log`。

本轮未重新启动 Redis/etcd，也未逐项收集集成测试跳过记录；有效存储的真实依赖回归复用任务 13，存储调用流程未变。`test/` 未搜索、读取或验证。

code-simplifier 与最终复审：保留一个文件内 nil helper 和原构造参数测试表，删除重复接口；没有新增通用校验模块或重复用例文件。独立 Standards 复审 0 项；Spec 缺失、越界与实现错误均为 0。主 agent 复核所有本轮改动及任务文件，格式和 diff 检查通过。

retro：接口提取应先保持方法集，再用构造边界区分“未配置”和“显式无效依赖”。带类型 nil 由行为测试覆盖，无需增加运行期模式标志或新的全局规则。
