# 05：集中规则并调整阅读顺序

Status: resolved
Blocked by: 04

## 验收

- [x] 兼容策略移入项目编码约定，Go 规范的来源和更新说明集中到末尾。
- [x] 架构按组件职责、请求与路由、生命周期、底层约束排列；正文契约不变，受影响引用同步更新。
- [x] 开发说明恢复项目结构总览，覆盖全部一级目录与根文件，展开 test 和 docs，标明职责与 module 边界。
- [x] 本地链接、锚点和空白检查通过；AGENTS、Matt 文档、Uber 原文及代码不变。
- [x] 继续复查导航与引用文案，保留用户对 AGENTS 的后续修改。

## Comments

使用 writing-for-agents 落实已确认的三项规划。基线 `65f018e`；保留现有目录与项目文档，不提交或推送。

已调整内容归属与章节顺序，同步 4 处锚点引用，并按原有总览粒度补全项目结构树。

验证：55 份 Markdown/HTML 的 330 个本地引用、176 个锚点通过；逐节比较确认 7 个架构章节正文、Go 规则和 11 个原有示例块不变。目录树的 55 个条目均存在，根目录与 test 一级条目、docs 文件覆盖完整。`git diff --check` 及未跟踪文件空白检查通过。证据脚本为 `/var/folders/dz/h_3sbvgs2yd5bt20y9dxvg8m0000gn/T/docs-reading-order-vlgmm292/validate.py`。仅改文档，未运行 Go 测试；外链沿用上一轮结果。

续审：区分启动注册与请求执行，修正测试文档中的过期引用描述，保留用户对 AGENTS 的后续修改。55 份文档的 329 个本地引用、175 个锚点及空白检查通过；目录树仍完整，18 个示例块不变。验证命令：`python3 /var/folders/dz/h_3sbvgs2yd5bt20y9dxvg8m0000gn/T/docs-followup-puywxrxb/validate.py`。仅改文档，未运行 Go 测试；外链未变，复用此前检查。
