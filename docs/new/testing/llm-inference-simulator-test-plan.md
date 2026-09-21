# LLM 推理模拟器完整测试方案

## 1. 目标和边界

本方案验证 `llm-d-inference-sim` 作为 vLLM、vLLM-Ascend 和 SGLang 流量模拟后端时，能够为 Token 网关、客户端 SDK 和压测器提供可重复的协议、时延、并发和故障行为。

测试结论分为两类：

| 结论 | 含义 |
| --- | --- |
| 协议兼容 | 请求和响应与固定的目标上游版本契约一致。 |
| 模拟行为正确 | Token、队列、时延、SSE 和故障满足配置或请求级控制。 |

本项目不验证模型回答质量、GPU/NPU 算子性能、真实显存占用或真实引擎的最终容量。vLLM-Ascend 在本项目中复用 OpenAI 兼容协议，以独立 Profile 表达性能画像；它不是 Ascend 运行时的替代测试。

每次正式执行必须记录目标上游版本、镜像 digest、Profile、Scenario、seed、测试器版本、CPU/内存、容器运行时版本和完整命令。未固定版本的上游行为不得作为回归基线。

## 2. 被测能力矩阵

| 能力 | vLLM | vLLM-Ascend Profile | SGLang OpenAI Profile | SGLang 原生 engine |
| --- | --- | --- | --- | --- |
| `/v1/chat/completions`、`/v1/completions` | 必测 | 必测 | 必测 | 必测 |
| SSE、usage、TTFT、ITL、队列 | 必测 | 必测 | 必测 | 必测 |
| `/v1/models`、`/health`、`/health/ready`、`/metrics` | 必测 | 必测 | 必测 | 必测 |
| `/v1/responses`、`/v1/messages`、`/v1/embeddings` | 必测 | 复用公共实现 | 复用公共实现 | 复用公共实现 |
| `/tokenize`、render、derender | 必测 | 复用公共实现 | 复用公共实现 | 复用公共实现 |
| `/inference/v1/generate` | 必测 | 必测 | 不承诺 | 不承诺 |
| LoRA、`/query`、sleep/wake、vLLM gRPC | 必测 | 必测 | 不承诺 | 不承诺 |
| `POST /generate` | 不承诺 | 不承诺 | 不承诺 | 必测 |
| `/model_info`、`/get_model_info`、`/server_info`、`/health_generate` | 不承诺 | 不承诺 | 不承诺 | 必测 |
| `/detokenize`、`/v1/rerank`、`/v1/score` | 以当前公共路由为准 | 以当前公共路由为准 | 以当前公共路由为准 | 未承诺，单独采集契约后再纳入 |

## 3. 测试分层和门禁

| 层级 | 位置或工具 | 触发 | 通过条件 |
| --- | --- | --- | --- |
| unit | 同包 Go 测试 | 每次代码改动 | 边界、非法输入、确定性和无数据竞争。 |
| package integration | `pkg/endpoint`、`pkg/simulator`、`pkg/communication` | 每次代码改动 | 队列、时延、KV Cache、SSE 和配置更新行为正确。 |
| HTTP/gRPC e2e | `pkg/tests` 的真实 HTTP 客户端和 gRPC 客户端 | 每个提交 | 客户端可观察到的状态、头、JSON、SSE 顺序和 EOF 正确。 |
| fixture regression | 版本化脱敏 fixture | 每日或上游版本更新 | 规范化后字段和事件顺序符合目标上游。 |
| performance | 专用压测器、SGLang `bench_serving` | nightly 或发布前 | 吞吐、连接、时延和资源指标满足已记录目标。 |
| soak | 独立环境 | nightly 或发布前 | 在固定时长内无持续的内存、FD、goroutine、连接或错误增长。 |

提交门禁为相关包测试、相关 e2e 和 `make presubmit`。性能与 soak 不作为普通提交阻塞项，但必须保留原始结果和元数据；发布前必须执行。

## 4. 测试环境和数据

### 4.1 环境

| 环境 | 用途 | 要求 |
| --- | --- | --- |
| 开发机 | unit、package integration、静态检查 | Go、make、Docker 或 Podman。 |
| 测试机 | HTTP/gRPC e2e、直压、短时并发 | `root@139.196.28.96:32025`，代码目录 `/var/code/llm-d-inference-sim`。 |
| 真实上游采集环境 | fixture 和时延校准 | 独立 vLLM、vLLM-Ascend、SGLang 实例，不使用生产用户数据。 |
| 发布前压力环境 | 8 小时和 24 小时 soak | 固定副本数和配额，记录网关是否在链路中。 |

测试机使用国内镜像代理或镜像缓存；记录代理地址和镜像 digest。测试数据不得包含 API key、用户提示词、用户媒体或生产响应。

### 4.2 Prompt 语料

每种引擎和每个兼容接口都使用以下脱敏 prompt 类型。每条请求有固定 `X-Request-Id`，随机生成模式使用固定 seed。

| 编号 | Prompt 类型 | 示例目的 |
| --- | --- | --- |
| P01 | 简短纯文本对话 | 基础 chat 和 completion。 |
| P02 | 多轮 system、developer、user 对话 | 角色和 chat template。 |
| P03 | 中文、英文、代码和 emoji 混合 | UTF-8 与 tokenizer 边界。 |
| P04 | 结构化 JSON 输出提示 | 内容和流式 chunk 拼接。 |
| P05 | Tool 定义和 tool choice | tool call、finish reason、参数增量。 |
| P06 | Reasoning 提示 | reasoning 字段或明确的未支持行为。 |
| P07 | 图片、音频、视频内容块 | 多模态 render 和 feature 元数据。 |
| P08 | embedding 的字符串、字符串数组、token id | 向量数量、维度、编码格式和 usage。 |
| P09 | 4K、16K、32K、128K 逻辑上下文 | 上下文边界、队列和 TTFT。 |
| P10 | 128、1K、4K 逻辑输出 | 长连接、ITL、usage 和资源回落。 |
| P11 | 无效 JSON、缺少 model、未知 model、非法 token | 400、404 和错误结构。 |
| P12 | 客户端中途取消或慢读 | drain、并发名额和资源释放。 |

逻辑长文本可用 `X-Mock-Prompt-Tokens` 和 `X-Mock-Output-Tokens` 构造，必须在 `enable-test-controls: true` 的测试 Profile 中执行。精确 tokenizer 对账使用 render 服务和真实模型名，不与 Dummy tokenizer 容量测试混用。

## 5. 功能测试用例

### 5.1 公共服务、模型和管理面

| ID | 用例 | 请求与步骤 | 预期结果 | 覆盖引擎 |
| --- | --- | --- | --- | --- |
| COM-001 | 存活检查 | `GET /health` | 200，JSON content type。 | 全部 |
| COM-002 | 就绪检查 | 启动期间和启动完成后请求 `/health/ready` | 启动时按 `startup-duration` 返回 503，完成后 200。 | 全部 |
| COM-003 | 模型发现 | `GET /v1/models` | 服务模型别名、`max_model_len` 和模型元数据正确。 | 全部 |
| COM-004 | 未知模型 | 向 chat、completion、embedding 发送未知 model | 404，统一错误 JSON，不产生成功 Token 指标。 | 全部 |
| COM-005 | 管理配置读取 | `GET /admin/config` | 返回清理后的有效配置和当前 Scenario。 | 全部 |
| COM-006 | 管理配置原子更新 | 并发流式请求期间 `POST /admin/config` 更新 Scenario、TTFT、ITL | 错误更新不改变配置；成功更新只影响之后到达的请求。 | 全部 |
| COM-007 | 指标身份 | 启动不同 Profile 后读取 `/metrics` | `llmd_simulation_info{engine,profile,scenario}` 为 1，旧 Scenario 更新后为 0。 | 全部 |
| COM-008 | 指标标签基数 | 使用不同 request ID、用户和任意请求头请求 | 新增指标不以这些动态值作为标签。 | 全部 |
| COM-009 | TLS 与请求 ID | 启用 TLS、`enable-request-id-headers` 后请求 | HTTPS 可用，响应关联正确请求 ID。 | 全部 |

### 5.2 OpenAI chat/completion

| ID | 用例 | 请求与步骤 | 预期结果 | 覆盖引擎 |
| --- | --- | --- | --- | --- |
| OAI-001 | 非流式 chat | P01，`stream=false` | 200；choice、finish reason、model、usage 完整；`total_tokens=prompt+completion`。 | 全部 |
| OAI-002 | 流式 chat | P02，`stream=true`，`include_usage=true` | SSE content type；role 后增量内容；finish frame、唯一 usage frame、`[DONE]` 顺序正确。 | 全部 |
| OAI-003 | 非流式 completion | P01 的 `prompt` 字符串 | 200；`choices[].text`、usage 和 logprobs 行为符合请求。 | 全部 |
| OAI-004 | 多 prompt completion | string 数组、token id、token id 数组 | 每个 prompt 对应 choice；usage 和索引不串扰。 | 全部 |
| OAI-005 | 多 choice | `n=2` 流式和非流式 | choice 索引完整；共享 prompt 只计一次 prompt token。 | 全部 |
| OAI-006 | max token 边界 | 0、1、正常值、超过上下文、`ignore_eos` | 合法请求按限制结束；非法值为 400；不会报告超过限制的 usage。 | 全部 |
| OAI-007 | cached token | P09 和缓存控制 | `prompt_tokens_details.cached_tokens` 不超过 prompt token；TTFT 与缓存语义一致。 | 全部 |
| OAI-008 | logprobs | P03，`logprobs`、`top_logprobs` | token、logprob、bytes 和候选数组结构正确。 | 全部 |
| OAI-009 | tool call | P05，流式和非流式 | function 名称、参数、call id、finish reason 与 chunk 顺序正确。 | 全部 |
| OAI-010 | reasoning | P06 | 已实现字段按契约返回；未支持字段不伪造且客户端行为明确。 | 全部 |
| OAI-011 | 多模态 chat | P07 | 有效内容块被解析；非法 URL/base64 产生稳定错误；render 特征与请求一致。 | 全部 |
| OAI-012 | 协议错误 | P11 | 无效 body、缺失 messages/prompt、错误类型字段得到 400，不会 panic。 | 全部 |

### 5.3 其他公共 API

| ID | 用例 | 请求与步骤 | 预期结果 | 覆盖引擎 |
| --- | --- | --- | --- | --- |
| API-001 | Responses 非流式 | P01、P05、P07 | item、output text/tool call、usage 和状态符合响应契约。 | 全部 |
| API-002 | Responses 流式 | P01、P05 | 事件名、data JSON、item 状态和完成顺序正确。 | 全部 |
| API-003 | Anthropic messages | P02、P05 | `content`、`stop_reason`、usage 与 SSE 事件正确。 | 全部 |
| API-004 | Embedding float | P08，单输入和批输入 | 返回向量数等于输入数，索引连续、维度正确、usage 正确。 | 全部 |
| API-005 | Embedding base64 | P08，`encoding_format=base64` | 每条向量是可解码 base64，维度与 float 模式一致。 | 全部 |
| API-006 | Tokenize | 文本、空文本、token id 和非法请求 | token id 可重复；非法输入稳定返回 400。 | 全部 |
| API-007 | Render | P03、P07 | `/render` token id 与 tokenizer 模式一致；多模态 feature 的 key、offset、length 合法。 | 全部 |
| API-008 | Derender | 由 render/generate 构造合法 token response | 重建 completion/chat，request id、choice 索引和 KV 参数透传正确。 | 全部 |
| API-009 | Derender 错误 | 空 choices、空 token ids、stream=true、数组长度不匹配 | 400，不启动 worker。 | 全部 |

### 5.4 vLLM 和 vLLM-Ascend 专项

| ID | 用例 | 请求与步骤 | 预期结果 | 覆盖引擎 |
| --- | --- | --- | --- | --- |
| VLLM-001 | 原生 generate | `POST /inference/v1/generate`，文本、token id、流式 | 状态、token、finish reason、SSE 与固定 fixture 一致。 | vLLM、Ascend |
| VLLM-002 | LoRA 生命周期 | load、模型查询、以 LoRA 请求、unload、再次查询 | 模型集合和 LoRA 指标变化正确；卸载后请求返回预期错误。 | vLLM、Ascend |
| VLLM-003 | KV 传输 | 带 remote prefill/decode 参数请求 | 透传字段、缓存事件和响应参数一致。 | vLLM、Ascend |
| VLLM-004 | Mooncake query | `GET /query` | 稳定的 rank 到 engine id 映射。 | vLLM、Ascend |
| VLLM-005 | sleep/wake | sleep、is_sleeping、wake_up、正常请求 | 状态转换可观察；休眠行为符合当前实现。 | vLLM、Ascend |
| VLLM-006 | gRPC Generate/GetModelInfo | 正常、流式、取消、未知模型 | 服务注册、状态码、metadata、流结束和取消正确。 | vLLM、Ascend |
| ASC-001 | Ascend Profile | 加载 `vllm-ascend-*` Profile 并执行 OAI-001 到 OAI-008 | 引擎身份、模型别名、队列和时延参数来自 Ascend Profile。 | Ascend |
| ASC-002 | 画像隔离 | 与 vLLM Profile 并行发相同固定 token 请求 | usage 一致；TTFT/ITL 和过载退化仅按各自 Profile 变化。 | Ascend |

### 5.5 SGLang OpenAI 和原生专项

| ID | 用例 | 请求与步骤 | 预期结果 | 覆盖引擎 |
| --- | --- | --- | --- | --- |
| SGL-001 | OpenAI 兼容面 | OAI-001 到 OAI-012 | 路由、usage、SSE 和指标复用公共实现。 | SGLang OpenAI、原生 |
| SGL-002 | 原生 generate 非流式 | `POST /generate`，P01，`sampling_params.max_new_tokens` | `text`、`meta_info.prompt_tokens`、`completion_tokens`、finish reason 符合 fixture。 | SGLang 原生 |
| SGL-003 | 原生 generate 流式 | `stream=true`，P01、P04 | 每帧 `text` 是累计文本或目标 fixture 定义的模式；末尾顺序可解析。 | SGLang 原生 |
| SGL-004 | 原生参数与错误 | 缺少 `text`、错误 JSON、0/负 max token、未知字段 | 稳定 400 或记录的兼容行为；不泄漏内部错误。 | SGLang 原生 |
| SGL-005 | 原生查询接口 | `/model_info`、`/get_model_info`、`/server_info` | 两个 model info 路由一致；模型、tokenizer、最大上下文、profile 只返回已实现信息。 | SGLang 原生 |
| SGL-006 | health_generate | 队列空闲、满载、注入失败时请求 | 健康语义、延迟和失败符合固定契约。 | SGLang 原生 |
| SGL-007 | 不承诺接口 | `/detokenize`、`/v1/rerank`、`/v1/score` | 在未实现前明确返回 404；采集 fixture 后再转为必测。 | SGLang 原生 |

### 5.6 Token、时延和配置控制

| ID | 用例 | 请求与步骤 | 预期结果 | 覆盖引擎 |
| --- | --- | --- | --- | --- |
| SIM-001 | 控制开关 | 关闭和开启 `enable-test-controls` 后发送所有 `X-Mock-*` | 关闭时无副作用；开启时按头生效。 | 全部 |
| SIM-002 | 固定逻辑 token | `prompt=4096,cached=2048,output=1024`，流式与非流式 | usage、上下文校验、KV 指标和时延使用同一逻辑值。 | 全部 |
| SIM-003 | TTFT/ITL 覆盖 | 两个并发请求使用不同 `X-Mock-TTFT`、`X-Mock-ITL` | 首 token 与相邻 token 时间落在容差内，两个请求无串扰。 | 全部 |
| SIM-004 | 边界和非法头 | 负数、非数字、缓存超过 prompt、输出超过窗口、非法 duration | 400，错误指出具体头和值。 | 全部 |
| SIM-005 | Scenario 优先级 | Profile、`/admin/config` Scenario、请求头同时设置 | 请求头高于 Scenario，Scenario 高于 Profile；运行中请求保持快照。 | 全部 |
| SIM-006 | 重放 | 相同 seed、request ID、Profile、Scenario 重复 N 次 | 状态、usage、chunk 边界和故障位置一致。 | 全部 |

## 6. 性能和并发测试用例

### 6.1 通用规则

测试分为直连模拟器和经 Token 网关两组。先取得零时延直连基线，再测网关；只有模拟器吞吐至少达到目标网关吞吐三倍，才能用后者得出网关容量结论。所有性能用例记录 RPS、成功率、TTFB、首内容 Token 时间、TTFT、ITL、E2E、usage 对账、活跃连接、goroutine、FD、CPU、RSS 和 `/metrics`。

延迟判定使用配置值加测量容差。零时延场景使用 p50/p95/p99 和吞吐；有时延场景使用请求级预期 TTFT/ITL 与实际差值。不要把单次随机结果作为容量结论。

### 6.2 负载矩阵

| ID | 场景 | 请求形态 | 并发或到达率 | 通过条件 |
| --- | --- | --- | --- | --- |
| PERF-001 | 零时延小请求 | P01，chat/completion 各半，流式和非流式各半 | 1、10、50、100、500 阶梯 | 吞吐随并发上升到平台；无 5xx、无连接泄漏。 |
| PERF-002 | 正常对话 | P02，1K 输入/1K 输出，流式 | 10、50、100、200 | p95 TTFT/ITL 在 Profile 值和容差内；usage 零误差。 |
| PERF-003 | 长上下文 | P09，4K/1K、16K/2K、32K/1K、128K/128 | 10、50、100 | 无超出上下文的误接受；资源在请求结束后回落。 |
| PERF-004 | 长输出 | P10，200/4K，流式 | 10、50、100 | chunk 顺序完整、ITL 稳定、客户端无读超时。 |
| PERF-005 | 混合业务 | chat、completion、embedding、responses、messages 按业务比例 | open-loop 10 到目标 RPS | 到达率不因服务变慢而降低；按接口统计成功率和时延。 |
| PERF-006 | 突发 | 1 秒内从 0 到 `max-num-seqs + queue` | 3 次重复 | 运行、等待、拒绝数量符合队列上限；恢复后指标回落。 |
| PERF-007 | 2,500 流连接 | P01，零时延和正常 ITL 两轮 | 2,500 持续流式连接 | 连接建立率、错误率、FD、goroutine 和 RSS 都有记录；无持续泄漏。 |
| PERF-008 | Profile 对比 | 同一 P02 和固定 Token 对 vLLM、Ascend、SGLang Profile | 每个 100 并发 | usage 相同；时延和过载曲线反映 Profile 差异。 |
| PERF-009 | SGLang 原生并发 | `/generate`，P01、P04，流式和非流式 | 1、10、100、500 | 累计文本可重建，SSE 不交叉，OpenAI 路由同时可用。 |
| PERF-010 | 网关开销 | PERF-001 至 PERF-005 直连和经网关各一次 | 相同负载 | 报告两者差值，不把模拟器瓶颈归因给网关。 |

### 6.3 稳定性测试

| ID | 时长 | 负载 | 观测和通过条件 |
| --- | --- | --- | --- |
| SOAK-001 | 30 分钟预检 | PERF-005 的 30% 目标负载 | 无单调增长的 RSS、goroutine、FD 和连接数；错误率不持续升高。 |
| SOAK-002 | 8 小时 nightly | PERF-005，定期插入 PERF-006 | 每 5 分钟保存指标快照和客户端结果；无未恢复队列积压。 |
| SOAK-003 | 24 小时发布前 | 混合业务加慢客户端、取消和故障 | 与 SOAK-002 相同，另保存容器重启、磁盘、网络和进程日志。 |

## 7. 故障和恢复测试用例

| ID | 故障 | 注入方式 | 预期结果 |
| --- | --- | --- | --- |
| ERR-001 | 400 非法请求 | P11、非法 `X-Mock-*` | OpenAI 结构错误；不进入 worker；无成功 token 计数。 |
| ERR-002 | 401 鉴权 | 现有 failure injection 或网关鉴权 | 状态和错误类型符合契约；无重试风暴。 |
| ERR-003 | 404 模型不存在 | 未知 model 或未承诺原生路由 | 404；模型和 LoRA 状态不变。 |
| ERR-004 | 429 队列过载 | 限制 `max-num-seqs` 和等待队列后突发 | 拒绝量可解释；在负载下降后恢复。 |
| ERR-005 | 500/503 | failure injection、sleep/startup 状态 | 错误结构正确；后续正常请求不受污染。 |
| ERR-006 | 首 token 超时 | 大 TTFT 或网关 timeout | 客户端超时后 worker、队列名额和 goroutine 回落。 |
| ERR-007 | 中途断流 | `X-Mock-Disconnect-After-Chunks` 和 Scenario | 在指定 flush 后 EOF；不发送 error、usage、`[DONE]`；记录 `disconnect`。 |
| ERR-008 | 慢解码 | stall Scenario 或请求头 | 停顿位置和时长在容差内；慢客户端取消可结束。 |
| ERR-009 | 缺失 usage | `X-Mock-Omit-Usage` | 保留正文、finish 和 `[DONE]`；计数 `omit_usage`。 |
| ERR-010 | 损坏 usage | `X-Mock-Corrupt-Usage` | 仅 usage 确定性异常；正文、finish 和正常 token 指标不变。 |
| ERR-011 | 缺失 DONE | `X-Mock-Omit-Done` | 保留正文和 usage；计数 `omit_done`。 |
| ERR-012 | 客户端提前关闭 | P12，在首 chunk、半程、usage 前关闭 body | drain 不阻塞 producer；并发名额、FD、goroutine 回落。 |
| ERR-013 | 管理更新竞争 | 并发 `POST /admin/config` 和流量 | 配置 copy-validate-swap 原子；请求仅使用一个 Scenario 快照。 |
| ERR-014 | 容器或进程重启 | 压测期间滚动重启一个副本 | 已断开请求可分类；健康检查恢复后新请求成功。 |
| ERR-015 | 依赖不可用 | render URL、dataset URL、ZMQ 不可达 | 启动或请求给出明确错误；不死锁、不无限重试。 |
| ERR-016 | SGLang 原生错误映射 | `/generate` 无效 body、错误 max token、服务过载 | 状态、错误 body 和连接关闭行为符合固定 fixture。 |

每个流故障用例必须检查 SSE 原始字节、帧数量、usage 和 `[DONE]` 是否出现、客户端错误类型、服务端 stream-fault counter、等待和运行请求指标，以及稳定窗口后的资源回落。

## 8. 版本化 fixture 和上游对照

每个目标引擎版本至少采集以下 fixture：普通 chat、普通 completion、流式 chat、流式 completion、tool call、reasoning、错误、usage、模型查询；SGLang 另采集 `/generate`、`model_info`、`server_info`。fixture 目录按 `engine/version/route/case` 组织，并保存采集命令、镜像 digest、模型、日期和字段规范化规则。

比较时忽略随机 id、时间戳、部署地址和动态端口；必须比较 HTTP 状态、content type、JSON 字段类型、SSE event/data 顺序、finish reason、usage、错误结构和连接终止位置。上游新增或变化字段先更新 fixture 和兼容性决策，再改变模拟器。

## 9. 指标和结果验收

每个性能和故障任务保存以下最小结果：

```json
{
  "run_id": "timestamp-profile-scenario",
  "git_commit": "<commit>",
  "engine": "vllm|sglang",
  "profile": "<profile>",
  "scenario": "<scenario>",
  "seed": 20260918,
  "image_digest": "<digest>",
  "driver": "<name and version>",
  "command": "<exact command>",
  "concurrency": 100,
  "request_rate": 0,
  "duration_seconds": 600,
  "host": "<cpu, memory, os, runtime>",
  "result_files": ["summary.json", "metrics.prom", "client.jsonl"]
}
```

必须采集：`llmd_simulation_info`、`llmd_simulation_stream_faults_total`、运行和等待请求、TTFT、ITL、E2E、请求成功、prompt/generation token、KV cache 使用率、进程 CPU、RSS、goroutine、FD 和客户端侧的 TTFB/首内容 token/E2E。仪表盘或 recording rule 至少提供总请求率、错误率、p50/p95/p99 TTFT/ITL/E2E、排队时间、活跃流连接、故障类型速率和资源曲线。

## 10. 执行清单

1. 选择 Profile 和 Scenario，先运行 `GET /health`、`GET /health/ready`、`GET /v1/models` 和 `/metrics` 基线。
2. 执行对应引擎的功能 e2e；流式用例保存原始 SSE。
3. 在零时延 Profile 运行 PERF-001，再运行正常时延的 PERF-002 至 PERF-005。
4. 执行 ERR-001 至 ERR-016，并在每次故障后检查资源回落。
5. 对需要精确 token 的用例接入 render 服务，完成 tokenizer 对账。
6. 执行 SOAK-001；通过后安排 SOAK-002 和发布前 SOAK-003。
7. 存档元数据、客户端结果、Prometheus 抓取、服务日志和失败复现命令。

当前仓库中可作为普通回归入口的命令包括：

```bash
make presubmit
go test ./pkg/communication ./pkg/endpoint ./pkg/simulator
go test ./pkg/tests -ginkgo.focus='traffic simulation|SGLang native' -count=1 -v
```

在测试机执行时使用已安装的 Go、Docker 镜像代理和测试环境变量；性能及 soak 命令必须在运行记录中完整保存，不能只保存终端摘要。

### 10.1 可执行脚本

`scripts/testing/run-traffic-simulation.sh` 启动一个 Profile，并调用不依赖第三方包的 Python HTTP 客户端。脚本把服务日志、Profile、指标、提交号、参数和 JSON 结果写入 `artifacts/traffic-simulation/`。

```bash
scripts/testing/run-traffic-simulation.sh \
  --profile examples/traffic-simulation/profiles/vllm-zero-delay.yaml \
  --suite functional

scripts/testing/run-traffic-simulation.sh \
  --profile examples/traffic-simulation/profiles/sglang-native-normal-chat.yaml \
  --suite concurrency --concurrency 100 --requests-per-worker 10

scripts/testing/run-traffic-simulation.sh \
  --profile examples/traffic-simulation/profiles/vllm-zero-delay.yaml \
  --suite faults
```

`functional` 验证健康、模型、指标、非流式 chat、流式 completion、embedding 和原生 SGLang 路由；`concurrency` 使用固定数量的并发 worker 输出请求数、RPS、p50 和 p95；`faults` 通过管理接口开启测试控制，验证断流、缺失 DONE 及其指标。更高并发、open-loop 和 soak 按本方案第 6 节在独立环境执行。
