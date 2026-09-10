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
go mod download all
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


## 提交信息与维护者推送

Commit message 全部使用英文，采用 Conventional Commits：

```text
<type>[(<scope>)][!]: <subject>

<body>

<footer>
```

- type 限定为 `build`、`chore`、`ci`、`docs`、`feat`、`fix`、`perf`、
  `refactor`、`revert`、`style`、`test`。
- scope 仅用于稳定子系统，可以省略，不使用临时项目阶段名称。
- 标题使用小写祈使句，尽量控制在 50 字符内，最长 72 字符，末尾不加句号。
  描述改动结果，避免 `WIP`、`misc`、`updates` 等空泛措辞，不添加 AI 署名。
- 非简单提交必须有正文，与标题空一行，按 72 字符换行。说明原因和主要结果，
  再补充实质性的兼容性或迁移影响、实际验证。保持简短，按能力组织而非罗列文件，
  不要求固定小标题。
- 破坏性变更使用 `!` 和 `BREAKING CHANGE:` footer。只引用真实 Issue。
  每个提交保持一个完整、聚焦的主题。

示例：

```text
docs: make bilingual onboarding easy to follow

Lead with startup commands and the first task workflow so new users can
start without reading maintainer history. Keep both languages aligned.

Validate documentation links and matching shell examples.
```

维护者开发默认按“验证 → 提交 → 普通推送到 main”完成，不要求另建 PR。
外部贡献者通过 PR 提交；`main` 上的 CI 继续运行。
提交前审阅暂存差异，保留无关工作，排除私有历史、秘密、本地 Agent 状态和生成产物。
只有仓库管理员有权限强推 `main`；独立的生效规则阻止其他角色强推。
日常开发仍使用普通推送。Agent 只有获得针对具体历史重写操作的明确授权后，
才可使用强推权限。分支删除保持禁用；Tag、Release 和包发布仍是独立操作。
