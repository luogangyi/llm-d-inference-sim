# Traffic simulation profiles

Each profile uses the existing OpenAI-compatible HTTP surface. Start one profile per simulator process or Deployment:

```bash
./bin/llm-d-inference-sim --config examples/traffic-simulation/profiles/vllm-normal-chat.yaml
```

The `*-normal-chat.yaml` files contain initial latency and concurrency values for integration testing. They are not capacity claims and must be calibrated against the target engine version and hardware before using them for sizing.

The `*-zero-delay.yaml` files remove simulated inference delay. Use them to measure the simulator, network, client, and gateway overhead independently of the virtual engine profile.

All files use a mock model name with no `render-url`, so the simulator selects its built-in tokenizer and does not download model weights. Use a render service and a real model name when a test needs exact tokenizer accounting.

`vllm-ascend-*` and `sglang-openai-*` remain OpenAI-compatible profiles. The SGLang native endpoints are a later milestone because their wire contract must be captured from the production SGLang version first.
