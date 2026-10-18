# Transport 自行管理内部生命周期

官方 Kratos v3 保持不动。Gateway 和 Node 继续作为原生 transport 接入；Node 自己管理内部发现记录、租约、准入和排空，应用继续管理标准 Kratos App 与外部依赖。这样可删除 Metadata、BeforeStart 和就绪 Registrar 的跨模块配对，避免增加通用 Host。

Node 目录位于环境 registry prefix 下的固定子空间，记录只包含一个内部 gRPC endpoint。普通业务注册保持独立；两类记录各自持有注册租约。Gateway 的内部回程地址来自 GateBinding，因此无需为长连接路由注册自身。

请求与副作用分阶段排空，Node epoch 和连接代次保护继续保留。Wire cleanup 负责 App 早退后的有界清理；超时不等于所有工作已经终止。构造期注册 gRPC handler，Start 才创建监听和登记外部记录。
