# Chora

> Project 下有多个仓库资源和主题 Room；Task 属于一个 Room，并选择一个或多个 Project
> 仓库。先创建 Project、关联已有本地 Git 仓库，再打开默认 General Room 或创建命名
> 主题 Room。同项目的 Room 共享 Project 资源，各自保留任务、说明和历史。首版与后期
> 范围见[路线图](ROADMAP.zh-CN.md)。

[English](README.md) · [文档](docs/README.zh-CN.md) ·
[参与贡献](CONTRIBUTING.zh-CN.md) · [安全策略](SECURITY.zh-CN.md) · [路线图](ROADMAP.zh-CN.md)

Chora 是一个本地优先的 Human-Agent 协作开发工作区。用户描述想要的改动，
Chora 将它整理成边界明确的 Spec Coding 任务，展示计划、执行和来源明确的检查，
并把最终 Patch 的决定权留给用户。

> **项目状态：** 公开前的源码 Developer Alpha，限定 Apple Silicon macOS。本机 Pi
> Workbench Golden Path 已通过技术验证；S3 日常使用/公开可用性功能已经实现，集成资格
> 与整体用户验收仍待完成。
> 尚无冻结公开候选通过发布验收。详见[路线图](ROADMAP.zh-CN.md)。

## 日常 Workbench 数据目录

`chora workbench --source /absolute/path/to/chora` 默认使用 `$HOME/.chora/data`。
Task worktree 集中保存在 `$HOME/.chora/data/task-workspaces/<task-hash>/<repo-hash>/`，
两层均为 12 位哈希，完整身份保留在任务记录中。旧任务沿用已记录的目录；
目录被占用时会校验完整归属，不会冒领。不放进原仓库。隔离验收或自定义持久目录仍可显式指定绝对路径 `--data`。
已有实例继续使用其配置目录；默认值变更不会自动迁移已有数据。

新 Task 分支使用 `feature/<12位哈希>`、`fix/<12位哈希>`、`chore/<12位哈希>` 等短名称。
任务标题中的明确类型（如 `fix:`、`chore:`）优先，其次识别常见中文前缀，未识别时使用 `feature`。
哈希由完整 Task 与仓库身份生成；创建时校验分支所有权，冲突不会覆盖已有分支。
历史 Task 分支及其审阅、交付关联保持不变。分支名称与 worktree 目录相互独立。

## 为什么做 Chora？

Coding Agent 很有用，但工作过程经常散落在终端输出或不透明的外部会话中。
Chora 把协作过程本身保存为持久的产品状态：

~~~text
一句需求
→ Room 上下文与验收标准
→ 可见的技术计划
→ 选择仓库及各自目标分支，创建 Task 独占的 Git 分支/worktree
→ 可见的 Agent Attempt
→ 来源明确的检查
→ 可评审的 Patch
→ 评审并接受、请 Agent 修复，或修改需求
→ 将已审阅、未提交的改动保留在各自 Task worktree 中
~~~

Chora 把 Task、Attempt、决策、检查、Patch 和评审历史保存到 SQLite，进程重启后
仍能重新打开同一个 Room。新的多仓库 Task 使用独立分支，接受 Patch 只保存评审，
改动留在 Task worktree 中，尚未提交。结果页展示目标分支、Task 分支和目录位置。
Review 不会写回原目录。历史 detached Task 保留既有 Apply 兼容流程。

接受 Review 后，可逐仓库执行 Commit → Push → GitHub PR → Merge → worktree 清理，
每个动作独立预览和确认。GitHub.com 同仓库 PR 使用已安装并登录的 `gh`；
merge commit 遵从托管端规则。Commit/Push 不需要 gh。刷新只核对结果，不重放不确定写入。
清理要求已证明合并、Task worktree 完全干净（包括忽略文件），保留分支和审阅历史。
反馈需要改动时明确新建 Task、选择目标分支并重新 Review；旧审阅不授权变化后的 PR 内容。

## 当前已经能做什么

- 通过 Room UI 创建和重新打开 Spec Coding 任务。
- 一个 Project 可关联多个本地 Git 仓库和多个主题 Room；每个 Task 显式选择仓库资源。
- 自动生成计划，并展示进度、检查、阻塞项与决策。
- 为 Task 管理独立分支和 Git worktree，保留历史 detached worktree 兼容。
- 保存仓库目标默认值，允许每个 Task 单独选择目标分支，并支持自动、命名或显式不运行
  检查，无需手工文件清单。
- 人工 Review 后保留 Task worktree 中的未提交改动，原目录保持不变。
- 使用确定性的 Fake Agent 做本地开发与浏览器测试。
- 支持点击安装固定 Pi 0.85.1，也可复用兼容的 PATH Pi；在 Task worktree 本机执行，
  无需 Docker。
- 保留使用私有输入、独立验证的 M1 Pi/Docker 路线。
- 来源真实：Local Connected Pi 检查为 Agent-reported。
- 支持取消、恢复、重试、归档/恢复和进程重启后的持久化。
- 可显式关闭仍符合条件的 Result 剩余交付资格，保留文件和既有事实，并与仓库
  worktree 清理分开处理。
- 提供带披露确认的 `Trusted Local · No Sandbox` 显式执行 Profile。

## 先运行本地演示

最快的体验方式使用确定性 Fixture。它不会调用云端模型，也不能证明真实 Runtime
或 Sandbox 路径。

### 前置条件

- Node.js 22.12 或更高版本
- npm 11 或更高版本
- Go 1.26 或更高版本

~~~sh
npm ci
npm run web:build
CHORA_DEMO_ROOT="$(mktemp -d /private/tmp/chora-demo.XXXXXX)"
go run ./tools/e2eserver \
  --db "$CHORA_DEMO_ROOT/chora.db" \
  --web "$PWD/web/dist" \
  --port 8787
~~~

打开 <http://127.0.0.1:8787>。按 `Ctrl-C` 停止服务。临时目录不会自动删除。

## 本机 Pi Workbench 与保留的 M1 路径

Workbench 可以复用 PATH 中兼容的 `pi`，也可由用户显式点击，把固定 Pi 0.85.1 包
安装到 Chora 数据根目录。它不会安装 Node，也不会静默替换已有 Pi。Pi 要求 Node
22.19+，上方演示的较低 Node 门槛不覆盖 Pi。已有 Pi 的接入下限是 0.84.2；0.85.1
是受管理安装和真实 Golden Path 的精确版本。下限不等于所有更高版本均已验证。
凭据和模型配置由 Pi 管理：启动选定的 Pi，使用 `/login` 和 `/model`，再回到 Workbench
刷新就绪状态。每个 Attempt 显示 Pi 实际报告的 provider/model；未观察到的身份保持
unknown，修改 Pi 默认值不会重写旧记录。

开发入口是 `go run ./cmd/chora workbench`，提供绝对 `--source`、`--data` 路径
（data 在源码外）、已构建 `web/dist`，以及可选 `--port`（默认 8787）。通过 macOS
原生窗口选择已有本地 Git 目录，显式确认 Local Connected / No Sandbox。Clone 和
手工路径创建 UI 已移除。上手和维护界面已经实现；精确冻结候选复现及 S3/O5 集成
验收仍待完成，此处不声称已经通过。

这条路线不依赖 M1 私有 Docker 镜像或维护者 OAuth。Project 可关联多个已有 Git
仓库；每个 Task 选择仓库资源，并固定各仓库目标分支的 commit/tree 和检查策略。
每个仓库可选自动发现、持久化的命名检查或显式“不运行检查”，无需手工文件清单。
自动发现依据已提交的项目配置；无法识别或未观察到的检查保持“未验证”。设置修改只
影响新任务，已有任务保持原配置。

普通 UTF-8 文本文件的新增、修改、删除与重命名（展示为删除/新增）共同进入审核，
由用户 Review 后保留在 Task worktree，Commit 及后续交付需分别确认。
原目录 Apply 仅保留为旧任务兼容路径。当前 Task 分支的每个仓库通过 Review → Commit
→ Push → GitHub PR → Merge → Cleanup 逐步预览和确认。混合 Result 只有在先核对不确定
写入后，才能关闭仍符合条件的剩余交付资格；关闭会保留已经交付的事实和文件，并且
不等同于 Cleanup。大型仓库不再受整仓 4,096 文件或 128 MiB 的准入门槛限制；发现和
读取按需执行且保持有界。未修改的二进制、LFS、子模块和大文件可以留在仓库中；涉及
二进制/LFS/子模块内容或可执行模式的实际变更会在 Result 校验时明确拒绝。普通文本
单次读取仍有 8 MiB 单文件限制。忽略的构建输出不进入 Patch，无关文件和原工作区脏改动保留。
成功但无文件变更的任务保留报告，无需 Commit 或 Apply；缺失或截断的结果仍视为错误。
准备命令需要明确列出，依赖不会自动复制或安装。执行超时会显示，费用无法获取时保持未知。
这些实现能力不代表公开 Alpha 或发布候选已经验收。

下方保留的 M1 隔离路径仍需私有预置镜像/认证，不是公开安装入口，不得削弱其隔离。
[开发验证记录](docs/local-alpha-development-validation.md)是历史维护者资料，不是
当前 Workbench 启动指南。

<details>
<summary>仅供维护者使用的 Source-checkout 精确参考</summary>

这条保留路径要求 Apple Silicon macOS、Node.js 22.12+、npm 11+、Go
1.26+、Pi 0.84.2、通过 Colima 0.10.3 提供的 Docker Engine/CLI 29.6.1、
仅所有者可读的 Pi OAuth 文件，以及下列两个已经准备好的镜像。

```sh
npm ci
npm run web:build
CHORA_SOURCE_ROOT="$(pwd -P)"
CHORA_SOURCE_REVISION="$(node -p "require('./contracts/g2-m4/source-baseline-v6/manifest.json').source_revision")"
CHORA_TARGET_ROOT="$(dirname "$CHORA_SOURCE_ROOT")/chora-source-alpha-target"
CHORA_DATA_ROOT="$(dirname "$CHORA_SOURCE_ROOT")/chora-source-alpha-data"
CHORA_PI_IMAGE=sha256:91698efead5641a633519f5f229373e08a59264045ca27f6d01fc06505deeea7
CHORA_BOUNDARY_IMAGE=sha256:4f7746f3cdbe55dc454775ead5958a9ed8a78b93776ea1df598255c1606b25c6
export CHORA_PI_CODEX_AUTH_FILE=/absolute/path/to/owner-only-auth.json
chmod 600 "$CHORA_PI_CODEX_AUTH_FILE"
git worktree add --detach "$CHORA_TARGET_ROOT" "$CHORA_SOURCE_REVISION"
install -d -m 700 "$CHORA_DATA_ROOT"
DOCKER_CLI="$(realpath "$(command -v docker)")"

test "$(git -C "$CHORA_TARGET_ROOT" rev-parse HEAD)" = "$CHORA_SOURCE_REVISION"
test -z "$(git -C "$CHORA_TARGET_ROOT" status --porcelain)"
colima ssh -- test -d "$CHORA_SOURCE_ROOT"
colima ssh -- test -d "$CHORA_TARGET_ROOT"
colima ssh -- test -d "$CHORA_DATA_ROOT"
test "$("$DOCKER_CLI" --context colima image inspect "$CHORA_PI_IMAGE" --format '{{.Id}}')" = "$CHORA_PI_IMAGE"
test "$("$DOCKER_CLI" --context colima image inspect "$CHORA_BOUNDARY_IMAGE" --format '{{.Id}}')" = "$CHORA_BOUNDARY_IMAGE"

go run ./cmd/chora source-checkout \
  --source "$CHORA_SOURCE_ROOT" \
  --repository "$CHORA_TARGET_ROOT" \
  --data "$CHORA_DATA_ROOT" \
  --auth "$CHORA_PI_CODEX_AUTH_FILE" \
  --docker-cli "$DOCKER_CLI" \
  --docker-context colima \
  --port 8787
```

启动时会校验固定且干净的 checkout、虚拟机可见路径、精确镜像 ID 与
仅所有者可读的凭据；任何前置条件缺失都会关闭式失败。

</details>

## 执行 Profile

**Standard** 是保留的受管理隔离 Profile。它在通过 Qualification 的 Docker Sandbox
中运行受管理的 Pi Runtime，并绑定固定的 Capability Policy。当前启动使用
`--no-skills`；隔离状态不确定时一律 Fail-Closed。

**Trusted Local · No Sandbox** 是独立的显式选择。它在宿主机直接运行 Pi，可能继承
本地配置、工具、凭据、文件系统与网络访问。UI 必须先确认当前精确版本的披露策略。
它绝不会成为 Standard 的自动回退。

仓库还包含 Doctor、Setup、Upgrade、有界 GC 和 Uninstall 等已安装产品生命周期
实现。Packaging、认证 Asset 分发和 installed-product 验收尚未完成，因此它们目前是
参考实现，而不是受支持的安装路径。

## 仓库结构

| 路径 | 用途 |
| --- | --- |
| `cmd/chora/` | Chora CLI 与产品组合 |
| `internal/domain/` | Room、Task、Run、决策、评审与不变量 |
| `internal/app/` | 产品 Use Case 与 Port |
| `internal/localweb/` | 本地 HTTP API、UI 服务与 Runtime 组合 |
| `internal/agent/` | Fake 与 Pi Agent Adapter |
| `internal/dockersupervisor/` | 受管理的 Docker Sandbox 执行 |
| `internal/trustedhost/` | 显式的无 Sandbox 本地执行 |
| `internal/verifier/` | 独立验证 |
| `internal/store/sqlite/` | SQLite 持久化 |
| `web/` | React 与 TypeScript UI |
| `contracts/`、`distribution/` | 固定的 Policy 与 Release 输入 |
| `migrations/` | SQLite Schema Migration |
| `e2e/` | Playwright 与 Node.js 端到端检查 |
| `tools/` | 开发、验证和 Fixture 工具 |
| `spikes/` | 历史实验，不属于受支持的产品路径 |

## 开发

运行仓库检查前先安装依赖：

~~~sh
npm ci
go mod download all
make public-test
make test
make verify
make vet
npm run e2e
git diff --check
~~~

`make verify` 包含 Go Race Test，以及 Web Type-check、测试和构建。Playwright
可能需要在本地安装浏览器：`npx playwright install chromium`。

`npm run e2e:public`（或 `make public-e2e`）运行长期维护的确定性公开 Journey 和
Project-entry 浏览器覆盖，是无需凭据的公开 CI 入口，但不能证明真实 Pi 行为。
配置真实 Pi 的 Journey 需显式运行 `npm run test:e2e:pi-local-connected`；真实 Pi
用例被 skip 不构成验收证据。

`make public-test` 是当前可复现的公开源码门禁。维护者完整门禁还使用第一版公开候选不包含
的固定 Baseline 与 Source-bundle 输入。

提交改动前请阅读 [参与贡献](CONTRIBUTING.zh-CN.md)。[文档索引](docs/README.zh-CN.md)
会区分当前产品指南、历史决策与研究资料。

## 当前限制

- 只有一套 Apple Silicon macOS 开发环境通过验证。
- S3 Local Connected 上手、维护和公开确定性检查已经实现，但精确候选集成资格与
  用户验收仍待完成，剩余门禁见路线图。
- M1 私有 OAuth/镜像不公开分发，也不是 Local Connected Workbench 的前置条件。
- 公开 Packaging、Upgrade、发布恢复与分发尚未就绪。
- 多用户授权、远程 Worker、多仓库原子改动、自动 SCM 发布和生产交付属于后续工作。
- 未审查实验、原始证据、Vendor 依赖、Snapshot 与私有 Runtime/Authentication 输入
  不进入第一版公开候选，详见[公开源码范围](docs/publication-scope.zh-CN.md)。

## 社区、安全与许可证

- 贡献指南：[CONTRIBUTING.zh-CN.md](CONTRIBUTING.zh-CN.md)
- 行为准则：[CODE_OF_CONDUCT.zh-CN.md](CODE_OF_CONDUCT.zh-CN.md)
- 安全策略：[SECURITY.zh-CN.md](SECURITY.zh-CN.md)
- 支持状态：[SUPPORT.zh-CN.md](SUPPORT.zh-CN.md)
- 维护与脱敏诊断：[docs/workbench-maintenance.zh-CN.md](docs/workbench-maintenance.zh-CN.md)
- 公开源码范围：[docs/publication-scope.zh-CN.md](docs/publication-scope.zh-CN.md)

Chora 采用 [GNU Affero General Public License v3.0](LICENSE) 开源。
