# 04：按开发约定与项目说明整理文档

Status: resolved
Blocked by: 03

## 验收

- [x] 规则与流程迁入 `docs/harness/`，五份项目说明迁入 `docs/project/`；项目补充规则仍与通用规则集中存放。
- [x] Matt 的 `docs/adr/`、`docs/agents/` 和根词汇表保持原位且内容不变；Uber 原文与许可证逐字节保留。
- [x] 清理重复描述，完整保留授权边界、项目规则和对外行为契约，同步所有受影响引用。
- [x] 检查链接、锚点、空白及迁移差异，确认源码、配置和已有暂存内容未改变。

## Comments

按用户确认的布局实施，仅修改文档；不提交或推送。

迁移 10 份文件并同步引用；AGENTS 表格改为“文档｜场景”。清理旧目录，未发现其他空目录。

按 writing-for-agents 追加审核：核对已安装 Skill 的入口与 Matt 文档约定，精简规范来源、工具说明和重复契约，保留原文、项目规则与示例。EventBus 改为按匹配订阅分发，依据 `event/nats/subscription.go` 的 `ChanSubscribe`，消除“每个 Bus 实例一份”的误述。

验证：全仓 54 份 Markdown/HTML 的 329 个本地引用和 176 个锚点通过，44 个外部页面可访问；外部页内锚点未逐一验证。`git diff --check`、`git diff --cached --check` 及未跟踪文档的空白检查通过。11 个示例代码块、356 份非文档文件、Matt 文档、Uber 原文及暂存区与各轮开始时一致。仅改文档，未运行 Go 测试。
