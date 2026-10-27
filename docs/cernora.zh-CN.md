# 可选 Cernora 完成记录 Adapter

[English](cernora.md) | **简体中文**

Chora 在 `tools/cernora/chora_adapter.py` 提供显式选择的文件 Adapter，接入
Cernora Preview `agent-run-export/v1` 检查器。它读取一份已终止、已脱敏的记录，
确定性导出检查文件；不访问 Chora 数据库、执行注册表、凭据或 Review API。
Cernora 保持外部离线评估器边界。

这是首个文件接入点，尚不自动采集现有 Workbench Run。当前 UI／事件 API 保留的是
显示摘要，不是完整 argv/stdout 证据，不能直接作为此完成记录。外部采集 Harness
必须提供实际事实；不得从显示文本还原命令、补造 stdout、从散文提取断言，或将
预期答案当成观测断言。

## 环境与三路径验收

使用仓库外的 Python 3.12 或 3.13 环境，安装 Cernora 及其依赖。环境必须包含
[Cernora 检查指南](https://github.com/Yangyang96/cernora/blob/main/docs/public/agent-run-inspection.md)
所述的 Preview 检查 API；仅有 `0.1.4` 版本号不能保证包含这项未发布 API。
Chora 不自动安装或激活该环境，也不给 Workbench Runtime 增加 Python 依赖。

在 Chora 仓库运行：

```sh
CHORA_CERNORA_PYTHON=/external/cernora-env/bin/python \
  npm run test:e2e:cernora
```

测试仅在仓库外临时目录创建合成记录，通过子进程运行 Adapter 和实际 Cernora CLI，
每条检查路径重复三次，验证输出字节一致：

| 演示 | 记录事实 | Cernora 观测 | 检查器退出码 |
| --- | --- | --- | --- |
| pass | 工具退出 0，stdout 和结构化断言为 `ready`；独立参考为 `ready` | `evidence_available`，断言准确率 1.0 | 0 |
| fail | 工具退出 0，stdout 和结构化断言为 `busy`；参考为 `ready` | 断言不匹配，准确率 0.0 | 1 |
| 缺证 | 有工具调用，但未记录结果；不补造输出或断言 | `inconclusive`，缺少一个结果 | 3 |

这里 pass/fail 仅指**检查观测**，不代表任务质量门禁或人工接受。检查器退出 0
也不能接受 Chora Task。完整 EvidenceBundle v2 导入／评估需要明确冻结的
Profile/Case 及其权威；此 Adapter 不将检查 JSON 改名为 Bundle，也不实现该独立路径。
它不改变 Run、验证或 Review 状态；合成测试不构成真实 Pi 采集或隔离验收。

## 完成记录契约

`chora.cernora-completed-record/v1` 是外部采集 Harness 的新增可选交换契约，
不是 `/api/runs/{runID}/events` 的响应格式。下列字段均必填；未知字段、重复
JSON 键、非有限数、无效 UTF-8、非终止记录和无效事件会被拒绝。

| 字段 | 含义 |
| --- | --- |
| `schemaVersion` | 固定为 `chora.cernora-completed-record/v1` |
| `completed` | 必须为 JSON `true`，表示采集结束，不表示成功 |
| `runId`、`taskId`、`caseId` | 已记录身份；`runId` 应区分 Attempt，`caseId` 必须显式选择 |
| `conditions` | Cernora 的 `agent`、`model`、`prompt_version`、`budget`、`toolset_version`，来自实际有效配置 |
| `userTask`、`finalAnswer` | 实际任务和最终公开答复，不含私有推理 |
| `events` | 保持观测顺序，`sequence` 从 1 连续递增，`timestampMs` 不倒退，包含 `type`、`payload` |
| `claims` | 实际结构化最终断言：`path`、JSON `value`、`evidence_ids`；没有则传 `[]` |

`conditions.budget` 是测得／配置的总 Token 预算；未记录则显式为 `null`，不能
用超时秒数或虚构的零代替。其余配置字段必须是已记录、非空的版本标识；必需身份或
配置缺失应修复采集，不能由 Adapter 推断。时间戳是 Harness 记录的毫秒，不能证明
账单或总运行时间。

事件映射保留并发交错，不排序，不按工具名配对：

| 源类型 | Cernora kind | 精确 payload 字段 |
| --- | --- | --- |
| `user`、`final` | 同名 | `text` |
| `assistant_message` | `assistant` | `text` |
| `tool_start` | `tool_call` | `invocation_id`、`tool`、`argv` |
| `tool_end` | `tool_result` | `invocation_id`、`exit_code`、`timed_out`、可选 `evidence_id`、必填 `stdout` |
| `error` | 同名 | `code` |

调用 ID 必须唯一；结果必须在对应调用之后。缺失结果保持缺失。`stdout` 必须是
实际观测的 UTF-8 字符串（可以是观测到的空串）并带唯一 `evidence_id`，或者显式
为 `null` 且没有证据 ID。此契约只投影 stdout；不包含 stderr、交付／提交收据或
Runtime 隔离。未知退出状态保持 `null`；必填 `timed_out` 必须来自观测，不能从
缺失输出推断。恰好一个 final 事件结束记录，并与 `finalAnswer` 相同；断言必须
引用已有输出。最终 Wire 语义由 Cernora 校验。

## 导出和检查

脱敏记录、参考与生成输出应放在仓库外。先脱敏再记录 stdout 和断言；Adapter 对
实际提供的 UTF-8 字节计算 SHA-256。它不自动清洗任意自由文本，不认证生产者，
也不证明参考答案的权威。

```sh
/external/cernora-env/bin/python -B tools/cernora/chora_adapter.py \
  /external/completed-record.json /external/new-inspection.json
/external/cernora-env/bin/python -m cernora agent-run inspect \
  /external/new-inspection.json --reference /external/reference.json
```

Adapter 不读取参考；拒绝非普通文件／源符号链接，以及超过 16 MiB 的输入。它以
仅所有者可读写的权限原子发布，拒绝已有输出文件、目录和符号链接。无效输入不
生成输出，诊断不回显源值。Adapter 退出 0 表示导出成功，2 表示参数错误，3 表示
输入无效或发布失败。检查器退出码另见上表。Adapter 不执行事件中的 Agent 或命令，
也不访问网络服务。
