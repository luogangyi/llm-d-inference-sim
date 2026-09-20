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
	"github.com/llm-d/llm-d-inference-sim/pkg/endpoint"
	"github.com/valyala/fasthttp"
)

type sglangGenerateInput struct {
	Text           string `json:"text"`
	Stream         bool   `json:"stream"`
	SamplingParams struct {
		MaxNewTokens *int64 `json:"max_new_tokens"`
	} `json:"sampling_params"`
}

// HandleSGLangGenerate adapts SGLang's native text request into the common
// completion request pipeline.
func (c *Communication) HandleSGLangGenerate(ctx *fasthttp.RequestCtx) {
	model := c.runtime.Config().DisplayModelName
	if model == "" {
		model = c.runtime.Config().Model
	}
	payload, apiErr := sglangGeneratePayload(ctx.Request.Body(), model)
	if apiErr != nil {
		c.sendError(ctx, apiErr, false)
		return
	}
	ctx.Request.SetBody(payload)
	c.handleHTTP(&endpoint.TextCompletionsParsedRequest{}, &sglangHTTPRespBuilder{}, ctx)
}

func sglangGeneratePayload(body []byte, model string) ([]byte, *api.Error) {
	var input sglangGenerateInput
	if err := json.Unmarshal(body, &input); err != nil {
		apiErr := api.NewError("Failed to read and parse request body, "+err.Error(), fasthttp.StatusBadRequest, nil)
		return nil, &apiErr
	}
	if input.Text == "" {
		apiErr := api.NewError("Missing text field", fasthttp.StatusBadRequest, nil)
		return nil, &apiErr
	}
	maxTokens := int64(128)
	if input.SamplingParams.MaxNewTokens != nil {
		maxTokens = *input.SamplingParams.MaxNewTokens
	}
	payload, err := json.Marshal(struct {
		Model     string `json:"model"`
		Prompt    string `json:"prompt"`
		MaxTokens int64  `json:"max_tokens"`
		Stream    bool   `json:"stream"`
	}{model, input.Text, maxTokens, input.Stream})
	if err != nil {
		apiErr := api.NewError("Failed to construct SGLang request, "+err.Error(), fasthttp.StatusInternalServerError, nil)
		return nil, &apiErr
	}
	return payload, nil
}

func (c *Communication) HandleSGLangModelInfo(ctx *fasthttp.RequestCtx) {
	model := c.runtime.Config().DisplayModelName
	if model == "" {
		model = c.runtime.Config().Model
	}
	body, err := json.Marshal(struct {
		ModelPath     string `json:"model_path"`
		TokenizerPath string `json:"tokenizer_path"`
		IsGeneration  bool   `json:"is_generation"`
		MaxModelLen   int    `json:"max_model_len"`
	}{model, model, true, c.runtime.Config().MaxModelLen})
	if err != nil {
		apiErr := api.NewError("Failed to construct model info, "+err.Error(), fasthttp.StatusInternalServerError, nil)
		c.sendError(ctx, &apiErr, false)
		return
	}
	c.addResponseHeaders(ctx, c.getRequestID(ctx))
	ctx.SetContentType("application/json")
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetBody(body)
}

func (c *Communication) HandleSGLangServerInfo(ctx *fasthttp.RequestCtx) {
	model := c.runtime.Config().DisplayModelName
	if model == "" {
		model = c.runtime.Config().Model
	}
	body, err := json.Marshal(struct {
		ModelPath string `json:"model_path"`
		Engine    string `json:"engine"`
		Profile   string `json:"profile"`
	}{model, "sglang", c.runtime.Config().TrafficSimulation.Profile})
	if err != nil {
		apiErr := api.NewError("Failed to construct server info, "+err.Error(), fasthttp.StatusInternalServerError, nil)
		c.sendError(ctx, &apiErr, false)
		return
	}
	c.addResponseHeaders(ctx, c.getRequestID(ctx))
	ctx.SetContentType("application/json")
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetBody(body)
}
