# 为 Chora 贡献

[English](CONTRIBUTING.md)

Chora 是从源码运行的 Local Alpha，唯一经过验证的宿主机是 Apple Silicon macOS。
其他宿主机取得同等证据后才能声称支持。当前能力与待完成里程碑见[英文路线图](ROADMAP.md)。

## 范围与发布

- 贡献采用 [AGPL-3.0](LICENSE)，仅提交你有权按该许可证分发的内容。
- 保持改动聚焦，保留无关工作。
- 无法证明隔离或身份时，受管理执行必须 Fail Closed，不得静默回退到宿主机。
  旧 Codex Runtime 保持禁用。
- Local Connected 使用用户配置的 Pi，无需私有 M1 镜像，不提供 Sandbox 隔离。
  Alpha 验收不代表已具备发布条件或支持其他平台。
- 遵守[发布策略](.github/publication-policy.json)。秘密、私有资产与历史、维护者状态、
  验收记录、发布清单、生成产物、机器路径和私有代理地址均放在本仓库之外。
  不得加入 Vendor 依赖或未经审查的实验。

## 文档语言

公开文档以英文为权威来源。`README.md`、`ROADMAP.md` 等文件是主版本，
`.zh-CN.md` 文件是翻译。

- **读取：**以英文文档作为需求、设计、参考资料和当前状态的基准，翻译仅作辅助。
- **写入：**先更新英文版，包括计划、路线图里程碑和状态记录；同次修改同步已有翻译。
- 各版本的含义、状态、日期、命令、链接和支持范围保持一致；存在差异时，以英文版
  为准并修正翻译。
- 里程碑变化时更新路线图。汇报结果优先链接英文文档，可附上翻译版。

## 开发与验证

使用 Node.js 22.12+、npm 11+、Go 1.26+，通过 `npm ci` 和 `go mod download all`
安装依赖。

迭代时运行聚焦检查，为可观察行为提供相关测试；用户可见、持久化、API 或跨层
改动需运行 E2E。在已验证宿主机上，完整代码验收门禁为：

```sh
make test
make verify
make vet
npm run e2e
git diff --check
```

多个 checkout 并行验证时，可通过 `CHORA_E2E_PORT` 选择主测试服务端口（及其后一端口），
通过 `CHORA_JOINED_BASE_PORT` 指定联合测试端口基数（使用其后一端口）。
设置 `CHORA_E2E_OUTPUT_DIR` 将浏览器证据保存在仓库外。
`VITEST_MAX_WORKERS` 可限制前端测试并行度，不改变测试范围。

纯文档修改检查准确性、翻译一致性、链接和差异格式；仅在任务要求时运行全套测试。
源码测试使用自包含夹具，不认证历史私有发布物。明确列出通过、跳过和受阻的检查，
包括真实 Agent 的环境限制；说明行为变化、兼容性影响和已知风险。

## Git

使用英文 Conventional Commits：`<type>[(<scope>)][!]: <subject>`。

- type：`build`、`chore`、`ci`、`docs`、`feat`、`fix`、`perf`、`refactor`、
  `revert`、`style`、`test`；可选 scope 仅用于稳定子系统。
- 每次提交保持聚焦，标题具体、使用小写祈使句，目标 50 字符、最多 72 字符，
  不加末尾句号或 AI 署名。
- 非简单提交需空一行后写正文，按 72 字符换行，说明原因、结果、兼容性影响和实际验证。
- 破坏性变更需 `!` 和 `BREAKING CHANGE:` footer；仅引用真实 Issue。

维护者验证后提交并普通推送到 `main`，外部贡献者使用 PR；CI 在 `main` 上运行。
推送前核对仓库、远端、分支、身份和暂存差异，仅提交符合发布边界的本任务文件。
交付时报告提交 SHA、目标分支、推送结果和验证限制。

强推或重写已发布历史必须取得针对该操作的明确授权及所需仓库权限。分支删除仍禁用。
Tag、Release 和包发布需要单独授权。
