# Workbench 维护

[English](workbench-maintenance.md)

> 使用备份记录的精确源码版本和数据布局。候选验收报告记录实际执行的恢复与迁移检查；
> doctor 检查成功本身不能证明备份可恢复。

本指南适用于 Apple Silicon macOS 上通过源码 Checkout 运行的 Workbench。首个公开
Alpha 的边界是：一个本机用户、一个显式绝对路径的 Chora 源码目录、一个显式绝对
路径的 Chora 数据根目录。本指南不定义在线备份服务、跨机器迁移或通用灾备系统。

当前 S3 候选源码使用 SQLite schema 41。Chora 尚无更早的公开版本，也尚无受支持的
跨版本升级或降级路径。本指南只描述使用匹配代码、在相同绝对布局下恢复。只有发布
说明明确列出已经验收的版本/schema 迁移后，才能把这些步骤用于升级。

## 先分清 Chora 拥有的状态

`chora workbench --source /absolute/path/to/chora` 默认使用
`$HOME/.chora/data`。如果启动时指定了 `--data`，这个参数的绝对路径就是数据根目录。
请记录该路径；默认值变化不会迁移已有实例。

完整数据根目录备份包括：

- `chora.db` 以及仍存在的 `chora.db-wal`、`chora.db-shm`；
- `task-workspaces/`，包括每个 Task 的各仓库 worktree；
- `pi-sessions/`；
- 通过 Workbench 安装 Pi 0.85.1 时产生的 `pi/`（安装内容归 Chora 所有，Pi 本机配置
  和凭据仍是独立状态）；
- `runtime/`，包括保留的 Review/Resource Patch 和运行记录；
- 同一数据根目录下由旧版 Chora 创建并保留的内容，包括可能存在的 `projects/`。

备份不包括：

- Chora 源码 Checkout 和构建后的 Web UI；
- Project 选择的原仓库、原仓库脏改动、Git 对象库和分支；
- 用户安装的 `pi`、Pi 本机配置和凭据；
- `gh` 配置和凭据；
- 数据根目录以外的任何文件。

Task worktree 通过 Git common directory 关联原仓库。原仓库必须继续位于记录的绝对
路径，且 Git 元数据和对象仍完整。只靠数据根目录压缩包无法恢复已删除或已搬迁的
原仓库。

## 维护前

1. 在 Workbench 中停止或完成运行中的 Run 和检查。先核对中断的 Apply、Commit、
   Push、PR、Merge 或 Cleanup。不得通过删除状态把结果不确定的写入变成可重试操作。
2. 保留原 Checkout 和 Task worktree 中的所有脏改动。维护过程不得执行
   `git reset`、`git clean`、`git checkout`、`git worktree remove` 或大范围文件删除。
3. 记录精确源码 revision 和相关前置版本，不复制凭据或环境变量：

   ```sh
   CHORA_SOURCE_ROOT="/absolute/path/to/chora"
   CHORA_DATA_ROOT="$HOME/.chora/data" # 使用过 --data 时替换为其精确值

   git -C "$CHORA_SOURCE_ROOT" rev-parse HEAD
   go version
   node --version
   npm --version
   if command -v pi >/dev/null 2>&1; then pi --version; else echo "pi unavailable"; fi
   ```

4. 在启动 Workbench 的终端按 `Ctrl-C`，等待命令退出。停止属于该 Workbench session
   的 Pi 进程。只要 Workbench 或 session 进程仍可能写入数据根目录，就不要复制。

构建 Workbench 源码要求 Go 1.26+、Node.js 22.12+、npm 11+。使用本机 Pi 要求
Node.js 22.19+ 和 Pi 0.84.2 或更高版本。真实验证使用过 Pi 0.85.1；最低版本不表示
所有更高版本都已经验收。

## 停止服务后备份整个数据根目录

备份目录必须位于 Chora 数据根目录之外。以下 macOS 命令把数据根目录作为整体归档，
并保留关闭后仍存在的 SQLite WAL/SHM 文件；不会修改原仓库。

```sh
set -eu
umask 077

CHORA_DATA_ROOT="$HOME/.chora/data" # 使用过 --data 时替换为其精确值
CHORA_BACKUP_DIR="$HOME/chora-backups/$(date -u +%Y%m%dT%H%M%SZ)"

test -d "$CHORA_DATA_ROOT"
mkdir -p "$CHORA_BACKUP_DIR"
CHORA_DATA_PARENT="$(dirname "$CHORA_DATA_ROOT")"
CHORA_DATA_NAME="$(basename "$CHORA_DATA_ROOT")"

/usr/bin/tar -C "$CHORA_DATA_PARENT" -cpf "$CHORA_BACKUP_DIR/data-root.tar" "$CHORA_DATA_NAME"
(
  cd "$CHORA_BACKUP_DIR"
  /usr/bin/shasum -a 256 data-root.tar > data-root.tar.sha256
)
```

把压缩包、校验和、精确 Chora 源码 revision 和启动参数放在一起保存。压缩包包含
敏感的本机数据，不得公开或附到 Issue 中。

只复制 `chora.db` 不构成受支持的备份。Chora 使用 WAL 模式；漏掉仍有效的 WAL
可能丢失已提交状态，而且只复制 SQLite 会遗漏 Task worktree、Pi session 和保留的
Patch 材料。

## 用匹配代码恢复到相同布局

只有 Workbench 已停止时才能恢复。数据根目录必须恢复到备份时相同的绝对路径，所有
被引用的原仓库也必须位于记录的绝对路径。不得把压缩包直接覆盖到已有数据根目录。

先验证压缩包：

```sh
CHORA_BACKUP_DIR="/absolute/path/to/the/backup"
(
  cd "$CHORA_BACKUP_DIR"
  /usr/bin/shasum -a 256 -c data-root.tar.sha256
)
```

然后把当前数据根目录保留为可逆副本，再把备份解压到原路径：

```sh
set -eu
umask 077

CHORA_DATA_ROOT="$HOME/.chora/data" # 原来的精确绝对路径
CHORA_BACKUP_DIR="/absolute/path/to/the/backup"
CHORA_RESTORE_HOLD="${CHORA_DATA_ROOT}.before-restore-$(date -u +%Y%m%dT%H%M%SZ)"

test -d "$CHORA_DATA_ROOT"
test ! -e "$CHORA_RESTORE_HOLD"
mv "$CHORA_DATA_ROOT" "$CHORA_RESTORE_HOLD"

CHORA_DATA_PARENT="$(dirname "$CHORA_DATA_ROOT")"
/usr/bin/tar -C "$CHORA_DATA_PARENT" -xpf "$CHORA_BACKUP_DIR/data-root.tar"
test -d "$CHORA_DATA_ROOT"
```

使用与备份精确匹配的 Chora 源码 revision，并指向已恢复的数据根目录。匹配源码需要
已经构建好 `web/dist`：

```sh
CHORA_SOURCE_ROOT="/absolute/path/to/the/matching/chora-source"
CHORA_DATA_ROOT="$HOME/.chora/data" # 已恢复的精确路径

cd "$CHORA_SOURCE_ROOT"
go run ./cmd/chora workbench \
  --source "$CHORA_SOURCE_ROOT" \
  --data "$CHORA_DATA_ROOT" \
  --port 8787
```

在删除或挪用保留目录前，通过 UI 核对：

- 备份中预期的所有 Project、Room、Task、Attempt、Result 和 Review 历史都存在；
- 所有已选仓库关联以及冻结的 base/check 配置未变；
- 每个仓库的 Commit、Push、PR、Merge、Cleanup 和旧 Apply 历史都保留；
- 保留的 Task worktree 能从记录路径打开，脏文件未丢失；
- 中断 session 仍可见，并通过已有 UI 动作 Resume、Refresh 或核对结果。

如果匹配代码无法打开恢复后的目录，请停止进程，保留失败的恢复目录供诊断，把之前
保留的目录放回精确的数据根路径，并使用与它匹配的代码。不得通过编辑
`schema_migrations` 修复不兼容，也不得让旧二进制写入已被新代码迁移的数据库。

## 源码升级与回退

Chora 尚无更早的公开版本，因此目前不存在受支持的公开版本升级路径。由任意发布前
源码 revision 生成的数据库没有跨版本兼容承诺。

未来只有发布说明明确给出以下信息时才能升级：源版本/revision、源 schema、目标
版本/revision、目标 schema、支持的 Pi/Node 范围，以及已经验收的迁移路径。请在
单独的干净源码 Checkout 中准备目标版本，不要 reset、覆盖或复用脏的 Chora 源码
目录。使用现有文档中的构建命令：

```sh
npm ci
npm run web:build
```

目标代码首次启动前，先停止活跃工作并完成上面的整目录备份。首次启动可能执行前向
迁移。如果迁移后升级失败，回退是：停止目标版本，把升级前备份恢复到相同路径，
再启动匹配的旧代码。只切换可执行文件或源码目录不等于数据库降级。

Pi 是独立的本机工具。升级后重新检查 `node --version` 和 `pi --version`。以交互方式
启动已选择的 Pi executable，再使用 `/login` 和 `/model` 完成本机认证和模型选择；
Chora 不收集这些凭据。Chora 不得覆盖或删除 PATH 中已有的 Pi、Pi 配置、Chora
数据根目录以外的 Pi session 或 Pi 凭据。

## 归档、清理与移除边界

Task 和 Room 的 Archive 动作会隐藏持久记录，也可能支持恢复。Archive 不删除文件，
也不能替代备份。

已交付 Task 分支的仓库级 **Cleanup** 要求 PR 已确认合并，且 Chora 拥有的 Task
worktree 完全干净，包括 ignored 文件。它删除该 worktree，但保留分支、Review 和交付历史。

不再需要的 Result 可通过 **Close result** 关闭剩余符合条件的仓库结果；此前的 Commit、
Push、PR、Merge 和旧 Apply 事实继续保留。关闭不删除文件。独立的 **Clean up closed result**
动作会检查精确关闭结果及保留内容；意外修改或额外文件会阻止删除。混合仓库结果中，
已交付并保留的仓库不进入该清理。每次删除都需要单独预览和确认，部分清理可以按仓库核对恢复。

不得手工删除 Task worktree，也不得清理活动或结果不确定的操作。先 Refresh/核对
`writing` 或 `recovery_required` 的交付。只有界面当前资格检查允许时才能归档；隐藏历史
不能解决待处理工作，也不能替代结果关闭。

用户显式启动安装后，Workbench 可以把固定的 Pi 0.85.1 包安装到 Chora 数据根目录
下的 `pi/`。该目录归 Chora 所有，并包含在整目录备份中。PATH 中已有的 Pi、Pi 本机
配置和凭据不归 Chora 所有。移除 Chora 源码或 Chora 自有数据时，绝不得删除这些
已有 Pi 及其配置或凭据、`gh` 配置、原仓库、Task 分支或用户脏改动。除非已经单独
决定丢弃全部 Chora 历史且持有验证过的备份，否则请保留数据根目录。

## 脱敏诊断与下一步

使用启动 Workbench 时相同的源码和数据根目录运行有界诊断：

```sh
go run ./cmd/chora workbench doctor \
  --source "$CHORA_SOURCE_ROOT" \
  --data "$CHORA_DATA_ROOT"
```

添加 `--json` 可获得稳定的机器可读输出。该命令以只读方式打开 SQLite，只报告 Chora
构建标识、schema/完整性状态、DataRoot 各组成部分的粗粒度可用性、有界记录数量，
以及规范化后的 Go/Node/npm/Pi 版本。它不输出私有绝对路径、数据库行、环境变量、
凭据、本机 Transcript 或仓库内容。退出码 0 表示被检查的 schema 和前置条件已就绪；
退出码 1 会给出稳定 finding code 和安全的下一步。现有 `chora doctor` 仍属于历史
installed/M1 preflight，不是 Workbench 数据诊断。两种 doctor 都不能证明备份或恢复
有效。

请求支持时只提供：

- 经过脱敏的 `chora workbench doctor` 输出（或其 `--json` 形式）；
- `uname -sm`、`sw_vers -productVersion`，以及涉及 GitHub 交付时经过脱敏的
  `gh --version`；
- UI 显示的稳定状态/原因、执行的动作、第一条经过脱敏的简洁错误；
- 数据根目录、原仓库和 Task worktree 是否存在，但不提供其私有绝对路径。

不得附加数据库、数据根目录压缩包、OAuth 文件、Pi/gh 配置、环境变量转储、本机
Transcript、仓库内容或完整私有路径。

按下表选择恢复方向：

| 现象 | 安全的下一步 |
|---|---|
| 原仓库或 Task 路径缺失/过期 | 停止操作，恢复记录的精确路径或备份。不要跨过保留数据重新关联或创建。 |
| 检查失败 | 保留声明的 argv、工作目录和来源；在 Task worktree 修复原因或请求新的 Agent Attempt。不得手工标成已验证。 |
| Session 中断 | 用相同源码和数据根目录重启，通过可见的 Resume/Refresh/核对动作恢复。保留 `pi-sessions/`。 |
| Push、PR、Merge 或 Apply 结果不确定 | 先 Refresh/核对，不得重放写入或清理 worktree。 |
| 目标分支前移 | 刷新目标状态；需要时创建新的、经过 Review 的交付授权。旧 Review 不授权变化后的内容。 |
| 执行 Profile 选错 | 停止 Attempt，在新 Attempt 前选择预期且已披露的 Profile。Local Connected 始终是显式的无 Sandbox 选择。 |
| 匹配代码拒绝 schema | 停止；使用匹配代码及其匹配备份，不得编辑迁移账本或原地降级。 |
