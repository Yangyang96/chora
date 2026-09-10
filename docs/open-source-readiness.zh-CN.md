# 开源就绪检查

[English](open-source-readiness.md) | [简体中文](open-source-readiness.zh-CN.md) |
[文档索引](README.zh-CN.md)

状态：**尚未达到公开发布条件**。M2-S1 本机 Pi 产品流程已验收，但日常基础、
公开上手、精确候选和发布检查尚未完成，见[路线图](../ROADMAP.zh-CN.md)。M1 私有
隔离输入和未验收的安装生命周期不构成 Local Connected 的公开启动前置。

本文只是就绪检查表，不能替代仓库许可证，也不构成安全策略、支持承诺或发布批准。
这里记录 Owner 已确认的决定，以及发布前仍需完成的操作。
具体顺序见[公开源码发布流程](release-process.zh-CN.md)。

## 发布前可安全修复

- [x] 为生成的浏览器抓取、本地输出和测试可执行文件补充忽略规则；继续跟踪依赖与
  源码 Lockfile。
- [x] 将包含固定个人绝对路径、绑定开发者环境的工具排除在第一版公开候选之外。
- [x] 将浏览器抓取、未经审查的 Spike、原始 Evidence、生成记录与固定源码 Snapshot
  排除在第一版公开候选之外。
- [ ] 在专用于发布的干净 Branch 或 Worktree 中审查精确暂存列表；不要对当前 Dirty
  Worktree 做宽泛暂存。
- [ ] 对精确 Release Candidate 运行仓库测试、Verify、Vet、与发布路径相关的 E2E、
  链接检查和 Secret Scan。
- [ ] 公开仓库建立后启用并测试 GitHub Private Vulnerability Reporting；仅有策略文档
  并不能打开这项仓库设置。
- [x] 提供双语文档导航，并标识当前指引、历史记录与版本固定证据。

## 待完成产品与公开路线门禁

- [ ] M2-S2-P1..P4 的 HTTP/基线/文件/检查基础验收。
- [ ] M2-S3 文档化启动、连续任务、数据维护及公共 CI 验收。
- [ ] 包含的 Commit/Push 已验收，或已验证外部 Git 交接且收窄声明。
- [ ] 精确导出候选不依赖 M1 私有输入，复现文档化本机 Pi 路线。

这是计划门禁，不表示其中每项实现都完全缺失。

## Owner 已确认的决定

- [x] 仓库采用 GNU Affero General Public License v3.0，见
  [`LICENSE`](../LICENSE)。
- [x] 使用 `github.com/Yangyang96/chora`；第一版保持 Local Alpha 标识，不声明为稳定版本。
- [x] 排除 Vendor 依赖，通过 Module Manifest 与 Lockfile 恢复；新依赖变更由 Dependency
  Review 与 Dependabot 检查。
- [x] 排除未经审查的 Spike、原始 Evidence、完整 Source-baseline Snapshot、生成的
  Distribution Artifact 与证书；只保留产品测试依赖的 Runtime-boundary Probe。
- [x] 增加中英文贡献、行为准则、安全和支持边界文档。
- [x] 安全问题使用 GitHub Private Vulnerability Reporting，公开支持使用 GitHub Issues；
  增加 Issue/PR Template、CI、Secret Scan、Dependency Review 与 Dependabot 策略。
- [x] 暂不提供私密的行为问题报告渠道；Owner 后续作出新决定前必须明确保留该限制。
- [x] 第一版保持 M1 Runtime Asset 与维护者认证私有；本机 Pi 可复现性是单独候选
  门禁，不是永久禁止声明。

## 保留风险与有意边界

- 保留的 M1 隔离 Source-checkout 路线依赖预置 Asset 与 Owner 提供的 OAuth；这些输入
  继续保持私有，不嵌入仓库。
- Installed Setup、Upgrade、GC、Uninstall 与认证分发是实现和设计参考，不是已验收的
  公开产品声明。
- 经过选择的历史决策和研究可以作为明确标记的上下文保留；原始 Evidence 与未审查
  实验由[公开源码范围](publication-scope.zh-CN.md)排除。
- Lockfile、Checksum、源码 Fixture 与有意保留的 UI 图片通常是可复现性输入，不应当作
  生成输出清理。
- 第一版公开候选排除证书和密钥，即使某个证书本身并不属于 Secret。

当前文档与历史证据的分类见[文档索引](README.zh-CN.md)，当前启动边界见仓库
[中文 README](../README.zh-CN.md)。
