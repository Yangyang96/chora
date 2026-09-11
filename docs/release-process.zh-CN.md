# 公开源码发布流程

[English](release-process.md) | [简体中文](release-process.zh-CN.md) |
[文档索引](README.zh-CN.md)

Chora 源码已公开，版本化预发布见 [Releases 页面](https://github.com/Yangyang96/chora/releases)。
这个维护者流程把稳定准备工作与只有产品候选字节冻结后才有意义的
检查分开。它落实当前 O5 v2 计划，不会恢复已被取代的 Installed-profile O4 Gate。

## 当前产品前置与范围

[公开路线图](../ROADMAP.zh-CN.md)要求当前多仓库基础、完整 Task 分支
Commit/Push/PR/Merge/清理和 S3 技术门禁通过后，自动准备并验证本地候选，再交
用户最终验收。Apply 保留兼容；当前候选不采用排除 SCM 的缩减方案。
冻结记录包含准确功能矩阵与源码身份，进行中的修复不是已验证候选。
Local Connected 公开复现不要求 M1 私有镜像或维护者 OAuth。

`PREPUBLICATION_READY_FOR_USER_ACCEPTANCE` 表示发布前技术准备完成；用户接受
准确候选后才可记录 `PUBLIC_DEVELOPER_ALPHA_READY`。本地冻结和验证不代表发布，
实际公开 Push/Merge、远端发布 Tag、Release/包/网站、仓库公开化和公告须由用户明确授权。
验收反馈产生修改时重验受影响内容并更新交付包。

## 什么事情何时可以做

| 工作 | 时机 | 原因 |
| --- | --- | --- |
| 维护双语 README 与社区规范 | 现在 | 产品细节可以变化，不影响规范结构。 |
| 校验仓库/许可证身份、排除路径、本地链接和 Workflow 语法 | 现在 | 这些检查可以保护后续每个候选。 |
| 维护 CI、Dependabot、Dependency Review 和 Secret Scan 配置 | 现在 | 仓库发布前即可审查配置。 |
| 演练临时 SBOM/Module 清单方法与候选导出 | 现在 | 不固定候选派生输出，也能持续验证工具。 |
| 固定截图、兼容性声明、版本、Release Note 和 Checksum | 候选冻结后 | 它们描述精确产品字节，产品变化后会过期。 |
| 生成最终 SBOM 和第三方许可证清单 | 候选冻结后 | 依赖输出必须和精确候选一致。 |
| 运行干净 Clone 安装、完整门禁和可检查历史的专用 Secret Scan | 候选冻结后 | 结果只对被审查的源码树和历史有效。 |
| 启用漏洞报告、Branch Protection、Required Checks 和仓库扫描 | 仓库建立后 | 这些是 GitHub 仓库设置，不是源码文件。 |
| 本地候选提交与专用私有测试仓库交付 | 准备 Goal 的明确授权范围内 | 在最终用户验收前产出可验证候选和流程证据。 |
| 产品公开 Push/Merge、远端发布 Tag、Release、公开化或公告 | 用户验收后实际发布 | 准备工作不等于发布授权。 |

## 候选冻结检查表

1. 创建干净的发布专用 Branch 或 Worktree，不要宽泛暂存当前混合 Worktree。
2. 按机器可读的[发布策略](../.github/publication-policy.json)生成精确公开源码树，逐项
   审查纳入与排除路径。
3. 将候选 Clone 到干净位置，只通过已提交的 Manifest 和 Lockfile 恢复依赖。
4. 运行：

   ```sh
   npm ci
   npm run docs:links
   make public-test
   git diff --check
   ```

   还须在导出候选上执行包含的公开浏览器流程及显式真实 Pi/macOS 验收。当前
   `make public-test` 不含浏览器 E2E，不能独自证明真实路线。记录选中用例和 skip，
   不用 M1 私有 fixture 替代；只有候选输入准确一致才复用相关产品证据。

5. 从精确依赖图生成最终 SBOM 和第三方许可证清单；人工审查例外，不复用旧报告。
6. 对精确候选及准备公开的历史运行专用 Secret Scanner。当前有界发布检查是额外保护，
   不能替代专用扫描。
7. 固定候选特定的状态、支持环境、限制、截图、版本、Release Note 和 Checksum。
8. 获得一次合并的独立就绪复核，并且没有未解决的 P0–P3 问题。

步骤 2 至 8 后，如果纳入范围内的产品或依赖字节发生变化，受影响的 Evidence 失效，
必须重新运行最小相关检查。

## 仓库建立检查表

GitHub 仓库建立后：

- 启用并测试 Private Vulnerability Reporting；
- 为 GitHub Actions 设置最小权限的默认 Permission；
- 配置 Branch Protection 与必须通过的公开 CI Check；
- 确认 Dependabot、Dependency Review 和 Secret Scan 实际运行；
- 在 GitHub UI 中验证 Issue 与 Pull Request Form；
- 在 Owner 后续作出新决定前，继续明确说明没有私密的行为问题报告渠道。

完成这些操作后，Owner 才单独决定是否发布。当前边界见[开源就绪检查](open-source-readiness.zh-CN.md)
和[公开源码范围](publication-scope.zh-CN.md)；可复用命令及其限制见
[依赖元数据预检](dependency-metadata.zh-CN.md)。

## 发布前交付包

提供可运行的验收入口、简短验收步骤、冻结候选路径/版本/提交或 SHA256、功能矩阵、
全部检查和独立审阅结果、许可证/SBOM/秘密扫描报告、备份恢复说明，以及准确目标、
命令、顺序和发布时核验项。提前只读检查已存在的目标；目标尚不存在或只能发布后
验证的状态须明确标为未验证并提供执行步骤，不能跳过其他可提前完成的门禁。
本地候选提交与专用私有测试仓库操作可按准备 Goal 的明确授权完成，不构成产品发布。
