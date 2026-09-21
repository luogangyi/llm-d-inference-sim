#!/usr/bin/env bash

# Routes simulator traffic through Higress and records the client-side results.
set -euo pipefail

root_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
suite=all
profile=
node_ip=
port=18080
namespace=llm-sim-e2e
concurrency=20
requests_per_worker=2
artifacts_dir=
keep_resources=false
render_only=false

usage() {
  cat <<'EOF'
Usage: scripts/testing/run-higress-traffic-simulation.sh --profile PATH --node-ip IP [options]

Options:
  --suite functional|concurrency|faults|all  Test suite to run. Default: all.
  --profile PATH                           Simulator YAML profile. Required.
  --node-ip IP                             Kubernetes node address for the simulator. Required.
  --port PORT                              Simulator host port. Default: 18080.
  --namespace NAME                         Namespace for the Service and Ingress. Default: llm-sim-e2e.
  --concurrency N                          Concurrent workers. Default: 20.
  --requests-per-worker N                  Requests per worker. Default: 2.
  --artifacts-dir PATH                     Directory for logs and result JSON.
  --keep-resources                         Leave the Service, EndpointSlice and Ingress installed.
  --render-manifest                        Print the Kubernetes manifest and exit.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --suite) suite=$2; shift 2 ;;
    --profile) profile=$2; shift 2 ;;
    --node-ip) node_ip=$2; shift 2 ;;
    --port) port=$2; shift 2 ;;
    --namespace) namespace=$2; shift 2 ;;
    --concurrency) concurrency=$2; shift 2 ;;
    --requests-per-worker) requests_per_worker=$2; shift 2 ;;
    --artifacts-dir) artifacts_dir=$2; shift 2 ;;
    --keep-resources) keep_resources=true; shift ;;
    --render-manifest) render_only=true; shift ;;
    --help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [[ -z "$profile" || ! -f "$profile" ]]; then
  echo "--profile must name an existing profile" >&2
  exit 2
fi
if [[ ! "$node_ip" =~ ^[0-9]{1,3}(\.[0-9]{1,3}){3}$ ]]; then
  echo "--node-ip must be an IPv4 address" >&2
  exit 2
fi
case "$suite" in functional|concurrency|faults|all) ;; *) echo "invalid suite: $suite" >&2; exit 2 ;; esac

render_manifest() {
  cat <<EOF
apiVersion: v1
kind: Namespace
metadata:
  name: $namespace
---
apiVersion: v1
kind: Service
metadata:
  name: llm-d-sim
  namespace: $namespace
spec:
  ports:
    - name: http
      port: 80
      protocol: TCP
      targetPort: $port
---
apiVersion: discovery.k8s.io/v1
kind: EndpointSlice
metadata:
  name: llm-d-sim-host
  namespace: $namespace
  labels:
    kubernetes.io/service-name: llm-d-sim
addressType: IPv4
ports:
  - name: http
    port: $port
    protocol: TCP
endpoints:
  - addresses:
      - $node_ip
    conditions:
      ready: true
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: llm-d-sim
  namespace: $namespace
spec:
  ingressClassName: higress
  rules:
    - http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: llm-d-sim
                port:
                  number: 80
EOF
}

if [[ "$render_only" == true ]]; then
  render_manifest
  exit 0
fi

if [[ -z "$artifacts_dir" ]]; then
  artifacts_dir="$root_dir/artifacts/higress-traffic-simulation/$(date -u +%Y%m%dT%H%M%SZ)"
fi
mkdir -p "$artifacts_dir"

binary="$root_dir/bin/llm-d-inference-sim"
if [[ ! -x "$binary" ]]; then make -C "$root_dir" build; fi

model=$(awk '/^model:[[:space:]]*/ {sub(/^model:[[:space:]]*/, ""); print; exit}' "$profile")
served_model=$(awk '/^served-model-name:[[:space:]]*$/ { in_models=1; next } in_models && /^[[:space:]]*-[[:space:]]*/ { sub(/^[[:space:]]*-[[:space:]]*/, ""); print; exit } in_models && /^[^[:space:]]/ { exit }' "$profile")
if [[ -n "$served_model" ]]; then model=$served_model; fi
if [[ -z "$model" ]]; then echo "profile has no model: $profile" >&2; exit 2; fi

native=false
if grep -Eq '^engine:[[:space:]]*sglang[[:space:]]*$' "$profile"; then native=true; fi

manifest="$artifacts_dir/higress-resources.yaml"
render_manifest >"$manifest"
server_log="$artifacts_dir/server.log"
"$binary" --config "$profile" --port "$port" >"$server_log" 2>&1 &
server_pid=$!

cleanup() {
  kill "$server_pid" 2>/dev/null || true
  wait "$server_pid" 2>/dev/null || true
  if [[ "$keep_resources" != true ]]; then
    kubectl -n "$namespace" delete ingress/llm-d-sim service/llm-d-sim endpointslice/llm-d-sim-host --ignore-not-found >/dev/null || true
  fi
}
trap cleanup EXIT

for _ in $(seq 1 100); do
  if curl --fail --silent --show-error "http://127.0.0.1:$port/health/ready" >/dev/null 2>&1; then break; fi
  sleep 0.1
done
curl --fail --silent --show-error "http://127.0.0.1:$port/health/ready" >/dev/null

kubectl apply -f "$manifest"
gateway_port=$(kubectl -n higress-system get service higress-gateway -o jsonpath='{.spec.ports[?(@.port==80)].nodePort}')
if [[ -z "$gateway_port" ]]; then echo "Higress gateway service has no HTTP NodePort" >&2; exit 1; fi
base_url="http://$node_ip:$gateway_port"
for _ in $(seq 1 100); do
  if curl --fail --silent --show-error "$base_url/health/ready" >/dev/null 2>&1; then break; fi
  sleep 0.2
done
curl --fail --silent --show-error "$base_url/health/ready" >/dev/null

run_suite() {
  local selected_suite=$1
  local command=(python3 "$root_dir/scripts/testing/traffic_simulation_client.py" --base-url "$base_url" --model "$model" --suite "$selected_suite" --concurrency "$concurrency" --requests-per-worker "$requests_per_worker")
  if [[ "$native" == true ]]; then command+=(--native); fi
  "${command[@]}" | tee "$artifacts_dir/$selected_suite.json"
}

if [[ "$suite" == all ]]; then
  run_suite functional
  run_suite concurrency
  run_suite faults
else
  run_suite "$suite"
fi

curl --fail --silent "$base_url/metrics" >"$artifacts_dir/metrics.prom"
git -C "$root_dir" rev-parse HEAD >"$artifacts_dir/git_commit"
cp "$profile" "$artifacts_dir/profile.yaml"
printf '%s\n' "suite=$suite" "profile=$profile" "model=$model" "node_ip=$node_ip" "simulator_port=$port" "gateway_port=$gateway_port" "namespace=$namespace" "concurrency=$concurrency" "requests_per_worker=$requests_per_worker" >"$artifacts_dir/run.properties"
echo "artifacts: $artifacts_dir"
