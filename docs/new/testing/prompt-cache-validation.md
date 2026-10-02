# Input Token 前缀缓存验证记录

日期：2026-10-03。测试环境：`root@139.196.28.96:32025`，代码目录 `/var/code/llm-d-inference-sim`，Docker 29.8.1，Go 1.26，GCC 10.3.1。

| 验证 | 命令或范围 | 结果 |
| --- | --- | --- |
| 缓存和相关组件单元测试 | `go test ./pkg/promptcache ./pkg/common ./pkg/simulator ./pkg/communication ./pkg/endpoint` | 通过 |
| 协议端到端测试 | `go test ./pkg/tests -run TestSimulator -ginkgo.focus='prompt cache protocol usage' -ginkgo.fail-fast` | 7/7 通过；覆盖开关、OpenAI Chat/Responses/Completions SSE、Anthropic Messages、vLLM gRPC、SGLang OpenAI、清理/指标及注入故障 |
| 数据竞争 | `CGO_ENABLED=1 go test -race ./pkg/promptcache` | 通过 |
| 大上下文容量 | `go test ./pkg/promptcache -run TestConcurrentWorkloadsAtCapacity -v` | 100 x 100K 和 1000 x 10K 均验证 80% 前缀命中，驻留 token 数不超过 1,000 万 |
| 格式和静态检查 | `make format && make lint` | 通过，0 issues |

大上下文单测在缓存已填充后读取 Go heap：100 x 100K 场景约 41.7 MB，1000 x 10K 场景约 42.8 MB；并发请求阶段分别约 20 ms 和 26 ms。该测试只测内存前缀索引，不代表 HTTP 端到端吞吐或 Pod RSS。

`go test ./...` 在测试机执行 447 个集成规格，其中 445 通过、2 个多模态 encoder-only 规格失败。失败请求依赖外部图片和 render 容器，render 的 `POST /v1/chat/completions/render` 等待 60 秒后 `context deadline exceeded`；错误出现在 tokenization 阶段，早于前缀缓存解析。缓存专项用例在同一环境中全部通过。

`make presubmit` 在测试机运行到漏洞扫描阶段：签名检查、`go.mod` 整洁性、格式与 lint 通过（0 issues）；`govulncheck` 报告现有 OpenTelemetry `v1.44.0` 依赖的 `GO-2026-6505`，因此门禁退出码为 2。缓存实现没有升级依赖，也没有跳过该检查。
