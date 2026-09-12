# Workbench 本地隔离模式

[English](isolated-local.md) | **简体中文**

Isolated Local 在 Docker 中运行 Pi，将校验通过的结果写回任务工作树。创建任务时显式选择此模式。
镜像、引擎、凭据或隔离边界不可用时停止执行，绝不自动切换到 Local Connected。

## 支持范围

- Apple Silicon macOS 主机，本地 Linux arm64 Docker Engine，Docker API 至少 1.44。
  环境准备必须通过绑定到确切引擎和镜像的能力探测。
- 固定 Pi `@earendil-works/pi-coding-agent@0.85.1`、Node.js `22.19.0`，
  Git `2.39.5`（Debian `1:2.39.5-0+deb12u3`）、Pi 原生 `deepseek` Provider 和 `deepseek-v4-flash` 模型。用户自备有模型访问权限的 DeepSeek API key。
  此模式不使用主机 Pi 的模型选择或 models.json。
- 至少有一次提交的 Git 仓库。首个支持的项目范围为 Node.js 标准库代码和测试
  （`node --test`），不包含包安装、原生编译、外部数据库或依赖服务。
- 保留任务不可变的各仓库基线、write/reference 角色、保护路径和检查策略。
  输入副本上限为 4,096 个普通条目、100 MiB；容器工作区另有 256 MiB 硬限制。

按照[快速开始](../README.zh-CN.md#快速开始)构建 Chora。安装 Docker 并启动本地引擎
（例如 Apple Silicon 上的 Colima）。用 `docker context use` 选定目标；
准备过程显式绑定此端点，不支持远端 Docker。

## 准备和执行

在新建任务面板选择 **本地隔离**，点击 **准备本地隔离环境**。
准备过程从公开 npm 获取输入，验证固定 Pi tarball 的 SHA-512 完整性，按已提交的
consumer lock 安装并禁用 lifecycle scripts，再编译公开检查助手。使用的官方 Node 镜像为：

```text
node@sha256:4a4884e8a44826194dff92ba316264f392056cbe243dcc9fd3551e71cea02b90
```

Git 及其 Debian 依赖来自固定的 `20260901T000000Z` 签名软件源快照，Git 包版本固定。
历史快照关闭索引过期检查，保留软件源签名验证。

无需私有 M1 镜像、企业 CA、维护者凭据或镜像仓库账号；可复用内容完全一致的本地缓存。
构建上下文仅含生成的必要输入，不复制完整工作区或 Pi 用户目录，也不使用模型凭据。
下载、构建或探测失败时，此模式保持不可用。

也可在公开源码目录使用相同的准备入口：

```sh
go run ./cmd/chora workbench prepare-isolated \
  --source "$(pwd -P)" --data "$HOME/.chora/data"
```

准备完成后，用相同 `--data` 目录重启 Workbench。每次执行前核对镜像、助手、observer、
引擎和策略身份。替换环境后必须再次重启；已有任务保持原契约，不能悄悄绑定到新镜像。

运行前，通过本机 Pi 配置 DeepSeek，使用户自己的 `~/.pi/agent/auth.json`（权限 `0600`）
包含类型为 `api_key` 的 `deepseek` 条目，key 必须是字面量；不接受 shell 命令或环境变量引用。
不要将 key 放入仓库、镜像或命令行参数。仅通过 stdin 将所选 key 注入容器内仅所有者可读
的内存目录，不注入其他 Provider 或 Git 凭据。投影随容器销毁；这不会撤销或轮换宿主原始
DeepSeek API key，其有效期、计费与撤销由用户在 DeepSeek 管理。Chora 不将容器内凭据
覆盖回本机 Pi 登录文件。

创建 Project/Room 下的任务，选择仓库、角色及检查策略并启动。执行后检查活动、按仓库分组
的差异和已观察到的检查。检查仍标为 **Agent 报告**；Docker 隔离不代表独立测试验证。
Review → Commit → Push → PR → Merge → 清理沿用现有逐仓库交付流程，保留旧 Apply。
Git 托管凭据只用于宿主交付流程，不注入 Pi 容器。

## 边界和恢复

| 边界 | 策略 |
| --- | --- |
| 文件 | 独立仓库副本；不将原始仓库、任务工作树、Docker socket 或无关宿主目录可写挂载。Git 元数据、参考仓库、上下文只读。 |
| 写回 | 所有仓库校验通过且证明容器死亡后，仅导入范围内的普通文本改动。源漂移、链接、元数据变更、越界写入均拒绝；导入使用回滚日志。 |
| 进程与资源 | 非 root uid 1000、只读 rootfs、移除全部 capabilities、no-new-privileges；2 CPU、4 GiB RAM、不额外使用 swap、256 PID、20 分钟。Docker VM 内另有任务专属 256 MiB 工作区 tmpfs，不计入容器 4 GiB 限制；容器内 `/tmp` 64 MiB、凭据 tmpfs 16 MiB。 |
| 网络 | 允许 Docker bridge 出站访问；无域名白名单，不使用主机网络、不发布端口、不提供 Docker socket。可能访问主机网络服务。 |
| 凭据 | 仅所选 DeepSeek API key，通过 stdin 注入仅所有者可读的 tmpfs，随容器销毁；模型请求使用网络。 |
| 结果 | 导出前冻结容器，导入前证明死亡。日志上限 10 MiB，可审阅产物/改动 100 MiB。取消或超时不导入部分结果。 |

取消停止选定 Attempt，证明终止后才可启动后继。Workbench 重启时只清理自身拥有的 Docker
资源。Resume 保留任务状态，以**新的隔离 Attempt**继续，不恢复容器内的 Pi 对话；
Review 反馈和任务工作树已有改动在同一冻结契约下接续。旧进程死亡未经证明时阻止启动；
恢复 Docker 并重启后重试，不可删除所有权记录绕过恢复。

交付完成或关闭不需要的结果后，使用 Workbench 显式清理；不删除原始仓库。
共享准备镜像保留供后续任务使用。删除应用数据前遵循[停服备份与恢复流程](workbench-maintenance.zh-CN.md)，
不要在 Workbench 或其他所有者仍使用数据时删除。

## 真实验收入口

使用独立且已准备的测试数据目录，与日常 Workbench 分开：

```sh
CHORA_ISOLATED_ACCEPTANCE_DATA=/absolute/prepared-test-data make isolated-acceptance
```

若需实际 GitHub PR → Merge → 清理，设置
`CHORA_ISOLATED_ACCEPTANCE_GITHUB_REPO=owner/disposable-test-repository`，并在宿主
为该测试仓库配置 Git/`gh` 登录。这会创建 fixture 和 Task 分支、创建 PR 并合入独立
fixture 分支，保留远端分支和 PR 证据，不操作默认分支。不设置该变量时使用本地 bare
remote 交付后关闭结果，这种运行不算实际 GitHub PR/Merge 验收。

入口要求真实 Docker 和 Pi 模型访问，前提缺失时失败，不跳过。创建临时 Git fixture，
验证多仓库执行、检查证据、Review 结果和原始仓库不变。这是实际集成验证，不表示全部历史
M1 或安装版测试已重跑。配套边界验证使用同一已准备镜像，不请求模型：

```sh
CHORA_ISOLATED_METADATA=/absolute/prepared-test-data/isolated-environment.json \
TMPDIR=/absolute/docker-shared-test-tmp \
go test -tags isolated_acceptance ./internal/dockersupervisor \
  -run TestRealWorkbench -count=1 -v
```

临时目录必须可被本地 Docker VM 共享。此验证覆盖只读挂载、凭据与资源限制、精确卷策略、
取消、恢复和自身资源零残留。合成 key 仅验证凭据边界；真实模型证据来自上述 Workbench 入口。

简短验收清单：

- 从公开输入准备环境，显式选择 Isolated Local。
- 取消、重启、Resume；使用参考仓库的信息修改可写仓库。
- 核实最终内容检查，Review 拒绝后修订并接受新结果。
- 在自有可丢弃 GitHub 目标完成 Commit、Push、PR、Merge、清理。
- 核实写入拒绝及错误隔离边界 fail closed，绝不回退宿主。
- 回归 Local Connected 与旧 Apply；skip 和历史排除项独立列示，不计为通过。

本次不包含 Minimal/Standard 双配置、第二 Provider、远程执行、团队功能或安装包。
