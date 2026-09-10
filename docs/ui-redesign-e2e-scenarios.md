# Chora UI 重写 — e2e 验收场景重定义清单

> 状态：已决策并执行（2026-08-21）
> 背景：新 UI 把旧的多步仪式（手动计划编辑/评审、手动验证、Room 归档）简化成了侧边栏会话流。M1-U1/U2/U3 的 e2e 场景因此不再一一对应，需要逐条重定义，而不是简单改选择器。

> 历史记录：本文保留 2026-08-21 的验收边界。旧 U4、O4 和私有安装旅程的
> 脚本已移出公开源码，仅在维护者本地保留；下文旧命令不是当前公开入口。
> 当前浏览器回归运行 `npm run e2e:public` 或 `npm run e2e`。

## 当时的验证入口

- `e2e:standard` 跑公开新 UI 的 Plan 展示/自动启动、Patch Review、Accept、
  Ask Agent to fix、Change requirement 与 Task Archive/Restore。
- `e2e:joined` 跑同库重启后的精确 Patch Review 恢复与 Task
  Archive/Restore。
- `u4:real-route` 跑 `u4-real-rejection-route.spec.ts`（真实 Pi/Docker 路径 + 三类拒绝矩阵）。

## A. 保留、只需改选择器的场景（后端语义不变）

| 旧场景（spec） | 旧 UI 断言 | 新 UI 验证 |
| --- | --- | --- |
| Directory 精确恢复（room-directory-resume） | Directory 页 Room 卡片 | 侧边栏 Room 树 + 点击进入 `/rooms/{id}`（深链仍走 URL） |
| 懒加载 Run 历史（room-directory-resume） | "Load Run history" | 侧边栏展开 Room 懒加载 tasks |
| 真实 patch 评审 + 接受（persistent-room） | "Awaiting independent verification" → 手动 Start → reviewable patch → accept | 去掉手动 Start：Agent 完成 → **自动验证** → RunStream 评审（接受） |
| 拒绝 + Agent retry（persistent-room） | Reject → "Retry Pi" → successor | `Ask Agent to fix` 一次完成拒绝路由与 successor Attempt |
| 三类拒绝路由（u4-real-route） | 拒绝类别 | 技术分类保留在内部审计；公开 UI 只显示 `Ask Agent to fix` / `Change requirement` |

## B. 已确认的简化决策

### B1. 手动计划编辑/提交/评审（immutable-planning，M1-U1 语义）

旧：Edit Draft → Save（EDIT VERSION 2）→ Submit Revision → Accept Revision → Start Run。
新：计划**默认放行**（创建任务后自动 submit + accept + start Run）。

**决策**：Plan 作为 Run 时间线第一项展示，在既有 Room 权限内由系统激活并
自动执行，不要求人工审批。不可变修订、CAS 与血缘继续由 Go 契约测试覆盖。

### B2. Room 归档/恢复（room-directory-resume + joined-room 的 archived Room）

旧：Room 主页 "Archive Room" 按钮 + "This Room is archived and read-only"。
新：**Task 归档取代了 UI 上的 Room 归档**（后端 Room 归档仍存在）。

**决策**：公开 UI 主路径使用 Task Archive/Restore；Room 归档保留为后端能力，
不再是 M1 主验收路径。

### B3. 验证取消/重试（persistent-room 的 Cancel/Retry Verifier）

旧："Start Independent Verification" → Cancel → "Retry Verifier"。
新：验证**自动启动**，手动 Start/Cancel 按钮已移除。

**决策**：正常验证自动启动；`verification_recovery_required` 仍提供
`Retry Verifier`，只处理恢复，不恢复手动 Start 仪式。

### B4. 重启恢复（joined-room）

公开 joined 验收优先证明 Patch Review 与 Task Archive/Restore 的精确重开；
Verifier 中断恢复由后端恢复契约和专门 Verifier 测试覆盖。

## C. 需真机环境的场景

| spec | 原因 |
| --- | --- |
| u4-real-rejection-route | 真实 Pi/Docker Agent + Verifier 容器，沙箱内无法运行 |
| persistent-room 的完整 patch（若要求真实 Pi） | 该 spec 目前用 e2eserver 的 fake verifier + patch fixture，可本地跑；若改为真实验证则需真机 |

## 验收结论边界

本地 Fake/fixture 浏览器套件只证明 Chora 自有 UI、状态、Review、重试和持久化
语义。正式产品通过仍必须另跑安装版公开 UI 的真实 Pi + Sandbox + Verifier
链路；Fake 不可替代该门禁。
