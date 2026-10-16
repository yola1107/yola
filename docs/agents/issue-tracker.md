# 本地任务

Issue 和 Spec 使用本地 Markdown，保存在 `.scratch/<feature>/`。Skill 中的“发布任务”指写入本地文件，“读取任务”指按路径或编号查找。

## 文件约定

- 每个功能一个目录，规格放在 `spec.md`。
- 每个任务一个文件，放在 `issues/NN-<slug>.md`，编号从 `01` 开始。
- 讨论追加到任务文件的 `## Comments`。
- [部署验收清单](../issues.md)保留验收范围；实施任务引用对应条目，不复制清单。
- `Blocked by: NN, NN` 记录任务依赖；全部依赖 resolved 后才可领取。无前置任务写 `None`，外部条件单列。

## 普通实施任务

- `Status:` 使用 `ready-for-agent`、`claimed` 或 `resolved`。
- 领取无阻塞任务时，先写入 `Status: claimed`。
- 在 `## Comments` 记录结果、验证命令、证据和未完成项。
- 票内验收条件全部满足后设置 `Status: resolved`。约定验证未完成时保持未结；独立的部署验收继续由验收清单跟踪。

## Wayfinding

使用 `/wayfinder` 时，在功能目录内按以下约定记录：

- 地图使用 `map.md`，包含 `Notes`、`Decisions-so-far` 和 `Fog`。
- 子任务沿用 `issues/NN-<slug>.md`，正文写明待解决的问题。
- `Type:` 使用 `research`、`prototype`、`grilling` 或 `task`。
- `Status:` 使用 `open`、`claimed` 或 `resolved`。
- 领取任务时，按编号选择首个 open 且无阻塞的任务，先写入 `Status: claimed`。
- 完成时，在 `## Answer` 写入结论，设置 `Status: resolved`，再向地图的 Decisions-so-far 添加摘要和任务链接。
