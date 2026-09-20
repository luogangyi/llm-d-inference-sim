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
	"context"
	"net/http"
	"os"
	"path/filepath"

	"github.com/llm-d/llm-d-inference-sim/pkg/common"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/openai/openai-go/v3/packages/param"
)

const trafficProfilesDir = "../../examples/traffic-simulation/profiles"

type trafficProfile struct {
	name              string
	simulationProfile string
	model             string
	servedModel       string
	maxNumSeqs        int
	interTokenMillis  int
}

var _ = Describe("traffic simulation profiles", func() {
	profiles := []trafficProfile{
		{
			name:              "vllm-normal-chat.yaml",
			simulationProfile: "vllm-normal-chat",
			model:             "mock-vllm",
			servedModel:       "Qwen/Qwen3-32B",
			maxNumSeqs:        600,
			interTokenMillis:  18,
		},
		{
			name:              "vllm-ascend-normal-chat.yaml",
			simulationProfile: "vllm-ascend-normal-chat",
			model:             "mock-vllm-ascend",
			servedModel:       "Qwen/Qwen3-32B-Ascend",
			maxNumSeqs:        400,
			interTokenMillis:  25,
		},
		{
			name:              "sglang-openai-normal-chat.yaml",
			simulationProfile: "sglang-openai-normal-chat",
			model:             "mock-sglang-openai",
			servedModel:       "Qwen/Qwen3-32B-SGLang",
			maxNumSeqs:        600,
			interTokenMillis:  16,
		},
		{
			name:              "sglang-native-normal-chat.yaml",
			simulationProfile: "sglang-native-normal-chat",
			model:             "mock-sglang-native",
			servedModel:       "Qwen/Qwen3-32B-SGLang",
			maxNumSeqs:        600,
			interTokenMillis:  16,
		},
	}

	DescribeTable("loads the documented profile", func(profile trafficProfile) {
		oldArgs := os.Args
		DeferCleanup(func() { os.Args = oldArgs })
		os.Args = []string{"cmd", "--config", filepath.Join(trafficProfilesDir, profile.name)}

		eng, err := resolveEngine()
		Expect(err).NotTo(HaveOccurred())
		config, err := common.ParseCommandParamsAndLoadConfig(eng)
		Expect(err).NotTo(HaveOccurred())

		Expect(config.Model).To(Equal(profile.model))
		Expect(config.ServedModelNames).To(ContainElement(profile.servedModel))
		Expect(config.MaxModelLen).To(Equal(131072))
		Expect(config.MaxNumSeqs).To(Equal(profile.maxNumSeqs))
		Expect(config.Latencies.InterTokenLatency.Milliseconds()).To(Equal(int64(profile.interTokenMillis)))
		Expect(config.TrafficSimulation.Profile).To(Equal(profile.simulationProfile))
		Expect(config.TrafficSimulation.Scenario).To(Equal("default"))
		Expect(config.TrafficSimulation.EnableTestControls).To(BeFalse())
	},
		Entry("vLLM", profiles[0]),
		Entry("vLLM Ascend", profiles[1]),
		Entry("SGLang OpenAI", profiles[2]),
		Entry("SGLang native", profiles[3]),
	)

	DescribeTable("serves OpenAI client prompts", func(profile trafficProfile) {
		ctx := context.Background()
		profilePath := filepath.Join(trafficProfilesDir, profile.name)
		client, err := startServerWithArgs(ctx, []string{"cmd", "--config", profilePath})
		Expect(err).NotTo(HaveOccurred())

		for _, path := range []string{"/health", "/health/ready", "/v1/models"} {
			response, err := client.Get("http://localhost" + path)
			Expect(err).NotTo(HaveOccurred())
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(response.Body.Close()).To(Succeed())
		}

		openAIClient, chatParams := getOpenAIClientAndChatParams(client, profile.servedModel, "Explain the simulator response.", false)
		chatResponse, err := openAIClient.Chat.Completions.New(ctx, chatParams)
		Expect(err).NotTo(HaveOccurred())
		Expect(chatResponse.Choices).NotTo(BeEmpty())
		Expect(chatResponse.Usage.TotalTokens).To(Equal(chatResponse.Usage.PromptTokens + chatResponse.Usage.CompletionTokens))

		_, completionParams := getOpenAIClientAndCompletionParams(client, profile.servedModel, "Complete this prompt", true)
		completionParams.MaxTokens = param.NewOpt(int64(2))
		stream := openAIClient.Completions.NewStreaming(ctx, completionParams)
		DeferCleanup(func() { Expect(stream.Close()).To(Succeed()) })

		usageChunks := 0
		for stream.Next() {
			usage := stream.Current().Usage
			if usage.TotalTokens != 0 {
				usageChunks++
				Expect(usage.TotalTokens).To(Equal(usage.PromptTokens + usage.CompletionTokens))
			}
		}
		Expect(stream.Err()).NotTo(HaveOccurred())
		Expect(usageChunks).To(Equal(1))
	},
		Entry("vLLM", profiles[0]),
		Entry("vLLM Ascend", profiles[1]),
		Entry("SGLang OpenAI", profiles[2]),
		Entry("SGLang native", profiles[3]),
	)
})
