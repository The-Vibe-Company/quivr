#!/usr/bin/env python3
"""Measure archive ingestion through hosted.embed and a loopback fake provider.

The provider proxy deliberately forwards only to the shared Go embedding fake.
It adds a deterministic delay and records request batch sizes without writing
request bodies, credentials or source text to evidence.
"""

from __future__ import annotations

import argparse
import datetime as dt
import http.server
import json
import os
import pathlib
import re
import stat
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


ROOT = pathlib.Path(__file__).resolve().parents[1]
SCRIPTS = ROOT / "scripts"
sys.path.insert(0, str(SCRIPTS))

from fake_api import Fake
import hosted_embed_plugin
import ingestion_plugin
import measure_archive
import plugin_environment
import ports
from local import Stack


UTC = dt.timezone.utc


def _iso_now() -> str:
    return dt.datetime.now(UTC).isoformat().replace("+00:00", "Z")


def _running(stack: Stack) -> bool:
    try:
        return stack.probe("probe_port") == 204 and stack.probe("worker_probe_port") == 204
    except (KeyError, OSError):
        return bool(stack.state.get("pids"))


def _json_config(raw: str) -> dict:
    return json.loads(raw)


def _write_json(path: pathlib.Path, value: dict) -> None:
    path.write_text(json.dumps(value, indent=2) + "\n")


def _package_with_optional_batch_wait(binary: pathlib.Path, directory: pathlib.Path, endpoint: str, batch_wait_ms: int | None):
    manifest, configuration, space = hosted_embed_plugin.package(binary, directory, endpoint, "cohere")
    if batch_wait_ms is None:
        return manifest, configuration, space
    configuration["batch_wait_ms"] = batch_wait_ms
    config_path = manifest.parent / "configuration.json"
    _write_json(config_path, configuration)
    with manifest.open("w") as output:
        subprocess.run([str(binary), "configure", str(config_path)], stdout=output, check=True)
    declaration = json.loads(manifest.read_text())
    space = next(iter(declaration["contributions"]["ingestion"]["spaces"]))
    return manifest, configuration, space


class _ProviderProxy(http.server.BaseHTTPRequestHandler):
    """Delay and forward provider requests to the per-run loopback fake."""

    server_version = "QuivrMeasurementProxy/1"

    def log_message(self, *_args):
        return

    def do_POST(self):  # noqa: N802 - BaseHTTPRequestHandler API
        started = time.perf_counter()
        status = 502
        body = b'{"error":"provider proxy failure"}'
        item_count = 0
        input_bytes = 0
        mode = ""
        try:
            length = int(self.headers.get("Content-Length", "0"))
            request_body = self.rfile.read(length)
            request = json.loads(request_body)
            values = request.get("texts", request.get("input", []))
            if not isinstance(values, list):
                raise ValueError("provider input is not a list")
            item_count = len(values)
            input_bytes = sum(len(str(value).encode()) for value in values)
            mode = request.get("input_type", "")
            time.sleep(self.server.provider_latency_ms / 1000)
            target = urllib.request.Request(
                self.server.fake_url + self.path,
                method="POST",
                data=request_body,
                headers={"Content-Type": "application/json", "api-key": "fake-key"},
            )
            with urllib.request.urlopen(target, timeout=30) as response:
                body = response.read()
                status = response.status
                content_type = response.headers.get("Content-Type", "application/json")
        except urllib.error.HTTPError as error:
            status = error.code
            content_type = "application/json"
            try:
                body = error.read()
            except OSError:
                body = b'{"error":"provider fake HTTP error"}'
        except Exception:
            content_type = "application/json"
        self.server.record({
            "ts": _iso_now(),
            "items": item_count,
            "bytes": input_bytes,
            "mode": mode,
            "latency_ms": round((time.perf_counter() - started) * 1000, 3),
            "status": status,
        })
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(body)
        self.close_connection = True


def _start_proxy(fake: Fake, latency_ms: int, log_path: pathlib.Path):
    fake_url = fake.url
    hostname = urllib.parse.urlsplit(fake_url).hostname
    if hostname not in {"127.0.0.1", "localhost", "::1"}:
        raise RuntimeError("embedding fake did not bind to loopback")
    log_path.parent.mkdir(parents=True, exist_ok=True)
    log_path.write_text("")
    lock = threading.Lock()

    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), _ProviderProxy)
    server.fake_url = fake_url
    server.provider_latency_ms = latency_ms
    server.log_path = log_path
    server.log_lock = lock

    def record(item):
        with lock:
            with log_path.open("a") as output:
                output.write(json.dumps(item, sort_keys=True) + "\n")
                output.flush()

    server.record = record
    thread = threading.Thread(target=server.serve_forever, name="archive-provider-proxy", daemon=True)
    thread.start()
    return server, thread


def _stop_proxy(server, thread):
    if server is None:
        return
    server.shutdown()
    server.server_close()
    if thread is not None:
        thread.join(timeout=10)


def _wrapper_evidence_path(directory: pathlib.Path, run: str) -> pathlib.Path:
    return directory / f"hosted-archive-measure-{run}.json"


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--stack", required=True, help="Running local stack name under .scratch/")
    parser.add_argument("--items", type=int, default=5000)
    parser.add_argument("--batch-size", type=int, default=500)
    parser.add_argument("--concurrency", type=int, default=32)
    parser.add_argument("--timeout", type=int, default=1200)
    parser.add_argument("--provider-latency-ms", type=int, default=500)
    parser.add_argument(
        "--batch-wait-ms",
        type=int,
        default=None,
        help="Optional hosted.embed document collection window (0..100 ms); omitted uses the plugin default",
    )
    args = parser.parse_args(argv)
    if (
        not re.fullmatch(r"[a-z0-9-]+", args.stack)
        or args.items < 1
        or not 1 <= args.batch_size <= 1000
        or not 1 <= args.concurrency <= 32
        or args.timeout < 1
        or args.provider_latency_ms < 0
        or args.batch_wait_ms is not None
        and not 0 <= args.batch_wait_ms <= 100
    ):
        parser.error("invalid stack, item count, batch size, concurrency, timeout, provider latency or batch wait")

    stack = Stack(args.stack)
    if not (stack.directory / "config.json").exists():
        parser.error("start the named local stack first")
    if not _running(stack):
        parser.error("the named local stack must be running")

    run = uuid.uuid4().hex[:12]
    directory = stack.directory / "measure-hosted"
    directory.mkdir(parents=True, exist_ok=True)
    provider_log = directory / f"provider-calls-{run}.jsonl"
    plugin_log = directory / f"plugin-{run}.log"
    original = {}
    config_names = ("config.json", "worker.json")
    for name in config_names:
        path = stack.directory / name
        original[name] = {"text": path.read_text(), "mode": stat.S_IMODE(path.stat().st_mode)}

    binary = None
    manifest = None
    plugin = None
    fake = None
    proxy = None
    proxy_thread = None
    stack_was_stopped = False
    temporary_processes_started = False
    configs_modified = False
    measurement = None
    measurement_error = None
    cleanup_errors = []
    run_started_epoch = time.time()
    evidence = {
        "run_id": run,
        "stack": args.stack,
        "started_at": _iso_now(),
        "provider_latency_ms": args.provider_latency_ms,
        "batch_wait_ms": args.batch_wait_ms,
        "items": args.items,
        "batch_size": args.batch_size,
        "concurrency": args.concurrency,
        "timeout_seconds": args.timeout,
        "provider_log": str(provider_log.resolve()),
        "plugin_log": str(plugin_log.resolve()),
        "status": "started",
    }
    try:
        # This helper is deliberately the repository's shared Go fake. No
        # provider address from outside loopback is accepted below.
        binary = hosted_embed_plugin.build(directory)
        fake = Fake("embedding")
        proxy, proxy_thread = _start_proxy(fake, args.provider_latency_ms, provider_log)
        endpoint = f"http://127.0.0.1:{proxy.server_port}"
        manifest, configuration, space = _package_with_optional_batch_wait(
            binary, directory / f"pin-{run}", endpoint, args.batch_wait_ms
        )
        evidence["manifest_path"] = str(manifest.resolve())
        evidence["plugin_binary_path"] = str(binary.resolve())
        evidence["provider_proxy"] = "loopback proxy forwarding only to the shared Go embedding fake"
        env = {
            **plugin_environment.inherited(),
            "QUIVR_PLUGIN_HOST": "127.0.0.1",
            "QUIVR_PLUGIN_PORT": str(ports.allocate()),
            "QUIVR_PLUGIN_MANIFEST": str(manifest),
            "AZURE_FOUNDRY_KEY": "fake-key",
        }
        with plugin_log.open("w") as output:
            plugin = subprocess.Popen(
                [str(binary)],
                env=env,
                stdout=output,
                stderr=output,
                start_new_session=True,
            )
        ingestion_plugin.await_healthy(plugin, int(env["QUIVR_PLUGIN_PORT"]), plugin_log)

        # Mark this before the first write so a partial configuration update is
        # restored if the second file cannot be written.
        configs_modified = True
        for name, saved in original.items():
            config = _json_config(saved["text"])
            config["ingestion"] = {"default": "hosted.embed"}
            config.setdefault("plugins", []).append({
                "manifest": str(manifest),
                "endpoint": f"http://127.0.0.1:{env['QUIVR_PLUGIN_PORT']}",
                "configuration": configuration,
                "spaces": {space: "served"},
            })
            path = stack.directory / name
            _write_json(path, config)
            path.chmod(0o600)
        stack.stop_processes()
        stack_was_stopped = True
        stack.start_processes()
        temporary_processes_started = True
        measurement_args = [
            "--stack", args.stack,
            "--items", str(args.items),
            "--batch-size", str(args.batch_size),
            "--concurrency", str(args.concurrency),
            "--timeout", str(args.timeout),
            "--provider-log", str(provider_log),
        ]
        measurement = measure_archive.main(measurement_args)
        evidence["measurement_status"] = measurement.get("status") if measurement else "unknown"
    except BaseException as error:
        measurement_error = error
        evidence["status"] = "failed"
        evidence["error"] = type(error).__name__
    finally:
        if plugin is not None:
            try:
                ingestion_plugin.stop_plugin(plugin)
            except BaseException as error:
                cleanup_errors.append(f"plugin cleanup: {type(error).__name__}")
        try:
            _stop_proxy(proxy, proxy_thread)
        except BaseException as error:
            cleanup_errors.append(f"provider proxy cleanup: {type(error).__name__}")
        if stack_was_stopped or temporary_processes_started or configs_modified:
            try:
                stack.stop_processes()
            except BaseException as error:
                cleanup_errors.append(f"temporary stack stop: {type(error).__name__}")
            for name, saved in original.items():
                try:
                    path = stack.directory / name
                    path.write_text(saved["text"])
                    path.chmod(saved["mode"])
                except BaseException as error:
                    cleanup_errors.append(f"restore {name}: {type(error).__name__}")
            try:
                stack.start_processes()
            except BaseException as error:
                cleanup_errors.append(f"restore stack start: {type(error).__name__}")
        if fake is not None:
            try:
                fake.close()
            except BaseException as error:
                cleanup_errors.append(f"embedding fake cleanup: {type(error).__name__}")

        evidence["finished_at"] = _iso_now()
        evidence["cleanup_errors"] = cleanup_errors
        if measurement_error is None and not cleanup_errors:
            evidence["status"] = "complete"
        elif measurement_error is None:
            evidence["status"] = "cleanup_failed"
        report_candidates = sorted(
            (path for path in stack.directory.glob("archive-measure-*.json") if path.stat().st_mtime >= run_started_epoch),
            key=lambda path: path.stat().st_mtime,
        )
        if report_candidates:
            evidence["measurement_report_path"] = str(report_candidates[-1].resolve())
        try:
            _write_json(_wrapper_evidence_path(directory, run), evidence)
        except OSError:
            pass

    if measurement_error is not None:
        raise measurement_error
    if cleanup_errors:
        raise RuntimeError("measurement cleanup failed: " + ", ".join(cleanup_errors))
    return measurement


if __name__ == "__main__":
    main()
