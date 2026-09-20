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

package sglang

import (
	"github.com/buaazp/fasthttprouter"
	"github.com/spf13/pflag"
	"google.golang.org/grpc"

	"github.com/llm-d/llm-d-inference-sim/pkg/common"
	"github.com/llm-d/llm-d-inference-sim/pkg/communication"
)

func (Engine) ApplyDefaults(_ *common.Configuration) {}

func (Engine) BindFlags(_ *pflag.FlagSet, _ *common.Configuration, _ map[string]any) error {
	return nil
}

func (Engine) ValidateConfig(_ *common.Configuration) error { return nil }

func (Engine) BindHTTP(r *fasthttprouter.Router, comm *communication.Communication) {
	r.POST("/generate", comm.HandleSGLangGenerate)
	r.GET("/model_info", comm.HandleSGLangModelInfo)
	r.GET("/get_model_info", comm.HandleSGLangModelInfo)
	r.GET("/server_info", comm.HandleSGLangServerInfo)
	r.GET("/health_generate", comm.HandleHealth)
}

func (Engine) BindGRPC(_ *grpc.Server, _ *communication.Communication) bool { return false }
