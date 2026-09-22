#!/usr/bin/env python3
"""Measure streaming time to first token through a Higress endpoint."""

import argparse
import concurrent.futures
import http.client
import json
import os
import sys
import threading
import time
from collections import Counter
from datetime import datetime, timezone
from pathlib import Path
from urllib.parse import urlsplit


def percentile(values, percentile_value):
    """Return a linearly interpolated percentile for a non-empty value list."""
    if not values:
        return None
    ordered = sorted(values)
    position = (len(ordered) - 1) * percentile_value / 100
    lower = int(position)
    upper = min(lower + 1, len(ordered) - 1)
    return ordered[lower] + (ordered[upper] - ordered[lower]) * (position - lower)


def parse_concurrencies(value):
    try:
        concurrencies = tuple(int(item) for item in value.split(","))
    except ValueError as error:
        raise argparse.ArgumentTypeError("concurrency values must be integers") from error
    if not concurrencies or any(item < 1 for item in concurrencies):
        raise argparse.ArgumentTypeError("concurrency values must be positive")
    if len(set(concurrencies)) != len(concurrencies):
        raise argparse.ArgumentTypeError("concurrency values must be unique")
    return concurrencies


def request_body(protocol, model, prompt, max_tokens):
    if protocol == "anthropic":
        return json.dumps(
            {
                "max_tokens": max_tokens,
                "model": model,
                "stream": True,
                "temperature": 1,
                "top_k": 5,
                "messages": [{"role": "user", "content": prompt}],
            },
            separators=(",", ":"),
        ).encode("utf-8")
    return json.dumps(
        {
            "max_tokens": max_tokens,
            "model": model,
            "stream": True,
            "temperature": 1,
            "top_k": 5,
            "topK": 5,
            "messages": [{"role": "user", "content": prompt}],
        },
        separators=(",", ":"),
    ).encode("utf-8")


def request_headers(protocol, api_key, body):
    headers = {
        "Content-Type": "application/json",
        "Accept": "text/event-stream",
        "Content-Length": str(len(body)),
    }
    if protocol == "anthropic":
        headers.update({"x-api-key": api_key, "anthropic-version": "2023-06-01"})
    else:
        headers["Authorization"] = "Bearer " + api_key
    return headers


def prompt_for_args(args):
    if args.prompt_tokens is None:
        return args.prompt
    if args.prompt_tokens < 5:
        raise ValueError("--prompt-tokens must be at least 5")
    # This target's tokenizer reports `a ` repeated N times as N + 5 tokens.
    return "a " * (args.prompt_tokens - 5)


class Endpoint:
    def __init__(self, value):
        parsed = urlsplit(value)
        if parsed.scheme not in ("http", "https") or not parsed.hostname:
            raise ValueError("--endpoint must be an absolute HTTP or HTTPS URL")
        self.secure = parsed.scheme == "https"
        self.host = parsed.hostname
        self.port = parsed.port or (443 if self.secure else 80)
        self.target = parsed.path or "/"
        if parsed.query:
            self.target += "?" + parsed.query

    def connection(self, timeout):
        connection_type = http.client.HTTPSConnection if self.secure else http.client.HTTPConnection
        return connection_type(self.host, self.port, timeout=timeout)


def first_content_token(endpoint, body, headers, barrier, timeout):
    try:
        barrier.wait(timeout=timeout)
        started = time.monotonic()
        connection = endpoint.connection(timeout)
        connection.request("POST", endpoint.target, body=body, headers=headers)
        response = connection.getresponse()
        ttfb = time.monotonic() - started
        if response.status != 200:
            detail = response.read(512).decode("utf-8", "replace").replace("\n", " ")
            connection.close()
            return {"ok": False, "error": f"HTTP {response.status} {detail}"}

        while True:
            line = response.fp.readline()
            if not line:
                connection.close()
                return {"ok": False, "error": "EOF before content"}
            if not line.startswith(b"data:"):
                continue
            event = line[5:].strip()
            if event == b"[DONE]":
                connection.close()
                return {"ok": False, "error": "DONE before content"}
            try:
                payload = json.loads(event)
            except json.JSONDecodeError:
                continue
            openai_content = any(
                choice.get("delta", {}).get("content") or choice.get("text")
                for choice in payload.get("choices", [])
            )
            anthropic_content = payload.get("delta", {}).get("text")
            if openai_content or anthropic_content:
                ttft = time.monotonic() - started
                connection.close()
                return {"ok": True, "ttfb_s": ttfb, "ttft_s": ttft}
    except Exception as error:  # HTTP client errors are recorded per request.
        return {"ok": False, "error": f"{type(error).__name__}: {str(error)[:400]}"}


def request_specs(protocol, endpoints, model, prompt, max_tokens, api_key, concurrency):
    protocols = ("openai", "anthropic") if protocol == "mixed" else (protocol,)
    counts = {name: 0 for name in protocols}
    if protocol == "mixed":
        counts["openai"] = (concurrency + 1) // 2
        counts["anthropic"] = concurrency // 2
    else:
        counts[protocol] = concurrency

    specs = []
    for name in protocols:
        body = request_body(name, model, prompt, max_tokens)
        headers = request_headers(name, api_key, body)
        specs.extend(
            {
                "body": body,
                "endpoint": endpoints[name],
                "headers": headers,
                "protocol": name,
            }
            for _ in range(counts[name])
        )
    return specs


def run_concurrency(specs, timeout):
    concurrency = len(specs)
    barrier = threading.Barrier(concurrency)
    started = time.monotonic()

    def run_request(spec):
        result = first_content_token(
            spec["endpoint"], spec["body"], spec["headers"], barrier, timeout
        )
        result["protocol"] = spec["protocol"]
        return result

    with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as executor:
        results = list(executor.map(run_request, specs))
    successes = [result for result in results if result["ok"]]
    failures = [result for result in results if not result["ok"]]
    ttft = [result["ttft_s"] for result in successes]
    ttfb = [result["ttfb_s"] for result in successes]
    protocols = {}
    for name in sorted({result["protocol"] for result in results}):
        protocol_results = [result for result in results if result["protocol"] == name]
        protocols[name] = {
            "requests": len(protocol_results),
            "successes": sum(result["ok"] for result in protocol_results),
            "failures": sum(not result["ok"] for result in protocol_results),
        }
    return {
        "concurrency": concurrency,
        "requests": concurrency,
        "successes": len(successes),
        "failures": len(failures),
        "elapsed_s": time.monotonic() - started,
        "protocols": protocols,
        "input_body_bytes": {
            name: len(next(spec["body"] for spec in specs if spec["protocol"] == name))
            for name in protocols
        },
        "ttft_s": {key: percentile(ttft, value) for key, value in (("p50", 50), ("p95", 95), ("p99", 99))},
        "ttfb_s": {key: percentile(ttfb, value) for key, value in (("p50", 50), ("p95", 95), ("p99", 99))},
        "errors": dict(Counter(f"{result['protocol']}: {result['error']}" for result in failures)),
    }


def self_test():
    assert parse_concurrencies("1,100,500") == (1, 100, 500)
    assert percentile([1, 2, 3, 4], 50) == 2.5
    assert percentile([], 95) is None
    body = json.loads(request_body("openai", "model", "hello", 16))
    assert body["stream"] is True
    assert body["messages"] == [{"role": "user", "content": "hello"}]
    anthropic_body = json.loads(request_body("anthropic", "model", "hello", 16))
    assert "topK" not in anthropic_body
    assert request_headers("anthropic", "key", b"{}")["x-api-key"] == "key"
    specs = request_specs(
        "mixed",
        {"openai": Endpoint("http://example.test/openai"), "anthropic": Endpoint("http://example.test/anthropic")},
        "model",
        "hello",
        16,
        "key",
        5,
    )
    assert [spec["protocol"] for spec in specs].count("openai") == 3
    assert [spec["protocol"] for spec in specs].count("anthropic") == 2
    print("self-test passed")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--endpoint", help="Full Higress endpoint URL for a single protocol")
    parser.add_argument("--openai-endpoint", help="OpenAI endpoint URL for --protocol mixed")
    parser.add_argument("--anthropic-endpoint", help="Anthropic endpoint URL for --protocol mixed")
    parser.add_argument("--protocol", choices=("openai", "anthropic", "mixed"), default="openai")
    parser.add_argument("--api-key-env", default="HIGRESS_API_KEY", help="Environment variable containing the API key")
    parser.add_argument("--model", required=False, help="Model name sent to Higress")
    parser.add_argument("--prompt", default="仅回复OK", help="Short prompt when --prompt-tokens is unset")
    parser.add_argument("--prompt-tokens", type=int, help="Generate this many input tokens using the calibrated a-space pattern")
    parser.add_argument("--max-tokens", type=int, default=16384)
    parser.add_argument("--concurrency", type=parse_concurrencies, default=(100, 500, 1000), help="Comma-separated concurrency levels")
    parser.add_argument("--timeout", type=float, default=180, help="Request and barrier timeout in seconds")
    parser.add_argument("--output-dir", help="Directory for JSON results")
    parser.add_argument("--self-test", action="store_true", help="Run local unit checks and exit")
    args = parser.parse_args()
    if args.self_test:
        self_test()
        return
    if not args.model:
        parser.error("--model is required")
    if args.protocol == "mixed":
        if not args.openai_endpoint or not args.anthropic_endpoint:
            parser.error("--openai-endpoint and --anthropic-endpoint are required for --protocol mixed")
        endpoints = {
            "openai": Endpoint(args.openai_endpoint),
            "anthropic": Endpoint(args.anthropic_endpoint),
        }
    elif not args.endpoint:
        parser.error("--endpoint is required for a single protocol")
    else:
        endpoints = {args.protocol: Endpoint(args.endpoint)}
    if args.max_tokens < 1:
        parser.error("--max-tokens must be positive")
    api_key = os.getenv(args.api_key_env)
    if not api_key:
        parser.error(f"environment variable {args.api_key_env} is required")

    prompt = prompt_for_args(args)
    output_dir = Path(args.output_dir or "artifacts/higress-ttft/" + datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ"))
    output_dir.mkdir(parents=True, exist_ok=True)
    (output_dir / "run.properties").write_text(
        "\n".join(
            (
                f"protocol={args.protocol}",
                f"model={args.model}",
                f"prompt_tokens={args.prompt_tokens if args.prompt_tokens is not None else 'short'}",
                f"max_tokens={args.max_tokens}",
                "concurrencies=" + ",".join(str(value) for value in args.concurrency),
                f"openai_endpoint={args.openai_endpoint or args.endpoint}",
                f"anthropic_endpoint={args.anthropic_endpoint or args.endpoint}",
            )
        ) + "\n",
        encoding="utf-8",
    )
    summaries = []
    for concurrency in args.concurrency:
        specs = request_specs(args.protocol, endpoints, args.model, prompt, args.max_tokens, api_key, concurrency)
        summary = run_concurrency(specs, args.timeout)
        (output_dir / f"ttft-{concurrency}-summary.json").write_text(json.dumps(summary, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(json.dumps(summary, ensure_ascii=False), flush=True)
        summaries.append(summary)
    (output_dir / "ttft-summary.json").write_text(json.dumps({"summaries": summaries}, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(f"artifacts: {output_dir}")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, AssertionError) as error:
        print(f"FAILED: {error}", file=sys.stderr)
        sys.exit(1)
