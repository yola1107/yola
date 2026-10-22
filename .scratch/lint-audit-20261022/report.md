# Lint 配置与告警复核

日期：2026-10-22。主分支仅修改 `.golangci.yml`，未修改 Go 源码、Makefile、AGENTS.md 或 CI 门禁。

## 验证范围与证据

- 源码基线：`77e6ad0672ff6ccc1a1e7e5a6d4d6961725e6ffd`。
- 主分支配置提交：`b0702ae7492c290d9302e3d868c4cf5d86ffe5b5`。
- 最终配置 blob：`c98bb8d95b40dd574848ff349ec1b87d35f479a8`，与复测配置完全一致。
- 环境：GitHub Actions，Linux amd64，Go 1.26.6，golangci-lint 2.15.0。
- 首轮：[run 39786903396](https://github.com/yola1107/yola/actions/runs/39786903396)，job `119852410360`，候选提交 `30fd4818f52dba8f6e3559baffb9064ee3a38f2a`。
- 最终复测：[run 39788264618](https://github.com/yola1107/yola/actions/runs/39788264618)，job `119857100352`，候选提交 `b898fa576ed086851b0f0a9961359f573ed40b09`。

本报告的数量、路径和退出码来自上述 job 日志中的 COUNTS、TOTAL、NEW_ISSUE 和 EXIT 记录；准确性判断来自源码复核，不是 lint 自动给出的结论。

## 实测结果

| 配置 | 根 module | test/ module | 合计 |
| --- | ---: | ---: | ---: |
| 原配置 | 0 | 0 | 0 |
| 首轮候选配置 | 9 | 12 | 21 |
| 最终配置 | 0 | 1 | 1 |

原配置两个模块的 lint 均退出 0；首轮候选均退出 1；最终配置根模块退出 0，test/ 模块退出 1。最终配置不是全绿状态。

`make lint-version`、`golangci-lint config verify --config .golangci.yml`、`git diff --exit-code` 和 `git diff --check` 均通过。未运行 Go 测试、race 或 make check；lint 分析测试源码不等于执行测试。

审计分别运行两个 module，避免根模块失败导致后一个模块不执行。每个 module 对新旧配置各运行一次，实际参数为：

```sh
golangci-lint run --config <仓库根目录下的配置绝对路径> \
  --timeout=8m --max-issues-per-linter=0 --max-same-issues=0 \
  --uniq-by-line=false \
  --output.json.path <报告.json> --output.text.path <报告.txt> ./...
```

上述占位符对应 Actions 日志中的绝对路径。基线配置由 `git show 77e6ad0672ff6ccc1a1e7e5a6d4d6961725e6ffd:.golangci.yml` 读取。审计工作流接受 lint 退出 1 以收集告警，所以工作流成功只表示收集完成，不代表 lint 通过。

## 最终配置

保留原有全部检查器、复杂度阈值、排除范围、gci 导入分组和 gofumpt。新增：

| 检查 | 范围 |
| --- | --- |
| errname | 哨兵错误与错误类型的命名 |
| predeclared | 遮蔽预声明标识符 |
| gocritic | 默认稳定检查，未启用全部或实验性规则 |
| errorlint | 仅错误类型断言和类型 switch；`asserts: true` |
| revive/early-return | 可通过提前返回简化的分支，使用 preserve-scope |
| revive/superfluous-else | 可去掉的 else，使用 preserve-scope |

Revive 显式启用默认规则，防止新增 rules 后丢失已有检查。新增 `max-issues-per-linter: 0`，不按检查器截断输出。

未启用 confusing-naming。errorlint 设置 `errorf: false`、`comparison: false`，不统一强制 `%w` 和 `errors.Is`；这两个维度仍需代码审查。没有新增 nolint 或文件排除来消除告警。

## 首轮告警类型与准确性

| 类型 | 根模块 | test/ 模块 | 判断与取舍 |
| --- | ---: | ---: | --- |
| errorlint：错误格式化未使用 %w | 3 | 2 | 五处都是 `%w: %v`，已包装分类错误，底层错误保留文本。是否继续暴露底层错误属于契约选择，不能统一判为错误。最终不启用该子检查。 |
| errorlint：错误直接比较 | 3 | 3 | 包含明确的身份断言、预期值分支，以及内部直接返回哨兵错误的比较。部分可改为 errors.Is 以兼容未来包装，但不等于当前逻辑错误。最终不启用该子检查。 |
| revive/confusing-naming | 3 | 6 | 命中仅大小写不同的名称。公开入口与私有实现可以合法并存，规则无法判断职责是否混淆，不适合作为本仓库统一禁令。 |
| gocritic/ifElseChain | 0 | 1 | 准确命中连续 if/else-if；改为 switch 可改善分支阅读。属于风格建议，不是已发现的运行时缺陷。保留。 |

### 关键复核

`node/push_test.go:286` 位于 `TestClientMiddlewarePreservesErrorIdentity`，明确验证返回的是同一个错误。将 `err == test.err` 替换成 `errors.Is` 会接受包装错误，从而削弱测试，不能按建议机械修复。

`network/internal/auth/reply_test.go:68` 比较的是测试表中的 `wantErr`，用于选择 EOF 身份检查；实际错误已经由 `require.ErrorIs` 验证。这里不是漏处理包装错误。

`test/internal/mailbox/group.go:126` 调用私有 `mailboxQueue.TryPost`。其队列满分支直接返回 `ErrFull`，目前直接比较成立；未来改变错误契约时应同步修改判断。

`internal/grpcendpoint/endpoint.go:89` 的私有 `validate` 由公开 `Validate` 调用，分别承担 endpoint 结构校验与协议、安全配置校验。可以进一步细化名称，但大小写相近本身不能证明实现有错。

`network/tcp/codec.go:50` 和 `network/websocket/client_options.go:192` 将错误分类包装进错误链，同时只保留底层错误文本。是否也包装底层错误应由公开错误契约决定。Go 官方说明：[Whether to Wrap](https://go.dev/blog/go1.13-errors#whether-to-wrap)。

### 首轮全部位置

| 类型 | 位置 |
| --- | --- |
| errorlint/errorf | `network/tcp/codec.go:50` |
| errorlint/errorf | `network/websocket/client_options.go:192` |
| errorlint/errorf | `network/websocket/codec.go:49` |
| errorlint/errorf | `test/ludo/internal/biz/handle.go:505` |
| errorlint/errorf | `test/whot/internal/biz/handle.go:474` |
| errorlint/comparison | `network/internal/auth/reply_test.go:68` |
| errorlint/comparison | `network/invoke_test.go:60` |
| errorlint/comparison | `node/push_test.go:286` |
| errorlint/comparison | `test/internal/mailbox/group.go:126` |
| errorlint/comparison | `test/internal/mailbox/group_test.go:107` |
| errorlint/comparison | `test/internal/mailbox/group_test.go:123` |
| revive/confusing-naming | `gateway/inbound.go:259` |
| revive/confusing-naming | `internal/grpcendpoint/endpoint.go:89` |
| revive/confusing-naming | `node/lifecycle.go:103` |
| revive/confusing-naming | `test/internal/timer/stdlib/scheduler.go:306` |
| revive/confusing-naming | `test/internal/timer/wheel/scheduler.go:433` |
| revive/confusing-naming | `test/ludo/internal/biz/table/table_mgr.go:50` |
| revive/confusing-naming | `test/ludo/internal/biz/table/test_helpers_test.go:188` |
| revive/confusing-naming | `test/whot/internal/biz/table/table_mgr.go:53` |
| revive/confusing-naming | `test/whot/tools/press/user.go:160` |
| gocritic/ifElseChain | `test/internal/pushbench/game.go:298` |

## 最终剩余告警

```text
test/internal/pushbench/game.go:298:2
ifElseChain: rewrite if-else to switch statement (gocritic)
```

该分支依次处理未找到请求、响应错误码、响应回调。可用无表达式 switch 保持顺序与互斥性；必须保留分支后的统一统计逻辑，不能简单提前返回。此次未修改该源码。

## 边界

命名语义、职责划分，以及多项 map/结构体每项独占一行，仍由 AGENTS.md 与审查约束；此次未增加专用 AST 检查。

Actions 的 Node.js 20 / punycode 弃用提示是运行环境提示，不计入上述 Go lint 告警。一次性审计工作流已从审计分支移除，运行日志与本报告保留；未向主分支加入工作流。
