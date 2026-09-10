# 支持

[English](SUPPORT.md)

Chora 尚无公开发布版本或通用支持服务。当前源码 Checkout Local Alpha 仅在 Apple
Silicon macOS 上，按仓库 README 记录的精确工具链与 Runtime 边界完成验证。

项目不承诺响应时限、服务级别、兼容周期、公开下载或托管服务。可通过
[GitHub Issues](https://github.com/Yangyang96/chora/issues) 提交可复现 Bug、边界清晰的功能
建议或文档问题。请求帮助时不得发送凭据、私有资产或漏洞细节；安全问题请使用
[安全策略](SECURITY.zh-CN.md)中的私密入口。

已实现的 S3 行为与待完成的集成资格/发布门禁见[路线图](ROADMAP.zh-CN.md)。Local
Connected 可使用 Workbench 管理的固定 Pi 0.85.1，也可复用兼容的 PATH Pi；M1 私有
镜像不是其前置，它不提供 Sandbox 隔离。认证和模型选择由 Pi 通过 `/login`、`/model`
管理；问题报告不得包含 Pi 凭据或配置输出。

## 请求帮助前

先确认使用了文档规定的版本与命令。Workbench 数据或前置条件失败时，先运行
[维护指南](docs/workbench-maintenance.zh-CN.md)中的只读脱敏 doctor；再按需运行最小
相关检查，并记录：

- 命令与经过脱敏的简洁错误输出；
- 操作系统与 CPU 架构；
- 脱敏后的 `chora workbench doctor` 状态/finding code，或仅记录相关的 Node.js、
  npm、Go、Pi、Docker 与 Colima 版本；
- 失败发生在一次性 Fake-Agent 演示还是真实 Agent 源码路径；
- 预期结果与第一个观察到的阻塞项。

不要附加 OAuth 文件、环境变量转储、私有绝对路径、Docker Context Endpoint、
Image Archive、数据库文件或生成的验收证据。在 Intel macOS、Linux、Windows 或其他
Engine 上发生的问题可以作为探索信息，但不能证明这些配置已受支持。

确定性公开 Journey 失败时，请注明失败入口是 `npm run e2e:public` 还是
`make public-e2e`。真实 Pi 资格使用独立的 `npm run test:e2e:pi-local-connected`；
真实用例被 skip 不构成证明。

Issues 是尽力而为的社区入口，不构成支持合同。提交前请先搜索已有 Issue，并确保每个
Issue 只聚焦一个可以复现的问题。
