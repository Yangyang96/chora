# macOS 应用

[English](macos-application.md) | **简体中文**

Apple Silicon 应用实现将 Chora、浏览器界面以及固定版本的 Node.js/Pi 依赖打包，
最低目标系统为 macOS 13。公开的签名、公证安装包尚未提供；当前公开 Alpha 入口
仍是 [README](../README.zh-CN.md) 的源码启动方式。ad-hoc 开发构建不代表已通过
Gatekeeper、公证或分发验收。

## 首次启动与认证

对于通过分发验收的安装包，将 `Chora.app` 拖入 Applications 后打开。
菜单栏应用准备受管理运行时，提供新建数据或迁移入口，启动本机回环 Workbench，
然后打开默认浏览器。安装后的机器无需 Go、Node.js、npm 或 Chora 源码。
任务仓库仍需 Git，以及自身检查、应用所需的工具。Docker 为可选依赖，仅用于
主动准备的隔离执行。本机执行保持无沙箱边界。

在 Chora 菜单选择 **Set Up Model Authentication…**。原生对话框使用 Pi 自己的
Provider 认证方法，包括 API key，以及 Provider 提供的浏览器或设备认证。
凭据存储和刷新由 Pi 负责；Chora 不将凭据复制到数据库或操作日志。
已有 Pi 配置会被复用。认证后回到 Workbench 刷新 Runtime readiness，再开始任务。
仍需满足 Provider 认证和网络条件。[第一个任务](../README.zh-CN.md#第一个任务)
介绍浏览器中的操作流程。

应用的数据、固定受管理运行时和操作日志位于 `~/Library/Application Support/Chora`。
运行时版本来自应用包并接受完整性检查；Chora 不会静默更新 Node.js 或 Pi。
Pi 保持自己的原生配置位置。维护者测试时可将 `CHORA_DESKTOP_SUPPORT_DIR` 设置为
绝对路径的临时目录；`PI_CODING_AGENT_DIR` 单独指定临时 Pi 配置目录。

## 生命周期与恢复

关闭浏览器后 Chora 继续运行；**Open Workbench** 可重新打开界面。
**Quit Chora…** 在停止任务、预览前确认，等待清理并保留任务状态。
清理无法确认时会报告错误并保留恢复状态，不会显示为干净退出。
重新启动可协调恢复状态，详情见日志。休眠或退出登录不保证继续执行，也不会自动重启任务。

**Back Up Data…** 与 **Restore Backup…** 要求任务空闲，停止服务后对完整 Chora
数据根目录操作。恢复前保留当前数据的安全备份。备份含敏感历史，应私密保存。
数据根目录中的 Task 工作区会被保留，但原始仓库、外部工作区及 Pi 凭据不随备份回滚。
请恢复到相同数据位置，保持记录的绝对路径有效。另见
[Workbench 维护](workbench-maintenance.zh-CN.md)。

首次启动的 **Migrate Existing Data…** 检查迁移记录及数据完整性，拒绝不支持或
更新的 Schema，要求旧服务停止，并在接管原位置前备份。保留原位置可避免破坏绝对
工作区引用。检查或备份失败时原安装仍可使用。不要让源码服务与应用同时使用同一数据目录。

## 更新与卸载

**Check for Updates…** 仅在主动请求时查询已发布的 macOS 资源。下载候选后选择
**Install Downloaded Update…**。安装要求任务空闲、应用及签名团队身份匹配、
版本更高、签名有效、已装订公证票据且通过 Gatekeeper；复制后会再次校验。
Chora 备份数据并保留上一版应用，安装后从 Applications 重新打开。
若启动失败，可恢复上一版应用及对应数据备份；不能假定旧版可读取新版写入的数据库。

退出 Chora 后将应用移入废纸篓即可卸载。数据、Pi 配置、受管理运行时及 Task 工作区
保留，便于重装；没有自动执行的破坏性数据删除步骤。

## 构建本地候选

在 Apple Silicon macOS 上使用 Xcode command-line tools、Python 3.12 或更高版本，
以及[开发工具链](../CONTRIBUTING.zh-CN.md)。先运行 `npm ci` 与
`go mod download all`，再使用源码目录外的新输出目录：

```sh
python3 tools/macos-package/build.py \
  --output /private/tmp/chora-macos-candidate \
  --version 0.1.0-alpha.2 --build-number 2 --dmg
```

构建器固定归档校验值与 Pi 依赖锁文件，打包可选的 Linux arm64 隔离助手，记录源码
身份和 npm SBOM，并校验产物签名。默认 ad-hoc 签名仅供本地开发，输出不得进入公开仓库。

`--identity 'Developer ID Application: …'` 要求干净、已提交的源码。该选项启用
带 hardened runtime 的分发签名，但**不会**公证或发布产物。正式候选仍需公证及
票据装订、干净机器 Gatekeeper 测试、首次任务及签名更新/恢复验收，以及对应的公开
源码和依赖声明。Developer ID 凭据与发布授权是独立前提。不要绕过 Gatekeeper，
也不要将 ad-hoc 构建称为受支持的公开安装包。
