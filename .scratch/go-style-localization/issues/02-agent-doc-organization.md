# 02：整理 Agent 文档职责与读取入口

Status: resolved
Blocked by: 01

## 验收

- [x] 对照 Due 的 AGENTS 与 .harness，记录适合 Yola 的做法及不采用的部分。
- [x] AGENTS 保持精简并明确阅读时机；编码规则与操作流程集中到 .harness，Matt 配置保留 docs/agents；docs 按用途组织且保持唯一来源。
- [x] 保留现有约束，合并重复规则，复审全部任务文件；链接、锚点、上游快照与空白检查通过。

## Comments

本轮继续使用 writing-for-agents，承接任务 01 的未提交文档。Due 参考提交为 `0dfccd085435cd1724f83069e5067ad776c3e7ef`。

采用：明确阅读触发条件、区分通用规范与项目规则、按需加载操作流程、验证实际提交内容。编码规则放 .harness/rules，操作流程放 workflows；Matt 的 domain 与 issue-tracker 配置保留 docs/agents。保留 Yola 的中文原子提交约定、双 module 验证范围及本地任务流程。

按用户要求直接复制 Due 的 uber-go、go-style 与项目规范模板。Uber 原文和许可证保持逐字节一致；规范索引只改名称、路径和项目差异，专属规则保留本仓现有约束。文件使用 conventions.md，编号使用 001 起的数字，不加项目前缀。

不采用：重复建立架构笔记、复制 Due 的 JSON/errors/defer 规则或发布设计。本次只整理文档，不安装 hook、CI 或发布自动化。

AGENTS 精简为 25 行；docs/README 为 19 行索引，装配步骤和源码导航分别进入 integration.md、development.md。Matt 两份配置内容保持原样；源代码示例、既有架构约束及测试规则完整保留，移动后的引用已同步。

复审全部 15 个任务文件。运行证据目录中的 validate.py，234 个本地链接、156 个锚点、所有任务文件的空白检查通过；Uber 目录与 Due 逐字节相同，339 个源码与配置文件哈希未变。仅修改文档，未运行 Go 测试，未暂存、提交或推送。

证据目录：`/var/folders/dz/h_3sbvgs2yd5bt20y9dxvg8m0000gn/T/yola-agent-doc-organization-klc0qfnc/`。

复盘：先复用参考仓库的完整规范，只调整项目差异；Skill 配置与编码规则分开，避免为整理目录扩大迁移。
