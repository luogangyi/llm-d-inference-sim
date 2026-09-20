# 流量模拟任务跟踪

依据：[流量模拟设计方案](llm-d-inference-sim-traffic-simulation-design.md) 和 [实施方案](llm-d-inference-sim-traffic-simulation-implementation-plan.md)。

每个里程碑必须先补齐单元测试和端到端测试；端到端测试使用真实 HTTP 客户端发起 LLM prompt 请求。完成时记录提交、验证命令和结果。

| 里程碑 | 范围 | 状态 | 提交 | 验证 |
| --- | --- | --- | --- | --- |
| M0 | 协议基线与交付边界 | 未开始 | - | fixture 契约测试 |
| M1 | OpenAI Profile 与基线 smoke | 已完成 | `ddd880b` | `make test`、`make presubmit` |
| M2 | Token、时延和 Scenario 控制 | 已完成 | 本提交 | 单元包通过；7 个 HTTP e2e 规格通过；`make presubmit` 通过 |
| M3 | SSE 流故障 | 已完成 | 本提交 | 流故障单元测试通过；5 个 HTTP e2e 规格通过；`make presubmit` 通过 |
| M4 | SGLang 原生 engine | 已完成 | 本提交 | SGLang 请求和 SSE 单元测试通过；3 个 HTTP e2e 规格通过；`make presubmit` 通过 |
| M5 | 指标、运行手册和压测复现 | 进行中 | 本提交 | 流故障指标单元测试通过；6 个 HTTP e2e 规格通过；`make presubmit` 通过 |

## M2 验收项

1. `traffic-simulation` 配置支持 YAML、复制、校验、`/admin/config` 原子更新和清理后的管理响应。
2. 仅在 `enable-test-controls` 开启时解析 `X-Mock-Prompt-Tokens`、`X-Mock-Cached-Tokens`、`X-Mock-Output-Tokens`、`X-Mock-TTFT` 和 `X-Mock-ITL`。
3. 非法控制值返回 HTTP 400；逻辑 prompt、cached 和 output Token 在流式与非流式 usage 中一致。
4. 两个带不同 TTFT/ITL 的并发请求互不影响；管理接口更新只影响新请求。

## 变更记录

| 日期 | 记录 |
| --- | --- |
| 2026-09-20 | 采用本文件管理后续里程碑，不再以 GitHub Issue 作为任务跟踪载体。 |
| 2026-09-20 | M2 实现请求级 Token 与时延控制、场景原子更新和 Profile/Scenario 指标；单元测试、HTTP e2e 和预检通过。 |
| 2026-09-20 | M3 实现 SSE 断流、停顿、usage 损坏、usage/DONE 省略及运行时场景更新；单元测试、HTTP e2e 和预检通过。 |
| 2026-09-20 | M4 注册 SGLang engine，提供原生 `/generate`、模型查询和健康路由；原生流式响应使用累计文本，并保留 OpenAI 兼容路由。 |
| 2026-09-20 | M5 开始实现流故障 Prometheus 计数器；标签使用请求开始时的 Profile 和 Scenario 快照。 |
