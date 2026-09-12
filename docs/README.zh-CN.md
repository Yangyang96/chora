# Chora 文档

[English](README.md) | **简体中文**


首次使用请从[快速开始](../README.zh-CN.md#快速开始)启动 Chora，再按
[第一个任务](../README.zh-CN.md#第一个任务)尝试一次代码改动。
日常维护请看[备份、恢复与诊断](workbench-maintenance.zh-CN.md)。
下方参考资料保留产品计划和维护者历史记录。

[本地隔离模式](isolated-local.zh-CN.md)：公开环境准备、支持范围、边界和真实验收入口。

## 当前产品与验收指引

- [公开路线图](../ROADMAP.zh-CN.md)：已验收切片、待实施基础/SCM/公开可用性与候选门禁。
- [本机 Pi Workbench](../README.zh-CN.md)：多仓库 Task 入口、自动/命名/不运行检查、
  受管理或 PATH Pi、模型来源与 Task 分支交付；区别于保留的 M1 私有输入。
- [Workbench 维护、备份恢复与脱敏诊断](workbench-maintenance.zh-CN.md)：停止服务后的
  整数据根目录维护、所有权边界、升级限制与只读 Workbench doctor。恢复步骤仍需对
  精确冻结候选完成资格验证。

- [Local Alpha 产品边界与开发验证](local-alpha-development-validation.md)（当前仅英文）
  — 保留的 M1 隔离维护者证据，不是当前 Workbench 启动指南；安装生命周期尚未验收。
- [Task 所有的受管理 Git Worktree 决策](decisions/2026-08-24-task-owned-worktrees.md)
  — 已接受的产品决策。
- [UI 重写 E2E 场景映射](ui-redesign-e2e-scenarios.md) — 已接受的场景映射，以及仍需
  真实环境的测试边界。
- [开源就绪检查](open-source-readiness.zh-CN.md) — 当前发布检查表，不构成发布声明。
- [公开源码范围](publication-scope.zh-CN.md) — 第一版候选的纳入、排除与自动检查边界。
- [公开源码发布流程](release-process.zh-CN.md) — O5 v2 对现在可做、候选冻结后和仓库
  建立后工作的顺序划分。
- [依赖元数据预检](dependency-metadata.zh-CN.md) — 临时 npm SBOM、Go Module 清单与
  确定性公开候选导出检查。

长期维护、无需凭据的公开浏览器 Journey 入口是 `npm run e2e:public` 或
`make public-e2e`。真实 Pi 验证仍通过独立的
`npm run test:e2e:pi-local-connected` 显式运行；真实用例被 skip 不构成验收。

## 计划与实施记录

- [Task 所有的受管理 Worktree 计划](plans/2026-08-24-task-owned-worktrees.md)
  — 与已接受决策一同保留的实施计划。计划中的里程碑标签只是记录，不是独立的当前
  Authority。

## 历史决策与研究

以下文件用于解释被拒绝的替代方案并保留当时证据，不得覆盖当前产品指引。

- [OpenAI Agents SDK Sandbox Agents 决策](decisions/2026-08-05-openai-agents-sandbox-decision.md)
  — 已拒绝的 Spike 决策。
- [OpenHands Runtime/Sandbox 决策](decisions/2026-08-05-openhands-runtime-sandbox-decision.md)
  — 已拒绝的 Spike 决策。
- [OpenAI Agents SDK Sandbox Agents 来源审计](research/2026-08-05-openai-agents-sandbox-spike-sources.md)
  — 为已拒绝 Spike 保留的历史研究。
- [OpenHands Runtime/Sandbox 来源审计](research/2026-08-05-openhands-runtime-sandbox-spike-sources.md)
  — 为已拒绝 Spike 保留的历史研究。

## 仅本地保留的验证证据

`docs/validation/` 当前被版本控制排除，其中保存的是版本固定的本地证据，并非当前
Runtime 选型 Authority。在 Owner 明确决定经过清理的处理方式前，不应链接或公开它。
