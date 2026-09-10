# 安全策略

[English](SECURITY.md)

Chora 是仅在 Apple Silicon macOS 上验证过的源码 Checkout Local Alpha。它不是
公开发布版本，也不声明已达到生产安全标准或支持其他平台。

## 报告安全问题

请勿在公开 Issue、Discussion、Patch 或日志中披露疑似漏洞、凭据、私有 Endpoint
或复现数据。

公开仓库启用后，请使用 [GitHub 私密漏洞报告](https://github.com/Yangyang96/chora/security/advisories/new)。
疑似漏洞不得提交到公开 Issue。该入口只有在仓库所有者启用 Private Vulnerability
Reporting 后才能使用；正式公开前仍须启用并实际测试这项仓库设置。

报告只应包含复现所需的最少材料，不得发送凭据、私有 Runtime Asset，以及无关的个人
或公司数据。

当前公开目标和待验收范围见[路线图](ROADMAP.zh-CN.md)。Local Connected 使用用户
安装/配置的 Pi，M1 私有镜像不是其前置；它不提供 Sandbox 隔离。

## 当前安全边界

安全修复必须保留受管理执行的 Fail-Closed 行为、精确 Runtime 与 Engine 身份检查、
Sandbox 隔离、Owner-only 凭据处理，以及禁止静默回退到宿主机直接执行的规则。
`Trusted Local · No Sandbox` 是需要单独确认的显式模式，不得描述为隔离边界。

在当前 Local Alpha 阶段，项目不承诺响应时限、Embargo 周期、漏洞奖金、披露日期或
受支持版本策略。
