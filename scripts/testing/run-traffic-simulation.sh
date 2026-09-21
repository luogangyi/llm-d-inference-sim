#!/usr/bin/env bash

# Runs one reproducible simulator HTTP test suite and writes its artifacts.
set -euo pipefail

root_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
suite=functional
profile=
port=18000
concurrency=20
requests_per_worker=2
artifacts_dir=

usage() {
  cat <<'EOF'
Usage: scripts/testing/run-traffic-simulation.sh --profile PATH [options]

Options:
  --suite functional|concurrency|faults  Test suite to run. Default: functional.
  --profile PATH                        Simulator YAML profile. Required.
  --port PORT                           Local simulator port. Default: 18000.
  --concurrency N                       Concurrent workers. Default: 20.
  --requests-per-worker N               Requests per worker. Default: 2.
  --artifacts-dir PATH                  Directory for logs and result JSON.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --suite) suite=$2; shift 2 ;;
    --profile) profile=$2; shift 2 ;;
    --port) port=$2; shift 2 ;;
    --concurrency) concurrency=$2; shift 2 ;;
    --requests-per-worker) requests_per_worker=$2; shift 2 ;;
    --artifacts-dir) artifacts_dir=$2; shift 2 ;;
    --help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [[ -z "$profile" ]]; then
  echo "--profile is required" >&2
  usage >&2
  exit 2
fi
if [[ ! -f "$profile" ]]; then
  echo "profile does not exist: $profile" >&2
  exit 2
fi
case "$suite" in functional|concurrency|faults) ;; *) echo "invalid suite: $suite" >&2; exit 2 ;; esac

if [[ -z "$artifacts_dir" ]]; then
  artifacts_dir="$root_dir/artifacts/traffic-simulation/$(date -u +%Y%m%dT%H%M%SZ)-$suite"
fi
mkdir -p "$artifacts_dir"

binary="$root_dir/bin/llm-d-inference-sim"
if [[ ! -x "$binary" ]]; then
  make -C "$root_dir" build
fi

model=$(awk '
  /^model:[[:space:]]*/ {sub(/^model:[[:space:]]*/, ""); print; exit}
' "$profile")
served_model=$(awk '
  /^served-model-name:[[:space:]]*$/ { in_models=1; next }
  in_models && /^[[:space:]]*-[[:space:]]*/ {
    sub(/^[[:space:]]*-[[:space:]]*/, "")
    print
    exit
  }
  in_models && /^[^[:space:]]/ { exit }
' "$profile")
if [[ -n "$served_model" ]]; then
  model=$served_model
fi
if [[ -z "$model" ]]; then
  echo "profile has no model: $profile" >&2
  exit 2
fi
native=false
if grep -Eq '^engine:[[:space:]]*sglang[[:space:]]*$' "$profile"; then
  native=true
fi

server_log="$artifacts_dir/server.log"
"$binary" --config "$profile" --port "$port" >"$server_log" 2>&1 &
server_pid=$!
cleanup() {
  kill "$server_pid" 2>/dev/null || true
  wait "$server_pid" 2>/dev/null || true
}
trap cleanup EXIT

base_url="http://127.0.0.1:$port"
for _ in $(seq 1 100); do
  if curl --fail --silent --show-error "$base_url/health/ready" >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done
if ! curl --fail --silent --show-error "$base_url/health/ready" >/dev/null; then
  echo "simulator did not become ready; see $server_log" >&2
  exit 1
fi

command=(python3 "$root_dir/scripts/testing/traffic_simulation_client.py"
  --base-url "$base_url" --model "$model" --suite "$suite"
  --concurrency "$concurrency" --requests-per-worker "$requests_per_worker")
if [[ "$native" == true ]]; then
  command+=(--native)
fi
"${command[@]}" | tee "$artifacts_dir/result.json"

curl --fail --silent "$base_url/metrics" >"$artifacts_dir/metrics.prom"
git -C "$root_dir" rev-parse HEAD >"$artifacts_dir/git_commit"
cp "$profile" "$artifacts_dir/profile.yaml"
printf '%s\n' "suite=$suite" "profile=$profile" "model=$model" "port=$port" "concurrency=$concurrency" \
  "requests_per_worker=$requests_per_worker" >"$artifacts_dir/run.properties"
echo "artifacts: $artifacts_dir"
