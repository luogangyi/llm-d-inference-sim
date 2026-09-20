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
	"bufio"
	"bytes"
	"encoding/json"

	"github.com/llm-d/llm-d-inference-sim/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/valyala/fasthttp"
)

var _ = Describe("stream fault writer", func() {
	It("writes the selected number of frames before disconnecting", func() {
		var body bytes.Buffer
		var faults []string
		writer := newStreamFaultWriter(bufio.NewWriter(&body), api.StreamFaultPolicy{DisconnectAfterChunks: 2},
			func(fault string) { faults = append(faults, fault) })

		Expect(writer.send(&jsonDataChunk{data: map[string]int{"chunk": 1}})).To(Succeed())
		Expect(writer.send(&jsonDataChunk{data: map[string]int{"chunk": 2}})).To(MatchError(errStreamFaultDisconnect))
		Expect(body.String()).To(Equal("data: {\"chunk\":1}\n\ndata: {\"chunk\":2}\n\n"))
		Expect(faults).To(Equal([]string{"disconnect"}))
	})

	It("changes only total_tokens in an OpenAI usage frame", func() {
		chunk := corruptUsageChunk{chunk: &jsonDataChunk{data: map[string]any{
			"usage":   map[string]int{"prompt_tokens": 12, "completion_tokens": 3, "total_tokens": 15},
			"choices": []any{},
		}}}

		encoded, err := chunk.SSEBytes()
		Expect(err).NotTo(HaveOccurred())
		var payload struct {
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			} `json:"usage"`
		}
		Expect(json.Unmarshal(bytes.TrimSuffix(bytes.TrimPrefix(encoded, []byte("data: ")), []byte("\n\n")), &payload)).To(Succeed())
		Expect(payload.Usage.PromptTokens).To(Equal(12))
		Expect(payload.Usage.CompletionTokens).To(Equal(3))
		Expect(payload.Usage.TotalTokens).To(Equal(16))
	})

	It("rejects a stream-only header on a non-streaming request", func() {
		header := fasthttp.RequestHeader{}
		header.Set(mockOmitDoneHeader, "true")
		policy := api.StreamFaultPolicy{}

		err := parseStreamFaultControls(&header, &policy, false)
		Expect(err).NotTo(BeNil())
		Expect(err.Message).To(ContainSubstring("stream=true"))
	})
})
