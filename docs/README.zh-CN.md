# Chora 文档

[English](README.md) | **简体中文**

首次使用请从[快速开始](../README.zh-CN.md#快速开始)启动 Chora，再按
[第一个任务](../README.zh-CN.md#第一个任务)尝试一次代码改动。
日常维护请看[备份、恢复与诊断](workbench-maintenance.zh-CN.md)。
下方参考资料覆盖当前使用方式与公开源码检查。

[隔离执行](isolated-local.zh-CN.md)：公开环境准备、支持范围、边界和真实验收入口。

[按需应用预览](app-preview.zh-CN.md)：按需查看 Task Web 应用，不改变代码验收流程。

[macOS 应用](macos-application.zh-CN.md)：打包运行时、认证、迁移、更新与分发限制。

## 已接受的产品方向

- [项目工作流程与执行设置](project-workflows.zh-CN.md)：两种执行环境、可复用设置、
  历史 profile 兼容，以及软件项目内调研/方案/文档协作规划。
- [下一阶段个人工作空间切片](../ROADMAP.zh-CN.md#下一阶段个人工作空间切片)：
  M2-S8 → M2-S9、开始条件与可观察完成标准。

## 当前产品与验收指引

- [公开路线图](../ROADMAP.zh-CN.md)：已验收切片、待实施基础/SCM/公开可用性与候选门禁。
- [本机 Pi Workbench](../README.zh-CN.md)：多仓库 Task 入口、自动/命名/不运行检查、
  受管理或 PATH Pi、模型来源与 Task 分支交付；区别于保留的 M1 私有输入。
- [Workbench 维护、备份恢复与脱敏诊断](workbench-maintenance.zh-CN.md)：停止服务后的
  整数据根目录维护、所有权边界、升级限制与只读 Workbench doctor。恢复步骤仍需对
  精确冻结候选完成资格验证。

- [依赖元数据预检](dependency-metadata.zh-CN.md) — 临时 npm SBOM、Go Module 清单与
  确定性公开候选导出检查。

长期维护、无需凭据的公开浏览器 Journey 入口是 `npm run e2e:public` 或
`make public-e2e`。真实 Pi 验证仍通过独立的
`npm run test:e2e:pi-local-connected` 显式运行；真实用例被 skip 不构成验收。

## Skills 和 MCP

- [本机执行中的 Skills 和 MCP](native-capabilities.zh-CN.md)：项目配置、
  原生能力发现、失败状态和连接验证；[英文权威版本](native-capabilities.md)。

## 任务看板实施

- [任务看板使用指南](task-board.zh-CN.md)：Project/Room 看板与列表、待处理提醒、
  筛选、生命周期结果和新鲜度。
- [原生任务看板 V0 — M2-S6A（英文权威版本）](task-board-plan.md)及其
  [中文译文](task-board-plan.zh-CN.md)：**COMPLETE**，包含产品边界、生命周期投影、
  只读 API 和验收矩阵。

## 仅本地保留的验证证据

`docs/validation/` 当前被版本控制排除，其中保存的是版本固定的本地证据，并非当前
Runtime 选型 Authority。在 Owner 明确决定经过清理的处理方式前，不应链接或公开它。
