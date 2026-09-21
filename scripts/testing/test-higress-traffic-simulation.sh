#!/usr/bin/env bash

# Checks the Kubernetes resources rendered for the Higress traffic test.
set -euo pipefail

root_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
manifest=$(mktemp)
trap 'rm -f "$manifest"' EXIT

"$root_dir/scripts/testing/run-higress-traffic-simulation.sh" \
  --profile "$root_dir/examples/traffic-simulation/profiles/vllm-zero-delay.yaml" \
  --node-ip 192.168.13.5 \
  --render-manifest >"$manifest"

grep -Fqx '  name: llm-sim-e2e' "$manifest"
grep -Fqx '    kubernetes.io/service-name: llm-d-sim' "$manifest"
grep -Fqx '      - 192.168.13.5' "$manifest"
grep -Fqx '  ingressClassName: higress' "$manifest"
grep -Fqx '                name: llm-d-sim' "$manifest"
