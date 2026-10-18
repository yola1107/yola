# 07：精简并合并编码规范

Status: resolved
Blocked by: 06

## 验收

- [x] 规范入口与项目约定集中到 go-style.md，删除 conventions.md，上游原文和许可证不变。
- [x] 项目约定保留明确偏好，删除通用规则的重复表述，取消强制夹具文件名并明确辅助函数的排列。
- [x] 引用、锚点、目录树和空白检查通过，AGENTS、代码和暂存区不变。

## Comments

使用 writing-for-agents，按已确认方案精简规则文档，不迁移代码，不提交或推送。

规范入口和项目约定合并为 43 行的 go-style.md，删除 conventions.md、中文摘要和重复流程。保留项目明确偏好，别名回归 Uber，夹具按职责组织；同步导航、测试约定链接和目录树。合并前后的项目条款一致。

验证：56 份文档的 270 个本地引用、118 个锚点、54 个目录树条目及空白检查通过；上游文件、AGENTS、代码和暂存区不变。命令：`python3 /var/folders/dz/h_3sbvgs2yd5bt20y9dxvg8m0000gn/T/rules-merge-_ocvvzln/validate.py`。外链沿用此前检查，纯文档未运行 Go 测试。
