#!/usr/bin/env python3
"""Unit and local end-to-end tests for higress_ttft_benchmark.py."""

import json
import os
import subprocess
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


SCRIPT = Path(__file__).with_name("higress_ttft_benchmark.py")
sys.path.insert(0, str(SCRIPT.parent))
import higress_ttft_benchmark as benchmark  # noqa: E402


class UnitTests(unittest.TestCase):
    def test_percentile_interpolates(self):
        self.assertEqual(benchmark.percentile([1, 2, 3, 4], 50), 2.5)

    def test_parse_concurrencies_rejects_duplicates(self):
        with self.assertRaisesRegex(Exception, "unique"):
            benchmark.parse_concurrencies("1,1")

    def test_token_prompt_uses_calibrated_offset(self):
        args = type("Args", (), {"prompt_tokens": 100, "prompt": "ignored"})()
        self.assertEqual(benchmark.prompt_for_args(args), "a " * 95)


class MockHigressHandler(BaseHTTPRequestHandler):
    def do_POST(self):
        if self.path not in ("/v1/chat/completions?mode=stream", "/v1/messages"):
            self.send_error(404)
            return
        anthropic = self.path == "/v1/messages"
        if anthropic and self.headers.get("x-api-key") != "test-key":
            self.send_error(401)
            return
        if anthropic and self.headers.get("anthropic-version") != "2023-06-01":
            self.send_error(400)
            return
        if not anthropic and self.headers.get("Authorization") != "Bearer test-key":
            self.send_error(401)
            return
        length = int(self.headers["Content-Length"])
        request = json.loads(self.rfile.read(length))
        if not request["stream"] or request["messages"][0]["content"] != "tiny":
            self.send_error(400)
            return
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        if anthropic:
            self.wfile.write(b'event: content_block_delta\n')
            self.wfile.write(b'data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"OK"}}\n\n')
        else:
            self.wfile.write(b'data: {"choices":[{"delta":{"role":"assistant"}}]}\n\n')
            self.wfile.write(b'data: {"choices":[{"delta":{"content":"OK"}}]}\n\n')
        self.wfile.flush()

    def log_message(self, _format, *_args):
        pass


class EndToEndTest(unittest.TestCase):
    def test_collects_first_content_token_for_both_protocols(self):
        server = ThreadingHTTPServer(("127.0.0.1", 0), MockHigressHandler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(server.shutdown)
        self.addCleanup(server.server_close)
        with tempfile.TemporaryDirectory() as output_dir:
            environment = os.environ | {"TEST_HIGRESS_KEY": "test-key"}
            for protocol, path in (("openai", "/v1/chat/completions?mode=stream"), ("anthropic", "/v1/messages")):
                protocol_dir = Path(output_dir) / protocol
                command = [
                    sys.executable, str(SCRIPT),
                    "--endpoint", f"http://127.0.0.1:{server.server_port}{path}",
                    "--protocol", protocol,
                    "--api-key-env", "TEST_HIGRESS_KEY",
                    "--model", "test-model",
                    "--prompt", "tiny",
                    "--max-tokens", "16",
                    "--concurrency", "3",
                    "--output-dir", str(protocol_dir),
                ]
                completed = subprocess.run(command, env=environment, capture_output=True, text=True, check=False)
                self.assertEqual(completed.returncode, 0, completed.stderr)
                summary = json.loads((protocol_dir / "ttft-summary.json").read_text())
                result = summary["summaries"][0]
                self.assertEqual(result["successes"], 3)
                self.assertEqual(result["failures"], 0)
                self.assertIsNotNone(result["ttft_s"]["p50"])
            mixed_dir = Path(output_dir) / "mixed"
            command = [
                sys.executable, str(SCRIPT),
                "--protocol", "mixed",
                "--openai-endpoint", f"http://127.0.0.1:{server.server_port}/v1/chat/completions?mode=stream",
                "--anthropic-endpoint", f"http://127.0.0.1:{server.server_port}/v1/messages",
                "--api-key-env", "TEST_HIGRESS_KEY",
                "--model", "test-model",
                "--prompt", "tiny",
                "--max-tokens", "16",
                "--concurrency", "4",
                "--output-dir", str(mixed_dir),
            ]
            completed = subprocess.run(command, env=environment, capture_output=True, text=True, check=False)
            self.assertEqual(completed.returncode, 0, completed.stderr)
            result = json.loads((mixed_dir / "ttft-summary.json").read_text())["summaries"][0]
            self.assertEqual(result["protocols"]["openai"]["requests"], 2)
            self.assertEqual(result["protocols"]["anthropic"]["requests"], 2)
            self.assertEqual(result["successes"], 4)
            duration_dir = Path(output_dir) / "duration"
            command[-1] = str(duration_dir)
            command.extend(["--duration", "0.1"])
            completed = subprocess.run(command, env=environment, capture_output=True, text=True, check=False)
            self.assertEqual(completed.returncode, 0, completed.stderr)
            result = json.loads((duration_dir / "ttft-summary.json").read_text())["summaries"][0]
            self.assertEqual(result["duration_s"], 0.1)
            self.assertGreaterEqual(result["requests"], 4)
            self.assertEqual(result["successes"], result["requests"])
            self.assertLess(result["elapsed_s"], 0.5)


if __name__ == "__main__":
    unittest.main()
