/*
Copyright 2026 The llm-d-inference-sim Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/llm-d/llm-d-inference-sim/pkg/api"
	"github.com/llm-d/llm-d-inference-sim/pkg/communication/grpc/pb"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func startPromptCacheServer(source string) (*http.Client, error) {
	return startServerWithArgs(context.Background(), []string{"cmd", "--config", promptCacheConfigPath(source)})
}

func promptCacheConfigPath(source string) string {
	path := filepath.Join(GinkgoT().TempDir(), "prompt-cache.yaml")
	config := "model: test-model\nmode: random\nmax-model-len: 256\ntraffic-simulation:\n  enable-test-controls: true\n  prompt-cache:\n    source: " + source + "\n    max-entries: 100\n    max-total-tokens: 10000\n    ttl: 5m\n"
	Expect(os.WriteFile(path, []byte(config), 0o600)).To(Succeed())
	return path
}

func postPromptCacheJSON(client *http.Client, path, body string, headers map[string]string) map[string]any {
	req, err := http.NewRequest(http.MethodPost, "http://localhost"+path, bytes.NewBufferString(body))
	Expect(err).NotTo(HaveOccurred())
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close() //nolint:errcheck
	data, err := io.ReadAll(resp.Body)
	Expect(err).NotTo(HaveOccurred())
	Expect(resp.StatusCode).To(Equal(http.StatusOK), "body: %s", string(data))
	var parsed map[string]any
	Expect(json.Unmarshal(data, &parsed)).To(Succeed())
	return parsed
}

func mapField(value any, key string) map[string]any {
	obj, ok := value.(map[string]any)
	Expect(ok).To(BeTrue())
	result, ok := obj[key].(map[string]any)
	Expect(ok).To(BeTrue())
	return result
}

var _ = Describe("prompt cache protocol usage", func() {
	It("switches between disabled and logical-prefix for OpenAI chat", func() {
		client, err := startPromptCacheServer("disabled")
		Expect(err).NotTo(HaveOccurred())
		body := `{"model":"test-model","messages":[{"role":"user","content":"A client asks about cache accounting."}],"max_tokens":1}`
		first := postPromptCacheJSON(client, "/v1/chat/completions", body, nil)
		second := postPromptCacheJSON(client, "/v1/chat/completions", body, nil)
		Expect(mapField(mapField(first, "usage"), "prompt_tokens_details")["cached_tokens"]).To(BeNumerically("==", 0))
		Expect(mapField(mapField(second, "usage"), "prompt_tokens_details")["cached_tokens"]).To(BeNumerically("==", 0))

		update := postPromptCacheJSON(client, "/admin/config", `{"traffic-simulation":{"prompt-cache":{"source":"logical-prefix"}}}`, nil)
		_ = update
		third := postPromptCacheJSON(client, "/v1/chat/completions", body, nil)
		fourth := postPromptCacheJSON(client, "/v1/chat/completions", body, nil)
		prompt := mapField(third, "usage")["prompt_tokens"]
		Expect(mapField(mapField(third, "usage"), "prompt_tokens_details")["cached_tokens"]).To(BeNumerically("==", 0))
		Expect(mapField(mapField(fourth, "usage"), "prompt_tokens_details")["cached_tokens"]).To(Equal(prompt))
		Expect(mapField(fourth, "usage")["total_tokens"]).To(BeNumerically(">=", prompt))
	})

	It("reports cached tokens in OpenAI Responses and bypasses test overrides", func() {
		client, err := startPromptCacheServer("logical-prefix")
		Expect(err).NotTo(HaveOccurred())
		body := `{"model":"test-model","input":"Responses cache prompt","max_output_tokens":1}`
		overridden := postPromptCacheJSON(client, "/v1/responses", body, map[string]string{"X-Mock-Cached-Tokens": "2"})
		Expect(mapField(mapField(overridden, "usage"), "input_tokens_details")["cached_tokens"]).To(BeNumerically("==", 2))
		first := postPromptCacheJSON(client, "/v1/responses", body, nil)
		second := postPromptCacheJSON(client, "/v1/responses", body, nil)
		Expect(mapField(mapField(first, "usage"), "input_tokens_details")["cached_tokens"]).To(BeNumerically("==", 0))
		Expect(mapField(mapField(second, "usage"), "input_tokens_details")["cached_tokens"]).To(Equal(mapField(second, "usage")["input_tokens"]))
	})

	It("reports Anthropic creation and read tokens in nonstream and message_start", func() {
		client, err := startPromptCacheServer("logical-prefix")
		Expect(err).NotTo(HaveOccurred())
		messages := []map[string]any{{"role": "user", "content": "Anthropic cache prompt"}}
		body := buildMessagesBody("test-model", false, messages, "", nil, nil)
		first := sendMessagesRequest(client, body)
		Expect(first.Usage.CacheCreationInputTokens).To(BeNumerically(">", 0))
		Expect(first.Usage.CacheReadInputTokens).To(Equal(0))
		Expect(first.Usage.InputTokens).To(Equal(0))
		stream := readMessagesSSEStream(client, buildMessagesBody("test-model", true, messages, "", nil, nil))
		Expect(stream).NotTo(BeEmpty())
		Expect(stream[0].EventType).To(Equal(api.MessagesEventMessageStart))
		var start api.MessagesMessageStartEvent
		Expect(json.Unmarshal(stream[0].Data, &start)).To(Succeed())
		Expect(start.Message.Usage.CacheReadInputTokens).To(Equal(first.Usage.CacheCreationInputTokens))
		Expect(start.Message.Usage.InputTokens).To(Equal(0))
	})

	It("reports streaming completion usage, stats, metrics, and clear", func() {
		client, err := startPromptCacheServer("logical-prefix")
		Expect(err).NotTo(HaveOccurred())
		body := `{"model":"test-model","prompt":"A streaming cache prompt","max_tokens":1,"stream":true,"stream_options":{"include_usage":true}}`
		for request := range 2 {
			resp, err := client.Post("http://localhost/v1/completions", "application/json", strings.NewReader(body))
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			usage := readStreamUsage(resp.Body)
			Expect(resp.Body.Close()).To(Succeed())
			Expect(usage.PromptTokensDetails).NotTo(BeNil())
			if request == 0 {
				Expect(usage.PromptTokensDetails.CachedTokens).To(Equal(0))
			} else {
				Expect(usage.PromptTokensDetails.CachedTokens).To(Equal(usage.PromptTokens))
			}
		}
		statsResp, err := client.Get("http://localhost/admin/prompt-cache/stats")
		Expect(err).NotTo(HaveOccurred())
		statsBody, err := io.ReadAll(statsResp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(statsResp.Body.Close()).To(Succeed())
		var stats map[string]any
		Expect(json.Unmarshal(statsBody, &stats)).To(Succeed())
		Expect(stats["entries"]).To(BeNumerically("==", 1))
		Expect(stats["hit_rate"]).To(BeNumerically(">", 0))

		metricsResp, err := client.Get("http://localhost/metrics")
		Expect(err).NotTo(HaveOccurred())
		metrics, err := io.ReadAll(metricsResp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(metricsResp.Body.Close()).To(Succeed())
		Expect(string(metrics)).To(ContainSubstring("llmd_prompt_cache_requests_total"))
		Expect(string(metrics)).To(ContainSubstring("llmd_prompt_cache_tokens_total"))

		clear := postPromptCacheJSON(client, "/admin/prompt-cache/clear", `{}`, nil)
		Expect(clear["entries"]).To(BeNumerically("==", 0))
		resp, err := client.Post("http://localhost/v1/completions", "application/json", strings.NewReader(body))
		Expect(err).NotTo(HaveOccurred())
		usage := readStreamUsage(resp.Body)
		Expect(resp.Body.Close()).To(Succeed())
		Expect(usage.PromptTokensDetails.CachedTokens).To(Equal(0))
	})

	It("reports the same cached token count in vLLM gRPC chunks and completion", func() {
		_, comm, _, err := startServerHandle(context.Background(), "",
			[]string{"cmd", "--config", promptCacheConfigPath("logical-prefix")}, nil)
		Expect(err).NotTo(HaveOccurred())
		for request := range 2 {
			maxTokens := uint32(3)
			req := &pb.GenerateRequest{
				RequestId:      "prompt-cache-grpc-" + string(rune('a'+request)),
				Input:          &pb.GenerateRequest_Tokenized{Tokenized: &pb.TokenizedInput{InputIds: []uint32{11, 22, 33}}},
				SamplingParams: &pb.SamplingParams{MaxTokens: &maxTokens},
				Stream:         true,
			}
			out := mockGenerateServer{ctx: context.Background()}
			Expect(comm.Generate(req, &out)).To(Succeed())
			Expect(out.responses).NotTo(BeEmpty())
			want := uint32(request * 3)
			for _, response := range out.responses {
				if chunk := response.GetChunk(); chunk != nil {
					Expect(chunk.CachedTokens).To(Equal(want))
				}
				if complete := response.GetComplete(); complete != nil {
					Expect(complete.CachedTokens).To(Equal(want))
				}
			}
		}
	})

	It("uses OpenAI cached usage with the SGLang engine", func() {
		client, err := startServerWithArgs(context.Background(), []string{
			"cmd", "--engine", "sglang", "--config", promptCacheConfigPath("logical-prefix"),
		})
		Expect(err).NotTo(HaveOccurred())
		body := `{"model":"test-model","messages":[{"role":"user","content":"SGLang cache prompt"}],"max_tokens":1}`
		first := postPromptCacheJSON(client, "/v1/chat/completions", body, nil)
		second := postPromptCacheJSON(client, "/v1/chat/completions", body, nil)
		Expect(mapField(mapField(first, "usage"), "prompt_tokens_details")["cached_tokens"]).To(BeNumerically("==", 0))
		Expect(mapField(mapField(second, "usage"), "prompt_tokens_details")["cached_tokens"]).To(Equal(mapField(second, "usage")["prompt_tokens"]))
	})

	It("does not populate the cache from a rejected request", func() {
		client, err := startPromptCacheServer("logical-prefix")
		Expect(err).NotTo(HaveOccurred())
		body := `{"model":"test-model","messages":[{"role":"user","content":"Retry this cache prompt"}],"max_tokens":1}`
		update := postAdminConfig(client, `{"failure-injection-rate":100,"failure-types":["server_error"]}`)
		Expect(update.StatusCode).To(Equal(http.StatusOK))
		Expect(update.Body.Close()).To(Succeed())
		failed, err := client.Post("http://localhost/v1/chat/completions", "application/json", strings.NewReader(body))
		Expect(err).NotTo(HaveOccurred())
		Expect(failed.StatusCode).To(Equal(http.StatusServiceUnavailable))
		Expect(failed.Body.Close()).To(Succeed())
		update = postAdminConfig(client, `{"failure-injection-rate":0}`)
		Expect(update.StatusCode).To(Equal(http.StatusOK))
		Expect(update.Body.Close()).To(Succeed())
		first := postPromptCacheJSON(client, "/v1/chat/completions", body, nil)
		second := postPromptCacheJSON(client, "/v1/chat/completions", body, nil)
		Expect(mapField(mapField(first, "usage"), "prompt_tokens_details")["cached_tokens"]).To(BeNumerically("==", 0))
		Expect(mapField(mapField(second, "usage"), "prompt_tokens_details")["cached_tokens"]).To(Equal(mapField(second, "usage")["prompt_tokens"]))
	})
})
