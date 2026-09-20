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

package engine

import "testing"

func TestSelectSGLang(t *testing.T) {
	eng, err := Select("sglang")
	if err != nil {
		t.Fatalf("Select(sglang): %v", err)
	}
	if eng.Name() != "sglang" {
		t.Fatalf("Name() = %q, want sglang", eng.Name())
	}
	if eng.BindGRPC(nil, nil) {
		t.Fatal("SGLang must not register the vLLM gRPC service")
	}
}
