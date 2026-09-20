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
	"encoding/json"
	"errors"
	"time"

	"github.com/llm-d/llm-d-inference-sim/pkg/api"
)

var errStreamFaultDisconnect = errors.New("stream fault disconnect")

type streamFaultWriter struct {
	writer  *bufio.Writer
	policy  api.StreamFaultPolicy
	onFault func(string)
	chunks  int
}

func newStreamFaultWriter(writer *bufio.Writer, policy api.StreamFaultPolicy, onFault func(string)) *streamFaultWriter {
	return &streamFaultWriter{writer: writer, policy: policy, onFault: onFault}
}

func (w *streamFaultWriter) send(chunk sseChunk) error {
	b, err := chunk.SSEBytes()
	if err != nil {
		return err
	}
	if _, err := w.writer.Write(b); err != nil {
		return err
	}
	if err := w.writer.Flush(); err != nil {
		return err
	}
	w.chunks++
	if w.policy.StallAfterChunks > 0 && w.chunks == w.policy.StallAfterChunks {
		w.recordFault("stall")
		if w.policy.StallDuration > 0 {
			time.Sleep(w.policy.StallDuration)
		}
	}
	if w.policy.DisconnectAfterChunks > 0 && w.chunks >= w.policy.DisconnectAfterChunks {
		w.recordFault("disconnect")
		return errStreamFaultDisconnect
	}
	return nil
}

func (w *streamFaultWriter) recordFault(faultType string) {
	if w.onFault != nil {
		w.onFault(faultType)
	}
}

// corruptUsageChunk changes only the numeric usage totals in an OpenAI JSON
// SSE frame. Other stream formats retain their original payload.
type corruptUsageChunk struct{ chunk sseChunk }

func (c corruptUsageChunk) SSEBytes() ([]byte, error) {
	b, err := c.chunk.SSEBytes()
	if err != nil {
		return nil, err
	}
	const prefix = "data: "
	if len(b) < len(prefix) || string(b[:len(prefix)]) != prefix {
		return b, nil
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(b[len(prefix):], &payload); err != nil {
		return b, nil
	}
	usage, ok := payload["usage"]
	if !ok || string(usage) == "null" {
		return b, nil
	}
	var usageFields map[string]json.RawMessage
	if err := json.Unmarshal(usage, &usageFields); err != nil {
		return b, nil
	}
	var total int
	if err := json.Unmarshal(usageFields["total_tokens"], &total); err != nil {
		return b, nil
	}
	usageFields["total_tokens"], err = json.Marshal(total + 1)
	if err != nil {
		return nil, err
	}
	payload["usage"], err = json.Marshal(usageFields)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return append(append([]byte(prefix), encoded...), '\n', '\n'), nil
}
