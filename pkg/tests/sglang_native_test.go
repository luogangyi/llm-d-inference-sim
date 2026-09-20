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
	"io"
	"net/http"
	"strings"

	"github.com/llm-d/llm-d-inference-sim/pkg/common"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func sglangRequest(stream bool) *http.Request {
	body := `{"text":"Explain queueing.","sampling_params":{"max_new_tokens":2},"stream":false}`
	if stream {
		body = strings.Replace(body, `"stream":false`, `"stream":true`, 1)
	}
	req, err := http.NewRequest(http.MethodPost, "http://localhost/generate", bytes.NewBufferString(body))
	Expect(err).NotTo(HaveOccurred())
	req.Header.Set("Content-Type", "application/json")
	return req
}

var _ = Describe("SGLang native HTTP", func() {
	It("serves native and OpenAI customer prompts from the SGLang engine", func() {
		client, err := startServerWithArgs(context.Background(), []string{
			"cmd", "--engine", "sglang", "--model", common.TestModelName, "--mode", common.ModeRandom,
		})
		Expect(err).NotTo(HaveOccurred())

		resp, err := client.Do(sglangRequest(false))
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(resp.Body.Close()).To(Succeed()) }()
		body, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(string(body)).To(ContainSubstring(`"text"`))
		Expect(string(body)).To(ContainSubstring(`"completion_tokens":`))

		streamResp, err := client.Do(sglangRequest(true))
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(streamResp.Body.Close()).To(Succeed()) }()
		streamBody, err := io.ReadAll(streamResp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(streamResp.StatusCode).To(Equal(http.StatusOK))
		Expect(string(streamBody)).To(ContainSubstring("data: "))
		Expect(string(streamBody)).To(ContainSubstring(`"text"`))
		Expect(string(streamBody)).To(ContainSubstring("data: [DONE]"))

		for _, path := range []string{"/model_info", "/get_model_info", "/server_info", "/health_generate"} {
			info, err := client.Get("http://localhost" + path)
			Expect(err).NotTo(HaveOccurred())
			Expect(info.StatusCode).To(Equal(http.StatusOK))
			Expect(info.Body.Close()).To(Succeed())
		}

		invalidResp, err := client.Post("http://localhost/generate", "application/json", strings.NewReader(`{"sampling_params":{"max_new_tokens":2}}`))
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(invalidResp.Body.Close()).To(Succeed()) }()
		Expect(invalidResp.StatusCode).To(Equal(http.StatusBadRequest))

		openAIResp, err := client.Post("http://localhost/v1/completions", "application/json",
			strings.NewReader(`{"model":"`+common.TestModelName+`","prompt":"Complete this customer prompt","max_tokens":1}`))
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(openAIResp.Body.Close()).To(Succeed()) }()
		Expect(openAIResp.StatusCode).To(Equal(http.StatusOK))
	})
})
