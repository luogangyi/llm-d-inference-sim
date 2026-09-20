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
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/llm-d/llm-d-inference-sim/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const trafficControlsConfig = `
model: test-model
mode: random
max-model-len: 64
traffic-simulation:
  profile: vllm-normal-chat
  scenario: default
  enable-test-controls: true
`

func startTrafficControlsServer(ctx context.Context, enabled bool) (*http.Client, error) {
	config := trafficControlsConfig
	if !enabled {
		config = strings.Replace(config, "enable-test-controls: true", "enable-test-controls: false", 1)
	}
	path := filepath.Join(GinkgoT().TempDir(), "traffic-controls.yaml")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		return nil, err
	}
	return startServerWithArgs(ctx, []string{"cmd", "--config", path})
}

func trafficControlsRequest(stream bool) *http.Request {
	body := `{"model":"test-model","prompt":"measure this prompt","max_tokens":1}`
	if stream {
		body = `{"model":"test-model","prompt":"measure this prompt","max_tokens":1,"stream":true,"stream_options":{"include_usage":true}}`
	}
	req, err := http.NewRequest(http.MethodPost, "http://localhost/v1/completions", bytes.NewBufferString(body))
	Expect(err).ToNot(HaveOccurred())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Mock-Prompt-Tokens", "12")
	req.Header.Set("X-Mock-Cached-Tokens", "5")
	req.Header.Set("X-Mock-Output-Tokens", "3")
	req.Header.Set("X-Mock-TTFT", "0s")
	req.Header.Set("X-Mock-ITL", "0s")
	return req
}

func setTrafficControlHeaders(req *http.Request, ttft, itl string) {
	req.Header.Set("X-Mock-Prompt-Tokens", "12")
	req.Header.Set("X-Mock-Cached-Tokens", "5")
	req.Header.Set("X-Mock-Output-Tokens", "3")
	req.Header.Set("X-Mock-TTFT", ttft)
	req.Header.Set("X-Mock-ITL", itl)
}

func readStreamUsage(body io.Reader) api.Usage {
	scanner := bufio.NewScanner(body)
	var usage api.Usage
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		var chunk struct {
			Choices []json.RawMessage `json:"choices"`
			Usage   api.Usage         `json:"usage"`
		}
		Expect(json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk)).To(Succeed())
		if len(chunk.Choices) == 0 && chunk.Usage.TotalTokens != 0 {
			usage = chunk.Usage
		}
	}
	Expect(scanner.Err()).ToNot(HaveOccurred())
	return usage
}

var _ = Describe("traffic simulation controls", func() {
	It("uses the same logical token usage for streaming and non-streaming completion prompts", func() {
		client, err := startTrafficControlsServer(context.Background(), true)
		Expect(err).ToNot(HaveOccurred())

		nonStreamResp, err := client.Do(trafficControlsRequest(false))
		Expect(err).ToNot(HaveOccurred())
		defer func() { Expect(nonStreamResp.Body.Close()).To(Succeed()) }()
		Expect(nonStreamResp.StatusCode).To(Equal(http.StatusOK))
		var nonStream struct {
			Usage api.Usage `json:"usage"`
		}
		Expect(json.NewDecoder(nonStreamResp.Body).Decode(&nonStream)).To(Succeed())

		streamResp, err := client.Do(trafficControlsRequest(true))
		Expect(err).ToNot(HaveOccurred())
		defer func() { Expect(streamResp.Body.Close()).To(Succeed()) }()
		Expect(streamResp.StatusCode).To(Equal(http.StatusOK))
		streamUsage := readStreamUsage(streamResp.Body)

		expected := api.Usage{PromptTokens: 12, CompletionTokens: 3, TotalTokens: 15}
		Expect(nonStream.Usage.PromptTokens).To(Equal(expected.PromptTokens))
		Expect(nonStream.Usage.CompletionTokens).To(Equal(expected.CompletionTokens))
		Expect(nonStream.Usage.TotalTokens).To(Equal(expected.TotalTokens))
		Expect(streamUsage.PromptTokens).To(Equal(expected.PromptTokens))
		Expect(streamUsage.CompletionTokens).To(Equal(expected.CompletionTokens))
		Expect(streamUsage.TotalTokens).To(Equal(expected.TotalTokens))

		chat, err := http.NewRequest(http.MethodPost, "http://localhost/v1/chat/completions",
			strings.NewReader(`{"model":"test-model","messages":[{"role":"user","content":"measure this chat prompt"}]}`))
		Expect(err).ToNot(HaveOccurred())
		chat.Header.Set("Content-Type", "application/json")
		setTrafficControlHeaders(chat, "0s", "0s")
		chatResp, err := client.Do(chat)
		Expect(err).ToNot(HaveOccurred())
		defer func() { Expect(chatResp.Body.Close()).To(Succeed()) }()
		Expect(chatResp.StatusCode).To(Equal(http.StatusOK))
		var chatResult struct {
			Usage api.Usage `json:"usage"`
		}
		Expect(json.NewDecoder(chatResp.Body).Decode(&chatResult)).To(Succeed())
		Expect(chatResult.Usage.PromptTokens).To(Equal(expected.PromptTokens))
		Expect(chatResult.Usage.CompletionTokens).To(Equal(expected.CompletionTokens))
	})

	It("ignores mock headers while controls are disabled", func() {
		client, err := startTrafficControlsServer(context.Background(), false)
		Expect(err).ToNot(HaveOccurred())

		resp, err := client.Do(trafficControlsRequest(false))
		Expect(err).ToNot(HaveOccurred())
		defer func() { Expect(resp.Body.Close()).To(Succeed()) }()
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		var result struct {
			Usage api.Usage `json:"usage"`
		}
		Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())
		Expect(result.Usage.PromptTokens).ToNot(Equal(12))
	})

	It("rejects invalid mock headers and atomically updates the scenario", func() {
		client, err := startTrafficControlsServer(context.Background(), true)
		Expect(err).ToNot(HaveOccurred())

		invalid := trafficControlsRequest(false)
		invalid.Header.Set("X-Mock-Prompt-Tokens", "-1")
		resp, err := client.Do(invalid)
		Expect(err).ToNot(HaveOccurred())
		defer func() { Expect(resp.Body.Close()).To(Succeed()) }()
		Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))

		update, err := http.NewRequest(http.MethodPost, adminConfigURL,
			strings.NewReader(`{"traffic-simulation":{"scenario":"overloaded"}}`))
		Expect(err).ToNot(HaveOccurred())
		update.Header.Set("Content-Type", "application/json")
		updated, err := client.Do(update)
		Expect(err).ToNot(HaveOccurred())
		defer func() { Expect(updated.Body.Close()).To(Succeed()) }()
		Expect(updated.StatusCode).To(Equal(http.StatusOK))
		body, err := io.ReadAll(updated.Body)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(body)).To(ContainSubstring(`"scenario":"overloaded"`))

		metrics, err := client.Get(metricsUrl)
		Expect(err).ToNot(HaveOccurred())
		defer func() { Expect(metrics.Body.Close()).To(Succeed()) }()
		metricsBody, err := io.ReadAll(metrics.Body)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(metricsBody)).To(ContainSubstring(`llmd_simulation_info{engine="vllm",profile="vllm-normal-chat",scenario="overloaded"} 1`))
	})

	It("keeps per-request TTFT and ITL overrides isolated for concurrent prompts", func() {
		client, err := startTrafficControlsServer(context.Background(), true)
		Expect(err).ToNot(HaveOccurred())

		type result struct{ elapsed time.Duration }
		start := make(chan struct{})
		results := make(chan result, 2)
		var wg sync.WaitGroup
		for _, delays := range [][2]string{{"120ms", "40ms"}, {"0s", "0s"}} {
			wg.Add(1)
			go func(ttft, itl string) {
				defer GinkgoRecover()
				defer wg.Done()
				req := trafficControlsRequest(false)
				setTrafficControlHeaders(req, ttft, itl)
				<-start
				started := time.Now()
				resp, requestErr := client.Do(req)
				Expect(requestErr).ToNot(HaveOccurred())
				Expect(resp.Body.Close()).To(Succeed())
				results <- result{elapsed: time.Since(started)}
			}(delays[0], delays[1])
		}
		close(start)
		wg.Wait()
		close(results)

		var durations []time.Duration
		for result := range results {
			durations = append(durations, result.elapsed)
		}
		Expect(durations).To(HaveLen(2))
		Expect(durations[0]).ToNot(Equal(durations[1]))
		var slow, fast time.Duration
		if durations[0] > durations[1] {
			slow, fast = durations[0], durations[1]
		} else {
			slow, fast = durations[1], durations[0]
		}
		Expect(slow).To(BeNumerically(">=", 150*time.Millisecond))
		Expect(fast).To(BeNumerically("<", 100*time.Millisecond))
	})
})
