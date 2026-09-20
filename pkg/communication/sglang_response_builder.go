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
	"strings"

	"github.com/llm-d/llm-d-inference-sim/pkg/api"
	"github.com/llm-d/llm-d-inference-sim/pkg/endpoint"
)

type sglangGenerateResponse struct {
	Text     string         `json:"text"`
	MetaInfo sglangMetaInfo `json:"meta_info"`
}

type sglangMetaInfo struct {
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	FinishReason     string `json:"finish_reason"`
}

type sglangHTTPRespBuilder struct {
	baseRespBuilder
	text string
}

func (*sglangHTTPRespBuilder) createUsageChunk([]endpoint.ResponseContext) sseChunk { return nil }

func (b *sglangHTTPRespBuilder) createResponse(contexts []endpoint.ResponseContext, tokens []api.Tokenized) any {
	text := ""
	if len(tokens) > 0 {
		text = strings.Join(tokens[0].Strings, "")
	}
	usage := aggregateUsage(contexts)
	return sglangGenerateResponse{Text: text, MetaInfo: sglangMetaInfo{
		PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens,
		FinishReason: *contexts[0].FinishReason(),
	}}
}

func (b *sglangHTTPRespBuilder) createChunk(_ endpoint.ResponseContext, tokens *api.Tokenized,
	_ *api.ToolCall, _ string, _ *string, _ int) sseChunk {
	if tokens != nil {
		b.text += strings.Join(tokens.Strings, "")
	}
	return &jsonDataChunk{data: sglangGenerateResponse{Text: b.text}}
}

func (*sglangHTTPRespBuilder) createLastChunk(endpoint.ResponseContext, string, int) sseChunk {
	return nil
}

func (*sglangHTTPRespBuilder) sendFinishReasonWithTokens() bool { return true }

var _ responseBuilder = (*sglangHTTPRespBuilder)(nil)
