# Input Token 前缀缓存模拟操作手册

## 用途与开关

模拟器将通过 tokenizer 得到的 input token ID 记录在内存中。后续请求在同一模型和 tokenizer 路径下命中相同 token 前缀时，返回命中 token 数，并用于按 token 计算的 prefill/TTFT。输出内容仍由原有 dataset 和生成逻辑决定。

在配置文件中设置：

```yaml
model: testmodel
max-model-len: 131072
max-num-seqs: 100
traffic-simulation:
  prompt-cache:
    source: logical-prefix
    min-prefix-tokens: 1
    max-entries: 10000
    max-total-tokens: 10000000
    ttl: 5m
```

`source` 的可选值：

| 值 | 行为 |
| --- | --- |
| `logical-prefix` | 开启精确 token 前缀缓存；无需 GPU、KV tensor 或 ZMQ。 |
| `disabled` | 关闭命中查找和写入，所有真实请求返回 0 命中。 |
| `kv-block` | 使用现有 vLLM KV block 缓存，需要 `kvcache.enable-kvcache: true`；只计算完整 block 命中，并发布 ZMQ 事件。 |
| `auto` | 默认值；启用现有 KV cache 时使用 `kv-block`，否则关闭缓存。 |

`max-total-tokens` 是驻留 trie 中不同 token 边的上限，`max-entries` 是完整 prompt 终点数上限。超出容量时按 LRU 驱逐，TTL 从插入时开始计时。大于 `max-total-tokens` 的单条 prompt 可读取已有前缀，但不会被写入。`min-prefix-tokens` 只限制上报命中数；低于阈值的请求仍会写入。

配置文件修改后重启服务。也可在线切换：

```bash
curl -sS -X POST http://127.0.0.1:8000/admin/config \
  -H 'Content-Type: application/json' \
  -d '{"traffic-simulation":{"prompt-cache":{"source":"disabled"}}}'

curl -sS -X POST http://127.0.0.1:8000/admin/config \
  -H 'Content-Type: application/json' \
  -d '{"traffic-simulation":{"prompt-cache":{"source":"logical-prefix","max-entries":10000,"max-total-tokens":10000000,"ttl":"5m"}}}'
```

修改任一 prompt-cache 配置会创建空缓存；再次发送相同 prompt 时，第一条请求建立缓存，第二条请求才报告命中。`/admin/config` 返回当前配置。修改外部 render 服务的 tokenizer 或 chat template 后也应清理缓存。

## 客户端验证

发送两次相同的 OpenAI Chat Completions 请求：

```bash
curl -sS http://127.0.0.1:8000/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"testmodel","messages":[{"role":"user","content":"Explain cache prefixes"}],"max_tokens":8}'
```

第二次响应中，`usage.prompt_tokens_details.cached_tokens` 应等于 `usage.prompt_tokens`。`prompt_tokens` 和 `total_tokens` 不因缓存命中而扣减。OpenAI `/v1/completions` 使用同一字段；流式请求需在 `stream_options` 中设置 `include_usage: true` 才会收到最终 usage frame。

OpenAI `/v1/responses` 返回 `usage.input_tokens_details.cached_tokens`，`input_tokens` 仍是完整输入量。Anthropic `/v1/messages` 返回 `usage.input_tokens`、`cache_read_input_tokens`、`cache_creation_input_tokens`；这三项之和等于完整输入 token 数。逻辑模式下，第一次请求通常全部计入 cache creation，重复请求全部计入 cache read。Anthropic 流式响应在 `message_start.message.usage` 返回输入缓存数据，`message_delta.usage` 返回输出 token 数。vLLM gRPC 的 chunk 和 complete 使用 `cached_tokens`；SGLang OpenAI 兼容路由沿用 OpenAI usage。

不同模型、LoRA、render URL 和 dummy tokenizer 模式不会共享逻辑缓存。包含多模态特征的请求旁路逻辑缓存，以防图像或音频占位符造成误命中。`X-Mock-Prompt-Tokens` 和 `X-Mock-Cached-Tokens` 测试控制会旁路真实缓存；测试控制需要 `traffic-simulation.enable-test-controls: true`。

## 清理和观测

查询统计：

```bash
curl -sS http://127.0.0.1:8000/admin/prompt-cache/stats
```

返回 `source`、`epoch`、`namespaces`、`entries`、`tokens`、`requests`、`queried_tokens`、`hit_tokens`、`written_tokens`、`hit_rate` 和驱逐计数，不返回 prompt 或 token 序列。TTL 过期项在下次请求或统计读取时移除。

清理全部逻辑缓存，或只清理一个已配置的模型别名：

```bash
curl -sS -X POST http://127.0.0.1:8000/admin/prompt-cache/clear \
  -H 'Content-Type: application/json' -d '{}'

curl -sS -X POST http://127.0.0.1:8000/admin/prompt-cache/clear \
  -H 'Content-Type: application/json' -d '{"model":"testmodel"}'
```

`epoch` 在清理和缓存配置变更时递增。已在处理中的请求保留自己的命中数。`/metrics` 提供：

```text
llmd_prompt_cache_requests_total
llmd_prompt_cache_tokens_total
llmd_prompt_cache_entries
llmd_prompt_cache_evictions_total
```

指标标签为 engine、profile、scenario、source，计数器还分别带 result、kind 或 reason。逻辑模式不会伪造 vLLM KV block 生命周期指标。

## 容量与存储

100 并发 x 100K token 和 1000 并发 x 10K token 都对应 1,000 万活跃 input token。若每个会话有独立的 80% 热前缀，缓存工作集约为 800 万 token；建议 `max-total-tokens: 10000000`、`max-entries: 10000`，Pod 从 `requests.memory: 1Gi`、`limits.memory: 2Gi` 起步。单测构造这两类并发负载后，驻留 trie 的 Go heap 分别约为 41 MB 和 42 MB；该值不包括 HTTP 请求 body、tokenization buffer、其他服务组件和进程 RSS。

实际容量由 TTL 内的不同前缀数量决定。若请求平均生命周期为 `T` 秒，五分钟内的独立缓存工作集上界约为 `0.8 * 上下文长度 * 300 * 并发数 / T` token。持续生成全新前缀时，应按该公式提高配额，或缩短 TTL、限制 `max-entries`，并观察驱逐率和命中率。缓存没有磁盘持久化，进程重启后从空缓存开始；无需为逻辑缓存配置持久卷。

需要 ZMQ KV block 事件时，请使用 `kv-block`。当前 block 实现会分配 `10 * kv-cache-size` 个事件队列槽位；800,000 blocks 时队列本身约 610 MiB。该模式的内存配额应按 block 数单独测算。

## 验证命令

在有 Docker/render 测试依赖的环境中运行：

```bash
go test ./pkg/promptcache ./pkg/common ./pkg/simulator ./pkg/communication ./pkg/endpoint
CGO_ENABLED=1 go test -race ./pkg/promptcache
go test ./pkg/tests -run TestSimulator -ginkgo.focus='prompt cache protocol usage'
make lint
make presubmit
```

`make presubmit` 包含项目级漏洞扫描。若该门禁报告与缓存改动无关的既有依赖漏洞，应保存完整输出并单独处理依赖升级，不跳过扫描。
