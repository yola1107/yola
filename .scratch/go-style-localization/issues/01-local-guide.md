# 01：收录本地 Go 规范

Status: resolved
Blocked by: None

## 验收

- [x] 固定上游提交，核对并完整保存原文及许可证。
- [x] 新增简短规范入口，明确读取时机、项目约定和更新方式。
- [x] 更新 AGENTS.md 与项目导航，核查链接、锚点和全部任务差异。

## Comments

采用与 Due 相同的上游提交 `1d60a91aa5e87d443002e23c21903c49489dbde5`。已从 Uber 官方仓库取得原文、许可证及目录记录，确认与 Due 的快照逐字节一致，上游无 NOTICE 文件。

使用 writing-for-agents：明确编码与审查时的读取条件，详细规范按主题加载；保留上游要求的强度、条件和例外。

已保存完整原文及许可证；go-style.md 记录来源、哈希、按主题阅读入口和更新方式。原 AGENTS.md 的项目约定集中到该入口，通用规则由原文承载；项目文档补充导航。

复审全部 7 个任务文件，核查 131 个本地链接与 89 个锚点。两份上游文件逐字节一致，全部文件空白检查通过；Go 源码、依赖与 lint 配置未改，因此未运行 Go 测试。未暂存、提交或推送。

证据目录：`/var/folders/dz/h_3sbvgs2yd5bt20y9dxvg8m0000gn/T/yola-local-go-style-lx6gixox/`，保存上游快照、目录记录、开工哈希和验证结果。

后续文档分工及迁移到 .harness 由[任务 02](02-agent-doc-organization.md)记录，以上为首次收录时的验证结果。
