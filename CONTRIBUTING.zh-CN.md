# 为 Chora 贡献

[English](CONTRIBUTING.md)

Chora 当前是从源码 Checkout 运行的 Local Alpha，并非公开发布版本。唯一经过
验证的开发宿主机是 Apple Silicon macOS。欢迎把其他宿主机上的工作作为探索，
但在取得同等证据前，不得声称已经支持。

## 贡献前须知

Chora 采用 GNU Affero General Public License v3.0 开源。提交贡献即表示你同意该
贡献可以在相同许可证下分发，并确认你有权提交该贡献。

新增文件前请阅读[公开源码范围](docs/publication-scope.zh-CN.md)。不得加入 Vendor
依赖、私有资产、原始 Evidence、凭据或未经审查的实验。

开始前先约定边界清晰的小范围任务。保留无关改动与未提交工作，不得削弱以下产品边界：

- 无法证明隔离或身份时，受管理执行必须 Fail Closed；
- 不得静默回退到宿主机直接执行；
- 旧 Codex Runtime 保持禁用；
- 私有发布资产、凭据与验收证据不得公开；
- Local Alpha 结果不等于发布或跨平台支持声明。

当前公开目标和待验收范围见[路线图](ROADMAP.zh-CN.md)。Local Connected 使用用户
安装/配置的 Pi，M1 私有镜像不是其前置；它不提供 Sandbox 隔离。

## 开发流程

使用 Node.js 22.12 或更高版本、npm 11 或更高版本，以及 Go 1.26 或更高版本。
运行聚焦检查前先安装 JavaScript 依赖：

```sh
npm ci
```

迭代时运行与改动最相关的 Go、Web 和端到端检查。涉及可观察行为、持久化、API
或安全边界时，应新增或更新测试。在已验证宿主机上接受改动前，完整门禁为：

```sh
make test
make verify
make vet
npm run e2e
git diff --check
```

部分真实 Agent 检查依赖私有、预置的 Local Alpha 环境。若无法运行，应准确列出
已经运行的检查与尚未验证的内容，不得用较弱证据替代支持声明。

保持改动聚焦，说明用户可见影响、契约影响和已知风险。贡献中不得包含凭据、
私有 Endpoint、机器专用路径或生成的验收证据。
