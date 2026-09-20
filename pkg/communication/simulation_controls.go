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
	"fmt"
	"strconv"
	"time"

	"github.com/llm-d/llm-d-inference-sim/pkg/api"
	"github.com/llm-d/llm-d-inference-sim/pkg/common"
	"github.com/valyala/fasthttp"
)

const (
	mockPromptTokensHeader = "X-Mock-Prompt-Tokens"
	mockCachedTokensHeader = "X-Mock-Cached-Tokens"
	mockOutputTokensHeader = "X-Mock-Output-Tokens"
	mockTTFTHeader         = "X-Mock-TTFT"
	mockITLHeader          = "X-Mock-ITL"
)

func parseSimulationControls(headers *fasthttp.RequestHeader, cfg *common.Configuration) (api.SimulationOverrides, *api.Error) {
	if !cfg.TrafficSimulation.EnableTestControls {
		return api.SimulationOverrides{}, nil
	}

	var overrides api.SimulationOverrides
	for _, control := range []struct {
		header string
		dest   **int
	}{
		{mockPromptTokensHeader, &overrides.PromptTokens},
		{mockCachedTokensHeader, &overrides.CachedPromptTokens},
		{mockOutputTokensHeader, &overrides.OutputTokens},
	} {
		value := string(headers.Peek(control.header))
		if value == "" {
			continue
		}
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 || parsed > cfg.MaxModelLen {
			return api.SimulationOverrides{}, invalidSimulationControl(control.header, value, "an integer within the model context window")
		}
		*control.dest = &parsed
	}

	for _, control := range []struct {
		header string
		dest   **time.Duration
	}{
		{mockTTFTHeader, &overrides.TTFT},
		{mockITLHeader, &overrides.ITL},
	} {
		value := string(headers.Peek(control.header))
		if value == "" {
			continue
		}
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed < 0 {
			return api.SimulationOverrides{}, invalidSimulationControl(control.header, value, "a non-negative Go duration")
		}
		*control.dest = &parsed
	}
	return overrides, nil
}

func invalidSimulationControl(header, value, want string) *api.Error {
	err := api.NewError(fmt.Sprintf("Invalid %s header value %q: must be %s", header, value, want), fasthttp.StatusBadRequest, nil)
	return &err
}
