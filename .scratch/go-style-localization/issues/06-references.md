# 06：核对文档与关键字引用

Status: resolved
Blocked by: 05

## 验收

- [x] 修正错误字段名和不存在的许可证引用，继续核对文档中的 API 与源码入口。
- [x] 本地链接、锚点、目录树和空白检查通过，保留已有改动与上游原文。

## Comments

使用 writing-for-agents，仅修正文档引用，不提交或推送。

已修正 QueueDroppedCurrent 字段名，删除不存在的根许可证引用和无来源链接的 Due 过程说明；核对主要 API、基准名称、环境变量及源码入口。

验证：14 份文档的 246 个本地引用、157 个锚点与空白检查通过。目录树及 43 个外链复用上一轮结果，相关内容未变；外部锚点未逐一核验。AGENTS、原有示例、上游原文及暂存区保持不变。命令：`python3 /var/folders/dz/h_3sbvgs2yd5bt20y9dxvg8m0000gn/T/docs-reference-fix-zkt5smkn/validate.py`。纯文档未运行 Go 测试。

职责评审：Gateway 广播契约位于 EventBus 接入文档，建议集中到架构契约，EventBus 仅维护事件接线与投递语义。本轮评审未调整文档归属。
