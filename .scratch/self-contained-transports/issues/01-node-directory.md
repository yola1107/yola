# 01：隔离内部 Node 目录

Status: resolved
Blocked by: None

从同一环境配置派生 Node Registrar 与 Discovery，保留普通业务目录，使相同逻辑身份的两类记录互不干扰。

- [x] 现有环境 prefix 派生固定 Node 子目录。
- [x] 普通记录与 Node 记录互不发现，注销各自独立。
- [x] 真实 etcd 验证通过，沿用原有登记所有权保护。

## Comments

父规格：[自包含 transport](../spec.md)。基线为 `62847d5`。

验证：新增工厂与 Accept 循环就绪信号先因 API 缺失失败，补实现后通过。`YOLA_ETCD_INTEGRATION=127.0.0.1:12389 go test -race ./registry/etcd ./internal/listener` 通过；使用本轮专用 etcd。
