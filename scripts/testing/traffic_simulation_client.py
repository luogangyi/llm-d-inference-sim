#!/usr/bin/env python3
"""Exercise simulator HTTP profiles without external Python dependencies."""

import argparse
import concurrent.futures
import json
import statistics
import sys
import time
import urllib.error
import urllib.request


def request(base_url, path, method="GET", payload=None, headers=None, timeout=30):
    body = None if payload is None else json.dumps(payload).encode("utf-8")
    all_headers = headers.copy() if headers else {}
    if body is not None:
        all_headers.setdefault("Content-Type", "application/json")
    req = urllib.request.Request(base_url + path, data=body, headers=all_headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as response:
            return response.status, response.headers, response.read()
    except urllib.error.HTTPError as error:
        return error.code, error.headers, error.read()


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def json_body(body, context):
    try:
        return json.loads(body)
    except json.JSONDecodeError as error:
        raise AssertionError(f"{context}: invalid JSON: {error}") from error


def assert_openai_response(body, context):
    payload = json_body(body, context)
    require(payload.get("choices"), f"{context}: choices is empty")
    usage = payload.get("usage")
    require(usage is not None, f"{context}: usage is missing")
    require(usage["total_tokens"] == usage["prompt_tokens"] + usage["completion_tokens"],
            f"{context}: usage totals are inconsistent")


def assert_stream(body, context, expect_done=True):
    text = body.decode("utf-8")
    require("data: " in text, f"{context}: SSE data frame is missing")
    if expect_done:
        require("data: [DONE]" in text, f"{context}: SSE done marker is missing")
    else:
        require("data: [DONE]" not in text, f"{context}: unexpected SSE done marker")
    return text


def functional(base_url, model, native):
    for path in ("/health", "/health/ready", "/v1/models", "/metrics"):
        status, _, _ = request(base_url, path)
        require(status == 200, f"{path}: expected 200, got {status}")

    chat = {
        "model": model,
        "messages": [{"role": "user", "content": "Explain a request queue in one sentence."}],
        "max_tokens": 2,
    }
    status, _, body = request(base_url, "/v1/chat/completions", "POST", chat)
    require(status == 200, f"chat completion: expected 200, got {status}: {body.decode('utf-8')}")
    assert_openai_response(body, "chat completion")

    stream = {
        "model": model,
        "prompt": "Complete this simulator request.",
        "max_tokens": 2,
        "stream": True,
        "stream_options": {"include_usage": True},
    }
    status, _, body = request(base_url, "/v1/completions", "POST", stream)
    require(status == 200, f"stream completion: expected 200, got {status}")
    stream_text = assert_stream(body, "stream completion")
    require('"usage"' in stream_text, "stream completion: usage frame is missing")

    embeddings = {"model": model, "input": ["queue", "cache"]}
    status, _, body = request(base_url, "/v1/embeddings", "POST", embeddings)
    require(status == 200, f"embeddings: expected 200, got {status}")
    embedding_payload = json_body(body, "embeddings")
    require(len(embedding_payload.get("data", [])) == 2, "embeddings: expected two vectors")

    if native:
        generate = {"text": "Explain a queue.", "sampling_params": {"max_new_tokens": 2}}
        status, _, body = request(base_url, "/generate", "POST", generate)
        require(status == 200, f"native generate: expected 200, got {status}")
        require("text" in json_body(body, "native generate"), "native generate: text is missing")

        generate["stream"] = True
        status, _, body = request(base_url, "/generate", "POST", generate)
        require(status == 200, f"native stream generate: expected 200, got {status}")
        require('"text"' in assert_stream(body, "native stream generate"), "native stream: text is missing")

        for path in ("/model_info", "/get_model_info", "/server_info", "/health_generate"):
            status, _, _ = request(base_url, path)
            require(status == 200, f"{path}: expected 200, got {status}")


def concurrency(base_url, model, concurrency, requests_per_worker):
    payload = {
        "model": model,
        "prompt": "Measure concurrent simulator requests.",
        "max_tokens": 1,
    }

    def one_request(_):
        started = time.monotonic()
        status, _, body = request(base_url, "/v1/completions", "POST", payload)
        elapsed = time.monotonic() - started
        if status != 200:
            raise AssertionError(f"concurrency request: expected 200, got {status}: {body.decode('utf-8')}")
        assert_openai_response(body, "concurrency request")
        return elapsed

    total = concurrency * requests_per_worker
    started = time.monotonic()
    with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as executor:
        latencies = list(executor.map(one_request, range(total)))
    elapsed = time.monotonic() - started
    ordered = sorted(latencies)
    p95_index = max(0, int(len(ordered) * 0.95) - 1)
    return {
        "requests": total,
        "concurrency": concurrency,
        "elapsed_seconds": round(elapsed, 6),
        "requests_per_second": round(total / elapsed, 3),
        "p50_seconds": round(statistics.median(ordered), 6),
        "p95_seconds": round(ordered[p95_index], 6),
    }


def faults(base_url, model):
    update = {"traffic-simulation": {"enable-test-controls": True}}
    status, _, body = request(base_url, "/admin/config", "POST", update)
    require(status == 200, f"enable test controls: expected 200, got {status}: {body.decode('utf-8')}")

    stream = {
        "model": model,
        "prompt": "Inject a deterministic stream fault.",
        "max_tokens": 3,
        "stream": True,
        "stream_options": {"include_usage": True},
    }
    headers = {"X-Mock-Disconnect-After-Chunks": "2"}
    status, _, body = request(base_url, "/v1/completions", "POST", stream, headers)
    require(status == 200, f"disconnect fault: expected 200, got {status}")
    assert_stream(body, "disconnect fault", expect_done=False)

    headers = {"X-Mock-Omit-Done": "true"}
    status, _, body = request(base_url, "/v1/completions", "POST", stream, headers)
    require(status == 200, f"omit done fault: expected 200, got {status}")
    assert_stream(body, "omit done fault", expect_done=False)

    status, _, body = request(base_url, "/metrics")
    require(status == 200, f"metrics after faults: expected 200, got {status}")
    metrics = body.decode("utf-8")
    require('type="disconnect"' in metrics, "disconnect metric is missing")
    require('type="omit_done"' in metrics, "omit_done metric is missing")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--model", required=True)
    parser.add_argument("--suite", choices=("functional", "concurrency", "faults"), required=True)
    parser.add_argument("--native", action="store_true")
    parser.add_argument("--concurrency", type=int, default=20)
    parser.add_argument("--requests-per-worker", type=int, default=2)
    args = parser.parse_args()

    if args.suite == "functional":
        functional(args.base_url, args.model, args.native)
        result = {"suite": args.suite, "status": "passed", "native": args.native}
    elif args.suite == "concurrency":
        result = concurrency(args.base_url, args.model, args.concurrency, args.requests_per_worker)
        result.update({"suite": args.suite, "status": "passed", "native": args.native})
    else:
        faults(args.base_url, args.model)
        result = {"suite": args.suite, "status": "passed", "native": args.native}
    print(json.dumps(result, sort_keys=True))


if __name__ == "__main__":
    try:
        main()
    except (AssertionError, urllib.error.URLError, TimeoutError) as error:
        print(f"FAILED: {error}", file=sys.stderr)
        sys.exit(1)
