# Input Token 缓存命中模拟设计

## 1. 目标

模拟器需要在同一实例内记录已接受请求的 input token 序列。后续请求完成 tokenizer、chat template 和多模态处理后，使用实际 token id 与已有序列比较，计算最长公共前缀的命中 token 数，并将该数以目标协议的标准 usage 字段返回。

该能力用于验证网关的 prompt cache 路由、计量、计费、TTFT 优化、重试和故障恢复。它不模拟真实 GPU KV tensor，也不以缓存命中结论替代真实模型容量测试。

## 2. 当前基础和问题

项目已存在 `pkg/kvcache`：

- `KVCacheHelper.OnRequestStart` 使用 `TokenizedPrompt().Tokens` 生成 vLLM 风格 KV block key。
- 已驻留的完整 block 被计为 `cached_tokens`，并写入 `api.Usage.PromptTokensDetails`。
- 命中会影响 token-aware TTFT，且发布 ZMQ block 生命周期事件和 vLLM 前缀缓存指标。

这满足需要对接 KV event 消费者的物理 block 语义，但有三个限制：必须开启 KV cache、配置 `POD_IP` 和 render tokenizer；命中只能是完整 block 的整数倍；没有为 Anthropic Messages 和 OpenAI Responses 提供协议字段映射。

本设计新增轻量逻辑前缀缓存，不替换 KV block cache。二者由一个统一命中解析器选择其一，避免把同一个 input token 计两次。

## 3. 协议语义

### 3.1 OpenAI Chat Completions 和 Completions

保持 `prompt_tokens` 为完整输入 token 数，包含命中 token；`completion_tokens` 与现有生成逻辑一致；`total_tokens = prompt_tokens + completion_tokens`。命中数写入：

```json
{
  "usage": {
    "prompt_tokens": 1200,
    "completion_tokens": 80,
    "total_tokens": 1280,
    "prompt_tokens_details": {
      "cached_tokens": 1024
    }
  }
}
```

该结构与 OpenAI Chat Completions 的 `prompt_tokens_details.cached_tokens` 定义一致，即 prompt 中已经缓存的 token。[OpenAI Chat Completions API](https://platform.openai.com/docs/api-reference/chat/object?lang=ruby)

流式请求仅在 `stream_options.include_usage=true` 时发送最终 usage frame，规则与现有 OpenAI 流式实现一致。

### 3.2 OpenAI Responses

`/v1/responses` 使用 OpenAI Responses 风格字段：

```json
{
  "usage": {
    "input_tokens": 1200,
    "input_tokens_details": {
      "cached_tokens": 1024
    },
    "output_tokens": 80,
    "total_tokens": 1280
  }
}
```

`input_tokens` 保持完整输入，`cached_tokens` 只是其明细，不能从 `total_tokens` 中再次扣减。该字段与 OpenAI usage 中“retrieved from cache”的定义一致。[OpenAI Usage API](https://platform.openai.com/docs/api-reference/usage/audio_transcriptions_object)

### 3.3 Anthropic Messages

Anthropic 将普通输入、缓存读取和缓存创建分列计量。逻辑缓存开启后，设：

- `P` 为完整 prompt token 数。
- `H` 为最长公共前缀命中 token 数。
- `W` 为本请求新写入缓存的 token 数。
- `U = P - H - W` 为未命中且不写入缓存的 token 数。

返回：

```json
{
  "usage": {
    "input_tokens": 0,
    "cache_read_input_tokens": 1024,
    "cache_creation_input_tokens": 176,
    "output_tokens": 80
  }
}
```

首个可缓存请求通常为 `H=0, W=P, U=0`；相同后续请求为 `H=P, W=0, U=0`；共享前缀而后缀不同的请求为 `H=sharedPrefix, W=P-H, U=0`。若配置不缓存该请求，则 `H=0, W=0, U=P`。三项输入 token 的和恒等于 `P`。

非流式 response 的 `usage` 完整返回上述字段。流式 response 的 `message_start.message.usage` 返回输入、读取和创建 token；后续 `message_delta.usage` 继续只报告 `output_tokens`。Anthropic 的 usage 将 `cache_creation_input_tokens` 和 `cache_read_input_tokens` 分列，且总输入量由三项相加计算。[Anthropic prompt caching pricing](https://docs.anthropic.com/en/docs/about-claude/pricing)

### 3.4 vLLM gRPC 和 SGLang

vLLM gRPC 的 `GenerateStreamChunk.cached_tokens` 和 `GenerateComplete.cached_tokens` 使用同一 `H`。SGLang OpenAI 兼容路由使用 OpenAI 字段。当前 SGLang 原生 `/generate` 未承诺缓存 usage 扩展，不新增非标准字段；在固定目标 SGLang 版本 fixture 明确字段后单独实施。

## 4. 缓存键和命中算法

### 4.1 输入来源

只比较 `api.Request.TokenizedPrompt().Tokens`。不得比较原始 HTTP body、字符串、JSON 序列化结果或 prompt hash，因为它们会忽略 chat template、role、tool schema、LoRA、tokenizer 和多模态占位符的差异。

在 `baseRequestContext.tokenize()` 成功后、上下文窗口校验通过后执行 lookup。以下字段隔离缓存：

```text
engine + base_model + displayed_model + lora_name + lora_id + render_url
  + force_dummy_tokenizer
```

- `displayed_model` 与 LoRA 防止不同模型错误共享。
- 模型、render URL 和 dummy tokenizer 选项区分不同 tokenization 路径。若同一 render URL 后的 tokenizer/template 被热替换，调用清理接口。
- 含有 `MMFeatures` 的多模态请求旁路逻辑缓存，避免不同图像或音频使用相同文本 token 占位符时错误命中。
- cache epoch 在 sleep、配置变更和清理操作后递增；逻辑缓存指针在配置变更时替换。

请求 ID、用户、认证头、完整 prompt 和任意客户端 header 不进入 namespace 或 Prometheus 标签。

### 4.2 精确逻辑前缀模式

`logical-prefix` 使用按 namespace 划分的 token radix trie。每个节点保存 token id、子节点、最近访问时间、可缓存终点和子树 token 数。lookup 从根逐 token 前进，直到没有子节点，返回已匹配 token 长度 `H`。插入完整 prompt token 序列后，后续较长 prompt 可以命中已有短前缀，后续短 prompt 也可以命中已有长 prompt 的前缀。

查找和插入在缓存互斥锁中完成。`max_entries`、`max_total_tokens` 和 TTL 触发 LRU 驱逐；驱逐仅影响未来 lookup，绝不修改已经建立的 response usage。压测用例覆盖 100 并发 x 100K 和 1000 并发 x 10K 的活跃工作集；若实测锁竞争成为瓶颈，再按 namespace 分片。

### 4.3 KV block 模式

`kv-block` 保持当前 `KVCacheHelper` 行为。命中数为连续已存在 block 数乘以 `block-size`，因此 `H` 为 block-size 整数倍；该模式继续产生 ZMQ `BlockStored` 和 `BlockRemoved` 事件。

### 4.4 统一选择规则

配置 `source` 只能选择一个来源：

| `source` | 行为 |
| --- | --- |
| `disabled` | `H=0`，不读取或写入逻辑缓存。 |
| `logical-prefix` | 使用 radix trie，允许精确 token 命中。 |
| `kv-block` | 使用当前 KV block cache，要求 `enable-kvcache=true`。 |
| `auto` | KV cache 开启时使用 `kv-block`，否则关闭缓存。 |

不得将 logical 和 block 的命中数相加。`auto` 保持现有部署行为；需要 CPU-only 精确逻辑命中时显式配置 `source: logical-prefix`。

## 5. 生命周期和并发语义

### 5.1 可见性

请求完成 tokenization 和上下文窗口校验后立即将 token 序列写入缓存。后续请求可命中已被工作线程接受的 prefix。被 4xx/5xx 拒绝、在入队前取消或未通过 tokenization 的请求不会插入。已开始处理的请求即使客户端中途关闭，缓存仍保留到 TTL 或驱逐。

每个请求在处理时将命中数写入请求对象，再构造 response usage。Scenario 更新、TTL 驱逐或 cache clear 不会修改已构造的 response usage 和 TTFT。

### 5.2 处理顺序

```text
HTTP request
  -> parse and tokenize
  -> validate context and request controls
  -> resolve cached prefix H
  -> construct immutable request context and usage
  -> admit/queue request
  -> insert on acceptance
  -> simulate TTFT using P-H
  -> encode protocol-specific usage
```

现有 `KVCacheOnRequestStart`、`SetNumberOfCachedPromptTokens` 和 `simulateTTFT` 是接入点。`KVCacheOnRequestStart` 按 source 解析命中；response context 只读取请求上冻结的 cached token 数，不自行调用 cache。

## 6. 配置和测试控制

新增配置置于 `traffic-simulation`：

```yaml
traffic-simulation:
  prompt-cache:
    source: auto                 # disabled, logical-prefix, kv-block, auto
    min-prefix-tokens: 1
    max-entries: 10000
    max-total-tokens: 1048576
    ttl: 5m
```

默认 `source: auto`，保证升级后原有行为不变。`max-*`、`ttl`、枚举和 `min-prefix-tokens` 在配置校验时检查；运行时可通过 `/admin/config` 更新并自动清空逻辑缓存。改变 tokenizer 或 chat template 时应调用 clear 接口，不能混用旧 entry。

现有 `X-Mock-Cached-Tokens` 仅在 `enable-test-controls=true` 时有效，优先级最高；它覆盖 usage 和 TTFT 的 cached token 数，但不读取、写入或污染 prompt cache。`X-Mock-Prompt-Tokens` 没有真实 token 序列时同样旁路 prompt cache。这样固定 token 压测保持可重复。

### 6.1 容量规划

缓存容量取决于五分钟 TTL 内保留的不同前缀集合，不能只由并发数推导。对并发数 `C`、每个请求输入 `P`、平均请求生命周期 `T` 秒，五分钟内最多到达 `300 * C / T` 个请求。若每个请求均产生互不共享的、长度为 `H` 的可缓存前缀，TTL 工作集上界为：

```text
distinct_cached_tokens = H * 300 * C / T
```

80% 命中场景中 `H = 0.8 * P`。当命中来自一条所有请求共享的前缀时，工作集只有一条 80,000 token 或 8,000 token 前缀；当每个活跃会话有独立热点前缀时，应按活跃工作集规划。

| 负载 | 活跃输入 token | 80% 热前缀工作集 | block-size=16 的活跃 block 数 |
| --- | ---: | ---: | ---: |
| 100 并发，100,000 token | 10,000,000 | 8,000,000 | 625,000 |
| 1,000 并发，10,000 token | 10,000,000 | 8,000,000 | 625,000 |

对有会话复用且缓存集合不随五分钟累计增长的两类负载，`logical-prefix` 推荐从以下配置开始：

```yaml
traffic-simulation:
  prompt-cache:
    source: logical-prefix
    ttl: 5m
    max-total-tokens: 10000000 # 8,000,000 token 工作集加 25% 裕量
    max-entries: 10000
```

token ID 按 `uint32` 存储时，1,000 万 token 的原始数据约 38 MiB。radix 索引、namespace、LRU 元数据、Go map 和并发请求的 tokenization buffer 会明显增加占用，以上配置应为 simulator Pod 设置 `requests.memory: 1Gi`、`limits.memory: 2Gi`。此缓存为内存态，重启可重建，不需要持久卷；日志、指标和数据集若需保留，应与缓存容量分开配置。

`kv-block` 是协议和 ZMQ 生命周期模拟，不适合用作该工作负载的五分钟逻辑缓存。当前实现会为每个完整输入 block 建立记录，因此仅容纳两种负载的 1,000 万并发输入就需要至少 625,000 blocks；为避免瞬时容量错误应使用 800,000 blocks：

```yaml
kvcache:
  enable-kvcache: true
  kv-cache-size: 800000
  block-size: 16
```

该模式除了约 49 MiB 的 token 数组外，还维护 block map、in-flight request map，并分配 `10 * kv-cache-size` 个 `EventData` 的事件 channel。800,000 blocks 时该 channel 的 backing array 约为 610 MiB，因此应至少设置 `requests.memory: 2Gi`、`limits.memory: 4Gi`。如果不同前缀在整个五分钟都累积，必须先按上式计算 token 数；例如平均请求生命周期为 5 秒时，两种负载都会形成约 4.8 亿 cacheable token。对应的 3,000 万 blocks 会使当前事件 channel 单独占用约 23 GiB，不能通过单纯提高 `kv-cache-size` 支持。

因此，压测目标是向 OpenAI 或 Anthropic 客户端返回缓存命中数时，使用 `logical-prefix`；只有需要向 llm-d 路由器发布 vLLM KV block 生命周期事件时才启用 `kv-block`。需要同时验证两者时，逻辑缓存保存 usage 命中结果，KV block 缓存限定为较小的事件模拟容量，并由统一 resolver 防止重复计数。

## 7. 时延、指标和管理面

TTFT 的 token-aware 分支使用 `uncached = P-H`，即：

```text
prefill-overhead + (P-H) * prefill-time-per-token
```

固定 `time-to-first-token`、显式 `X-Mock-TTFT` 和远程 prefill 维持当前优先级。ITL、输出 token 和 dataset 内容不因 input cache 改变。

新增低基数指标：

```text
llmd_prompt_cache_requests_total{engine,profile,scenario,source,result}
llmd_prompt_cache_tokens_total{engine,profile,scenario,source,kind}
llmd_prompt_cache_entries{engine,profile,scenario,source}
llmd_prompt_cache_evictions_total{engine,profile,scenario,source,reason}
```

`result` 为 `hit`、`partial`、`miss`；`kind` 为 `queried`、`hit`、`written`；`reason` 为 `ttl`、`capacity`、`clear`、`epoch`。不使用 model 完整路径、namespace hash、request ID 或用户作为标签。现有 `vllm:prefix_cache_hits_total` 和 `vllm:prefix_cache_queries_total` 在 `kv-block` 模式继续保持原有含义；逻辑模式不伪造 KV block 指标。

管理面增加只读统计与清理：

```text
GET  /admin/prompt-cache/stats
POST /admin/prompt-cache/clear
```

stats 只返回 namespace 数、entry 数、token 数、命中率和 epoch，不返回 token、prompt、hash 或用户数据。clear 默认清除当前 Profile 的所有逻辑 namespace 并递增 epoch；可以按明确 model alias 限定，不能接受任意正则或 prompt 作为选择器。

## 8. 测试方案

### 8.1 单元测试

| ID | 场景 | 断言 |
| --- | --- | --- |
| PC-UNIT-01 | 相同 token 两次请求 | 第一次 `H=0`，第二次 `H=P`。 |
| PC-UNIT-02 | 共享前缀后缀不同 | 返回最长公共前缀，不匹配位置之后为 0。 |
| PC-UNIT-03 | 模型、LoRA、tokenizer、模板或多模态变化 | 不跨 namespace 命中。 |
| PC-UNIT-04 | `min-prefix-tokens` | 小于阈值报告 0 命中，请求仍记录以供后续命中。 |
| PC-UNIT-05 | TTL、容量、clear、epoch | 被驱逐后为 miss；在途 `CacheResolution` 不变。 |
| PC-UNIT-06 | 并发 lookup/insert | `go test -race` 无数据竞争；计数和 trie 不损坏。 |
| PC-UNIT-07 | block 模式 | 只返回完整 block 数；不与 logical 数相加。 |
| PC-UNIT-08 | test controls | `X-Mock-Cached-Tokens` 覆盖返回值但不改变 trie。 |

### 8.2 协议和端到端测试

| ID | 场景 | 断言 |
| --- | --- | --- |
| PC-E2E-01 | OpenAI chat 非流式，重复 P01 | 第二次 `prompt_tokens_details.cached_tokens=P`，usage 总数不变。 |
| PC-E2E-02 | OpenAI completion 流式 | 最终 include_usage frame 有正确 cached token。 |
| PC-E2E-03 | OpenAI Responses | `input_tokens_details.cached_tokens` 正确。 |
| PC-E2E-04 | Anthropic Messages 非流式 | `input_tokens + cache_read_input_tokens + cache_creation_input_tokens=P`。 |
| PC-E2E-05 | Anthropic Messages 流式 | message_start 包含 input/cache 字段，message_delta 仅改变 output token。 |
| PC-E2E-06 | vLLM gRPC | chunk 和 complete 的 `cached_tokens` 与 response context 一致。 |
| PC-E2E-07 | SGLang OpenAI | 复用 OpenAI usage；原生不出现未承诺字段。 |
| PC-E2E-08 | Profile/Scenario 更新和 clear 并发 | 新请求使用新 epoch，在途请求 usage 不变。 |
| PC-E2E-09 | 断流、取消、500 | 按 visibility 和 cancel 配置验证是否保留写入；无 worker 或连接泄漏。 |

### 8.3 性能和验收

1. 使用 4K 共享 system prompt 加不同 user suffix 的 open-loop 测试，比较 disabled、logical-prefix、kv-block 三种模式的 TTFT、RPS 和 hit token。
2. 使用 2,500 条流式连接检查 cache mutex 不成为吞吐瓶颈；记录 p95/p99 lookup 时间、CPU、RSS、goroutine 和 eviction rate。
3. 分别执行 30 分钟预检、8 小时 nightly 和 24 小时发布前 soak，验证 trie token 数不超过上限、内存和 FD 不单调增长。
4. 对每次运行保存 Profile、Scenario、source、visibility、seed、tokenizer、cache 配额、镜像 digest 和原始 Prometheus 抓取。

完成标准是：相同 namespace 的命中数可重复；协议 usage 公式始终成立；不同 namespace 不错误共享；逻辑缓存与 KV block 缓存不重复计数；缓存命中只影响输入 prefill/TTFT，不改变输出语义。

## 9. 实施顺序

1. 在 `pkg/endpoint` 定义不可变 `CacheResolution`，先补 unit tests。
2. 在 `pkg/kvcache` 新增按 namespace 分片的 `PromptPrefixCache`，实现 TTL 和容量驱逐。
3. 将 `baseRequestContext.HandleRequest` 的现有 KV lookup 改为统一 resolver，保留 `kv-block` compatibility path。
4. 扩展 `api.Usage`、Responses usage 和 Messages usage builders，补充流式 frame 测试。
5. 增加 metrics、管理 stats/clear、admin update 和 e2e。
6. 用真实 vLLM、SGLang、Anthropic-compatible gateway fixture 校验字段和事件顺序；没有 fixture 的原生 SGLang 字段不实现。

每一步采用 TDD：先写失败的同包测试和真实 HTTP/gRPC e2e，再实现最小通过改动；每个可回滚阶段运行相关包测试和 `make presubmit`。
