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
	mockPromptTokensHeader  = "X-Mock-Prompt-Tokens"
	mockCachedTokensHeader  = "X-Mock-Cached-Tokens"
	mockOutputTokensHeader  = "X-Mock-Output-Tokens"
	mockTTFTHeader          = "X-Mock-TTFT"
	mockITLHeader           = "X-Mock-ITL"
	mockDisconnectHeader    = "X-Mock-Disconnect-After-Chunks"
	mockOmitDoneHeader      = "X-Mock-Omit-Done"
	mockOmitUsageHeader     = "X-Mock-Omit-Usage"
	mockCorruptUsageHeader  = "X-Mock-Corrupt-Usage"
	mockStallAfterHeader    = "X-Mock-Stall-After-Chunks"
	mockStallDurationHeader = "X-Mock-Stall-Duration"
)

func parseSimulationControls(headers *fasthttp.RequestHeader, cfg *common.Configuration, isStream bool,
	random *common.Random) (api.SimulationOverrides, *api.Error) {
	overrides := api.SimulationOverrides{StreamFaults: configStreamFaultPolicy(cfg, random)}
	if !cfg.TrafficSimulation.EnableTestControls {
		return overrides, nil
	}

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

	if err := parseStreamFaultControls(headers, &overrides.StreamFaults, isStream); err != nil {
		return api.SimulationOverrides{}, err
	}
	return overrides, nil
}

func configStreamFaultPolicy(cfg *common.Configuration, random *common.Random) api.StreamFaultPolicy {
	faults := cfg.TrafficSimulation.StreamFaults
	policy := api.StreamFaultPolicy{}
	if random == nil {
		return policy
	}
	if random.RandomBool(faults.DisconnectRate) {
		policy.DisconnectAfterChunks = faults.DisconnectAfterChunks
	}
	if random.RandomBool(faults.StallRate) {
		policy.StallAfterChunks = faults.StallAfterChunks
		policy.StallDuration = faults.StallDuration
	}
	policy.OmitDone = random.RandomBool(faults.OmitDoneRate)
	policy.OmitUsage = random.RandomBool(faults.OmitUsageRate)
	policy.CorruptUsage = random.RandomBool(faults.CorruptUsageRate)
	return policy
}

func parseStreamFaultControls(headers *fasthttp.RequestHeader, policy *api.StreamFaultPolicy, isStream bool) *api.Error {
	hasControl := false
	for _, header := range []string{
		mockDisconnectHeader, mockOmitDoneHeader, mockOmitUsageHeader, mockCorruptUsageHeader,
		mockStallAfterHeader, mockStallDurationHeader,
	} {
		if len(headers.Peek(header)) > 0 {
			hasControl = true
			break
		}
	}
	if hasControl && !isStream {
		return invalidSimulationControl("X-Mock stream fault", "", "used only with stream=true")
	}

	if value := string(headers.Peek(mockDisconnectHeader)); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 {
			return invalidSimulationControl(mockDisconnectHeader, value, "a positive integer")
		}
		policy.DisconnectAfterChunks = parsed
	}

	for _, control := range []struct {
		header string
		dest   *bool
	}{
		{mockOmitDoneHeader, &policy.OmitDone},
		{mockOmitUsageHeader, &policy.OmitUsage},
		{mockCorruptUsageHeader, &policy.CorruptUsage},
	} {
		value := string(headers.Peek(control.header))
		if value == "" {
			continue
		}
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return invalidSimulationControl(control.header, value, "a boolean")
		}
		*control.dest = parsed
	}

	stallAfter := string(headers.Peek(mockStallAfterHeader))
	stallDuration := string(headers.Peek(mockStallDurationHeader))
	if (stallAfter == "") != (stallDuration == "") {
		return invalidSimulationControl("X-Mock-Stall", stallAfter+stallDuration, "both X-Mock-Stall-After-Chunks and X-Mock-Stall-Duration")
	}
	if stallAfter != "" {
		chunks, err := strconv.Atoi(stallAfter)
		if err != nil || chunks <= 0 {
			return invalidSimulationControl(mockStallAfterHeader, stallAfter, "a positive integer")
		}
		duration, err := time.ParseDuration(stallDuration)
		if err != nil || duration <= 0 {
			return invalidSimulationControl(mockStallDurationHeader, stallDuration, "a positive Go duration")
		}
		policy.StallAfterChunks = chunks
		policy.StallDuration = duration
	}
	return nil
}

func invalidSimulationControl(header, value, want string) *api.Error {
	err := api.NewError(fmt.Sprintf("Invalid %s header value %q: must be %s", header, value, want), fasthttp.StatusBadRequest, nil)
	return &err
}
