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

package communication

import (
	"encoding/json"

	"github.com/llm-d/llm-d-inference-sim/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SGLang native request translation", func() {
	It("maps native generation fields to the common completion request", func() {
		payload, apiErr := sglangGeneratePayload([]byte(`{"text":"Explain queues","stream":true,"sampling_params":{"max_new_tokens":7}}`), "mock-sglang")
		Expect(apiErr).To(BeNil())

		var request struct {
			Model     string `json:"model"`
			Prompt    string `json:"prompt"`
			MaxTokens int64  `json:"max_tokens"`
			Stream    bool   `json:"stream"`
		}
		Expect(json.Unmarshal(payload, &request)).To(Succeed())
		Expect(request.Model).To(Equal("mock-sglang"))
		Expect(request.Prompt).To(Equal("Explain queues"))
		Expect(request.MaxTokens).To(Equal(int64(7)))
		Expect(request.Stream).To(BeTrue())
	})

	It("uses the default output limit and rejects invalid prompt bodies", func() {
		payload, apiErr := sglangGeneratePayload([]byte(`{"text":"hello"}`), "mock-sglang")
		Expect(apiErr).To(BeNil())
		Expect(string(payload)).To(ContainSubstring(`"max_tokens":128`))

		_, apiErr = sglangGeneratePayload([]byte(`{"sampling_params":{"max_new_tokens":2}}`), "mock-sglang")
		Expect(apiErr).NotTo(BeNil())
		Expect(apiErr.Code).To(Equal(400))

		_, apiErr = sglangGeneratePayload([]byte(`{`), "mock-sglang")
		Expect(apiErr).NotTo(BeNil())
		Expect(apiErr.Code).To(Equal(400))
	})
})

var _ = Describe("SGLang native streaming response", func() {
	It("emits cumulative text in each SSE frame", func() {
		builder := &sglangHTTPRespBuilder{}
		first, err := builder.createChunk(nil, &api.Tokenized{Strings: []string{"first "}}, nil, "", nil, 0).SSEBytes()
		Expect(err).NotTo(HaveOccurred())
		second, err := builder.createChunk(nil, &api.Tokenized{Strings: []string{"second"}}, nil, "", nil, 0).SSEBytes()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(first)).To(ContainSubstring(`"text":"first "`))
		Expect(string(second)).To(ContainSubstring(`"text":"first second"`))
		Expect(builder.createDoneChunk().SSEBytes()).To(Equal([]byte("data: [DONE]\n\n")))
	})
})
