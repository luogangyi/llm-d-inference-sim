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
	"io"
	"net/http"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func readTrafficStream(client *http.Client, req *http.Request) (int, string, time.Duration) {
	started := time.Now()
	resp, err := client.Do(req)
	Expect(err).NotTo(HaveOccurred())
	defer func() { Expect(resp.Body.Close()).To(Succeed()) }()
	body, err := io.ReadAll(resp.Body)
	Expect(err).NotTo(HaveOccurred())
	return resp.StatusCode, string(body), time.Since(started)
}

var _ = Describe("traffic simulation stream faults", func() {
	It("ends a customer completion stream without usage or DONE after the requested chunk", func() {
		client, err := startTrafficControlsServer(context.Background(), true)
		Expect(err).NotTo(HaveOccurred())

		req := trafficControlsRequest(true)
		req.Header.Set("X-Mock-Disconnect-After-Chunks", "2")
		status, body, _ := readTrafficStream(client, req)

		Expect(status).To(Equal(http.StatusOK))
		Expect(body).To(ContainSubstring("data: "))
		Expect(body).ToNot(ContainSubstring(`"usage":{`))
		Expect(body).ToNot(ContainSubstring("data: [DONE]"))
	})

	It("omits only the requested terminal frames from a customer completion stream", func() {
		client, err := startTrafficControlsServer(context.Background(), true)
		Expect(err).NotTo(HaveOccurred())

		req := trafficControlsRequest(true)
		req.Header.Set("X-Mock-Omit-Usage", "true")
		req.Header.Set("X-Mock-Omit-Done", "true")
		status, body, _ := readTrafficStream(client, req)

		Expect(status).To(Equal(http.StatusOK))
		Expect(body).To(ContainSubstring(`"finish_reason":"length"`))
		Expect(body).ToNot(ContainSubstring(`"usage":{`))
		Expect(body).ToNot(ContainSubstring("data: [DONE]"))
	})

	It("returns deterministically corrupted usage without changing stream completion", func() {
		client, err := startTrafficControlsServer(context.Background(), true)
		Expect(err).NotTo(HaveOccurred())

		req := trafficControlsRequest(true)
		req.Header.Set("X-Mock-Corrupt-Usage", "true")
		status, body, _ := readTrafficStream(client, req)

		Expect(status).To(Equal(http.StatusOK))
		Expect(body).To(ContainSubstring(`"finish_reason":"length"`))
		Expect(body).To(ContainSubstring(`"total_tokens":16`))
		Expect(body).To(ContainSubstring("data: [DONE]"))
	})

	It("stalls a customer completion stream after the requested chunk", func() {
		client, err := startTrafficControlsServer(context.Background(), true)
		Expect(err).NotTo(HaveOccurred())

		req := trafficControlsRequest(true)
		req.Header.Set("X-Mock-Stall-After-Chunks", "1")
		req.Header.Set("X-Mock-Stall-Duration", "20ms")
		status, body, elapsed := readTrafficStream(client, req)

		Expect(status).To(Equal(http.StatusOK))
		Expect(body).To(ContainSubstring("data: [DONE]"))
		Expect(elapsed).To(BeNumerically(">=", 15*time.Millisecond))
	})

	It("rejects stream-only controls on a non-streaming customer request", func() {
		client, err := startTrafficControlsServer(context.Background(), true)
		Expect(err).NotTo(HaveOccurred())

		req := trafficControlsRequest(false)
		req.Header.Set("X-Mock-Omit-Done", "true")
		resp, err := client.Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(resp.Body.Close()).To(Succeed()) }()
		body, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())

		Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		Expect(strings.ToLower(string(body))).To(ContainSubstring("stream"))
	})

	It("records an omitted terminal frame with the active simulation identity", func() {
		client, err := startTrafficControlsServer(context.Background(), true)
		Expect(err).NotTo(HaveOccurred())

		req := trafficControlsRequest(true)
		req.Header.Set("X-Mock-Omit-Done", "true")
		_, body, _ := readTrafficStream(client, req)
		Expect(body).ToNot(ContainSubstring("data: [DONE]"))

		metricsResp, err := client.Get(metricsUrl)
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(metricsResp.Body.Close()).To(Succeed()) }()
		metricsBody, err := io.ReadAll(metricsResp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(metricsBody)).To(ContainSubstring(`llmd_simulation_stream_faults_total{profile="vllm-normal-chat",scenario="default",type="omit_done"} 1`))
	})
})
