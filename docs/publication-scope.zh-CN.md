# 公开源码范围

[English](publication-scope.md) | [简体中文](publication-scope.zh-CN.md) |
[文档索引](README.zh-CN.md)

本文定义 `github.com/Yangyang96/chora` 第一版公开源码候选的预期范围。它不会发布仓库、
创建 Release，也不会让私有的真实 Agent 输入变成可公开复现的内容。

## 纳入范围

- Chora 产品源码、Web UI、Migration、Schema 与开发工具。
- 用于解释和验证受支持源码树的测试与 Fixture。
- 当前产品文档、经过选择的历史设计决策与社区规范文件。
- 用于可复现性的 Lockfile 与 Checksum，不包括 Secret。
- `spikes/runtime-boundary/` 下被产品测试直接使用的两个 Probe 文件；虽然保留在历史
  目录名下，但它们是经过审查的测试辅助程序。

## 第一版排除范围

- `vendor/`；依赖通过 `go.mod`、`go.sum` 与 npm Lockfile 恢复，并通过依赖自动化审查。
- 未审查的 `spikes/`、原始验证证据、本地运行输出、浏览器抓取、生成报告与 Agent
  编排状态。
- 指向仓库外部的 Agent 编排说明，例如本地实现仓库的 `AGENTS.md`；公开贡献指引以
  `CONTRIBUTING.md` 为准。
- 固定的 Source-baseline Snapshot 与生成的 Distribution Artifact。
- 证书、密钥、凭据、OAuth、私有 Runtime 镜像，以及绑定开发者本地验收路径的工具。

“排除”只表示不进入公开源码候选，不会删除维护者的本地文件，也不会改变已经接受的
Local Alpha 证据。

## 自动边界检查

`.github/publication-policy.json` 是机器可读策略。运行：

```sh
npm run publication:test
npm run publication:check
npm run publication:export:dry-run
npm run dependency-metadata:check
make public-test
```

检查范围是工作区内实际存在、由 Git 跟踪或未被忽略的候选文件。发现排除路径、未经
审查的 Spike、证书/密钥文件名、典型 Secret 格式、维护者主目录路径、公共规范文件
缺失、仓库身份漂移或许可证元数据漂移时，检查会失败。

这是一项有界检查，不能代替首次 Push 前对精确暂存列表的人工检查和专用 Secret
Scanner。最终候选仍须在干净、专用于公开发布的 Branch 或 Worktree 中准备。

`make public-test` 运行可以独立复现的 Go、Web、披露与公开边界检查。维护者完整门禁还
会使用第一版公开候选刻意排除的固定 Source-bundle 与 Baseline Fixture；CI 不会把这些
依赖私有输入的检查伪装成公共检查。

## 保持私有的产品输入

M1 隔离 Source-checkout 路线的预置镜像和维护者 OAuth 不公开。当前 Local Connected
Workbench 使用用户安装/配置的 Pi，不依赖这些输入。Fake 保留为无凭据演示。
公开候选必须按[路线图](../ROADMAP.zh-CN.md)和发布流程另行证明本机 Pi 可复现性，
不能把维护者环境的通过当成候选已通过，也不能因私有 M1 输入缺失而退回 Fake 声明。

## 历史测试适用范围

`e2e/public-test-applicability.json` 精确列出依赖不公开的冻结 M1/installed 夹具或历史
企业 CA 的测试。公开 Go 运行器在执行前按实际枚举结果核对包名和测试名，将这些条目
记录为**不适用**，不计作通过。同包内其他测试（包括后来新增的测试）仍会执行。
清单项缺失、重复或存在歧义时，门禁失败。

维护者完整 `make test` 和 `make verify` 仍使用原始输入执行全部这些历史测试。
公开结果清单位于 `output/public-go-tests.json`；运行时 skip 单独记录，不构成真实 Pi、
安装器、浏览器或私有托管旅程的验收。候选验收还须包含适用的独立执行证据。

## 公开浏览器测试范围

保留当前 UI 回归、确定性的 CI 旅程、必要的最小夹具，以及可自行配置的 Pi Local
Connected 测试。旧 O4/U4 阶段验收、依赖私有安装环境或 Docker 输入的旧旅程，
及其专用 OCR 工具不进入公开源码；对应 npm 命令与 Playwright 配置同步移除。
测试报告、trace、原始截图和缓存属于本地产物。`docs/images/` 中为文档挑选的
产品截图可以公开：使用虚构示例数据，发布前逐张检查私有路径和凭据。
发布检查会拒绝重新纳入上述排除路径。

冻结的 Source-bundle 清单及原始路径列表继续作为历史兼容记录保留，
不代表当前公开测试目录。私有副本与验收证据保存在本仓库之外。
