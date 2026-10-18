# Go 规范本地化与 Agent 文档整理

完整收录 Uber Go 原文与许可证。通用风格直接引用原文，项目补充与规范入口统一放在 `docs/harness/rules/go-style.md`，执行边界统一引用 AGENTS。

`AGENTS.md` 保留读取入口和执行边界；编码规则与项目补充集中到 `docs/harness/rules/`，操作流程放 `docs/harness/workflows/`。

项目设计、接入与验证说明集中到 `docs/project/`。Matt 的根目录 `GLOSSARY.md`、`docs/adr/`、`docs/agents/` 与本地任务保持原位。

`docs/README.md` 维护总索引与文档边界。每条规则只维护一处，目录迁移同步引用。

本任务仅修改文档，不调整代码、依赖或 lint 配置，不执行提交或推送。

长期 docs 以接入方式和对外行为契约为核心；完整装配由可运行示例维护，内部流程、字段和默认值按需链接源码。删除失效报告与独立待办清单，部署验收条件随功能文档维护；历史结果留在任务记录和 Git 中。规则与流程采用项目无关的表述。

实施与验证见 [任务 01](issues/01-local-guide.md)、[任务 02](issues/02-agent-doc-organization.md)、[任务 03](issues/03-docs-maintenance.md)、[任务 04](issues/04-docs-layout.md)、[任务 05](issues/05-reading-order.md)、[任务 06](issues/06-references.md)和[任务 07](issues/07-simplify-rules.md)。
