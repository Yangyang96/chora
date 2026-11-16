# 先研究再编码

[English](research-workflow.md)

这项可选开发功能会研究公开的 Chora 文档，展示带来源的方案，等待任务所有者确认或
补充编码要求，再为原 Task 创建一个 Pi Run。检查、结果评审和显式交付继续使用工作台
原流程。接受研究方案不等于接受代码，也不授权发布。未选择研究时仍使用默认 Pi 流程。

## 配置与使用

按[Worker 配置说明](../services/workflow/README.md)安装锁定版本的 Python 包，
使用 Python 3.12 或 3.13，以及临时或明确配置的 UTF-8 PostgreSQL 数据库。执行说明中的
`init-db` 管理命令以添加 checkpoint 和桥接表；Worker 不会自动迁移数据库。
工作台打开 SQLite 时升级到迁移 0050。升级前备份数据库；其他功能分支迁移过的数据库
不能直接替代本功能的迁移历史。

将 `CHORA_WORKFLOW_PYTHON` 设为已安装 `chora_workflow` 的 Python 解释器绝对路径。
向工作台进程提供说明中的 `CHORA_WORKFLOW_DATABASE_URL`、模型及提供商环境配置。
`CHORA_WORKFLOW_CODE_REVISION` 应为实际部署的 40 位 commit，
`CHORA_WORKFLOW_SAMPLE_TYPE` 应如实标记采集用途。凭据仅留在操作者环境中，不写入
仓库、浏览器请求或诊断。已有验收账本和过期授权不构成新模型调用预算。

创建仓库 Task 时选择“先研究再编码”。工作台激活原有计划，冻结需求及已接受的上下文，
启动研究而不是 Pi。已有 Task 在计划已激活、尚无 Run 时也提供“开始研究，再确认编码”。
首版仅检索固定版本的公开 Chora 文档。任务私有材料仍冻结在 Go 执行快照中，不上传给研究。
本阶段未启用完整 RAG、私有来源工具或并发研究 Worker。

检查摘要和来源链接，可补充编码要求，然后选择“确认并编码”或“拒绝方案”。无答案的方案
不能确认编码。补充要求仍受任务原执行契约约束。“打开编码结果”进入原检查与人工评审流程。
刷新待确认页面仍保留同一个 gate。图、样本、预算和操作元数据仅在诊断中展示，主进度显示
业务状态。

## 恢复与边界

Go 权威管理项目/操作者授权、任务快照、人工决定、Run、Attempt、工作区和交付；Python
管理图推进和 checkpoint。一个 Task 只有一个调度器，旧委派与自动重试不推进研究管理的
Task。M1 只允许单研究 Worker，不提供自动多 Worker 租约或高可用承诺。

SQLite 先提交命令再投递，Python 将 inbox 和回执保存在 PG。
稳定的 `operation_id` 和载荷 `input_digest` 不随解释器、投递 attempt 改变。
Worker 身份、投递 attempt 与图 owner epoch 分别记录，不改变操作身份。
图操作保留 M0 journal 和 checkpoint fencing。新图操作前核对冻结的模型、提示词、工具、
语料、图、样本及部署版本。旧暂停任务需恢复原解释器与配置；修改默认值不等于图迁移。

gate 绑定所有者、项目、内容摘要和展示版本。相同决定重放返回同一结果；冲突决定和旧页面
会被拒绝。研究及补充要求作为独立、不可信的上下文传入 Pi，不重写快照或扩大权限。
研究仅使用固定公开目录，没有可跨项目或跨 epoch 复用的受保护来源授权接口。

创建 Run、操作回执和工作流映射在同一个 SQLite 事务中提交。响应丢失后查询并复用原 Run。
Pi 启动前，PG 记录同一个 Go 操作 ID、输入摘要和 Run 回执。此确认丢失后重放绑定，
仍保留已创建的 Run。
PG 与 SQLite 没有共享事务，桥接和 checkpoint 都不保证模型/工具外部副作用 exactly-once。

“停止研究与编码”先持久化停止意图，再操作 Worker 或进程。迟到回执不能撤销停止。
研究提交前到达的 Stop 会在 PG 留下持久停止标记。没有 runtime session 的 Run 可直接取消；
进程身份不确定时保留 unresolved。在 Stop 之前已接受的启动仍可能发生，必须由原 supervisor
取消并对账。

传输失败或 Pi 结果不确定时显示“需要对账”，暂停自动推进。检查原操作/Run 后可选择
“对账原有操作”。恢复沿用原命令或查询原 Run，不重置用量、截止、重试、模型版本和权限。
未知模型/工具 reservation 保持 unresolved。使用原 Run 的恢复流程补足 runtime 证据，
不要创建新 Run。桥接子进程上限 130 秒，超时可能留下不确定的图操作，需对账而非重新提交。

SQLite 持久保存递增事件序号。SSE 支持 `Last-Event-ID` 或 `after`，先补完所有分页再等待。
浏览器重连能补读终态，较旧投影不能覆盖较新的界面状态。

## 验证

运行仓库标准检查及锁定 Python 测试。局部命令：
`go test ./internal/localweb -run '^TestWorkflow'` 和
`python -m pytest services/workflow/tests/test_product.py`。后者需要
`CHORA_WORKFLOW_TEST_DSN` 指向临时 UTF-8 PG，各测试自行创建并删除独立 schema。

可选 Go 跨语言流程使用 `CHOR50_TEST_PYTHON`、`CHOR50_TEST_DSN`，先用 `init-db`
初始化专用 schema。每次桥接启动新解释器。构建前端并安装 Chromium 后，执行
`CHOR50_BROWSER=1 go test ./internal/localweb -run '^TestWorkflowBrowserJourney$'`，
在临时 Go 服务上检查入口、gate 刷新、修改后确认、评审、窄屏和浏览历史。

这些流程使用合成研究响应和 Pi supervisor 夹具，不证明真实模型质量、实际 Pi 代码修改、
人工业务批准、多 Worker 接管或生产运行。真实集成另需明确模型预算、已有 Pi 访问和真实人工
交付决定。
