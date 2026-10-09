"""Public CLI/lifecycle coverage for the archive measurement modes.

The tests own two contracts: the normal command keeps its historical defaults,
and the hosted-fake command performs one measurement while restoring the local
stack even when that measurement cannot start. External helpers are fakes; the
tests observe the report and stack lifecycle rather than helper call shapes.
"""

import contextlib
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import types
import unittest
from unittest import mock
from urllib.parse import parse_qs, urlsplit

import measure_archive


class Startup(unittest.TestCase):
    def test_help_starts_from_outside_the_repository_without_pythonpath(self):
        script = pathlib.Path(__file__).resolve().with_name("measure_archive.py")
        with tempfile.TemporaryDirectory() as folder:
            result = subprocess.run([sys.executable, str(script), "--help"], cwd=folder,
                                    env={**os.environ, "PYTHONPATH": ""}, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("--hosted-fake", result.stdout)


class _Response:
    status = 200
    headers = {"Content-Type": "application/json"}

    def __init__(self, payload):
        self._body = json.dumps(payload).encode()

    def read(self, *_args):
        return self._body

    def __enter__(self):
        return self

    def __exit__(self, *_args):
        return False


class _ArchiveAPI:
    def __init__(self, fail_on_corpus=False):
        self.fail_on_corpus = fail_on_corpus
        self.connector = "connector-1"

    def __call__(self, request, timeout=30):
        path = urlsplit(request.full_url).path
        query = parse_qs(urlsplit(request.full_url).query)
        if path == "/v0/corpora" and request.method == "POST":
            if self.fail_on_corpus:
                raise OSError("synthetic API unavailable")
            return _Response({"corpus_id": "corpus-1"})
        if path == "/v0/changes" and "cursor" not in query:
            return _Response({"next_cursor": "cursor-1"})
        if path == "/v0/changes" and "cursor" in query:
            events = [
                {"type": "record.accepted", "event_id": "event-1", "resource": {"id": "record-1"}},
                {"type": "record.accepted", "event_id": "event-2", "resource": {"id": "record-2"}},
                {"type": "record.materialized", "resource": {"id": "record-1"}},
                {"type": "record.retrieval_ready", "resource": {"id": "record-1"}},
                {"type": "record.enrichment_available", "resource": {"id": "record-1"}},
            ]
            return _Response({"items": events, "next_cursor": "cursor-2", "has_more": False})
        if path.startswith("/v0/admin/stats/"):
            return _Response({"items": []})
        if path == "/v0/connectors" and request.method == "POST":
            return _Response({"connector_id": self.connector})
        if path == f"/v0/connectors/{self.connector}" and request.method == "GET":
            return _Response({"health": {"diagnostics": {"members_done": 2}}})
        if path == f"/v0/connectors/{self.connector}/disable" and request.method == "POST":
            return _Response({})
        raise AssertionError(f"unexpected measurement request: {request.method} {path}")


class _Stack:
    def __init__(self, directory):
        self.directory = directory
        self.state = {"demo": "token", "api_port": 1234}
        self.events = []

    def stop_processes(self):
        self.events.append("stop")

    def start_processes(self):
        self.events.append("start")


class _Plugin:
    pid = 1234


class _Fake:
    url = "http://127.0.0.1:43210"

    def __init__(self):
        self.closed = False

    def close(self):
        self.closed = True


class MeasureArchiveCLITest(unittest.TestCase):
    def setUp(self):
        self.tempdir = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self.tempdir.name)
        (self.root / ".scratch" / "demo").mkdir(parents=True)
        self.stack = _Stack(self.root / ".scratch" / "demo")
        self.original = {
            "config.json": '{"plugins": [{"manifest": "base"}], "s3": {"endpoint": "http://source"}}\n',
            "worker.json": '{"worker": {"concurrency": 2}}\n',
        }
        for name, value in self.original.items():
            path = self.stack.directory / name
            path.write_text(value)
            path.chmod(0o640)
        self.modes = {name: (self.stack.directory / name).stat().st_mode & 0o777 for name in self.original}

    def tearDown(self):
        self.tempdir.cleanup()

    def _seed(self, stack, bucket, count, marker):
        path = stack.directory / "archive-source.json"
        path.write_text(json.dumps({
            "config": {"bucket": bucket, "media_type": "text/plain"},
            "credential": {"access_key_id": "fixture-access", "secret_access_key": "fixture-secret"},
            "members_total": 2,
        }) + "\n")
        return path

    def _common_patches(self, api, hosted=False):
        patches = [
            mock.patch.object(measure_archive, "__file__", str(self.root / "scripts" / "measure_archive.py")),
            mock.patch.object(measure_archive, "Stack", return_value=self.stack),
            mock.patch.object(measure_archive, "seed", side_effect=self._seed),
            mock.patch.object(measure_archive.urllib.request, "urlopen", side_effect=api),
        ]
        if hosted:
            self.fake = _Fake()
            self.proxy = mock.Mock(server_port=43211)
            hosted_embed = types.SimpleNamespace(build=mock.Mock(return_value=self.root / "hosted-embed"))
            plugin_environment = types.SimpleNamespace(inherited=mock.Mock(return_value={}))
            ports = types.SimpleNamespace(allocate=mock.Mock(return_value=43212))
            ingestion_plugin = types.SimpleNamespace(
                await_healthy=mock.Mock(),
                stop_plugin=mock.Mock(),
            )
            subprocess = types.SimpleNamespace(Popen=mock.Mock(return_value=_Plugin()))

            def start_proxy(_fake, _latency_ms, log_path):
                log_path.parent.mkdir(parents=True, exist_ok=True)
                log_path.write_text("[]")
                return self.proxy, object()

            patches.extend([
                mock.patch.object(measure_archive, "_running", return_value=True, create=True),
                mock.patch.object(measure_archive, "hosted_embed_plugin", hosted_embed, create=True),
                mock.patch.object(measure_archive, "Fake", return_value=self.fake, create=True),
                mock.patch.object(measure_archive, "_start_proxy", side_effect=start_proxy, create=True),
                mock.patch.object(measure_archive, "_stop_proxy", create=True),
                mock.patch.object(measure_archive, "_package_with_optional_batch_wait",
                                  return_value=(self.root / "quivr-plugin.yaml", {"provider": "fake"}, "hosted.embed/default"),
                                  create=True),
                mock.patch.object(measure_archive, "plugin_environment", plugin_environment, create=True),
                mock.patch.object(measure_archive, "ports", ports, create=True),
                mock.patch.object(measure_archive, "ingestion_plugin", ingestion_plugin, create=True),
                mock.patch.object(measure_archive, "subprocess", subprocess, create=True),
            ])
        return contextlib.ExitStack(), patches

    def _run(self, argv, api, hosted=False):
        stack_context, patches = self._common_patches(api, hosted=hosted)
        with stack_context:
            for patcher in patches:
                stack_context.enter_context(patcher)
            return measure_archive.main(argv)

    def test_base_cli_keeps_existing_measurement_defaults(self):
        report = self._run(["--stack", "demo", "--items", "2"], _ArchiveAPI())

        self.assertEqual(report["status"], "complete")
        self.assertEqual(report["batch_size"], 100)
        self.assertEqual(report["concurrency"], 8)
        self.assertEqual(report["timeout_seconds"], 600)
        self.assertNotIn("hosted_fake", report)

    def test_hosted_fake_is_one_measurement_and_restores_stack(self):
        report = self._run([
            "--hosted-fake", "--stack", "demo", "--items", "2", "--provider-latency-ms", "7", "--batch-wait-ms", "0",
        ], _ArchiveAPI(), hosted=True)

        self.assertEqual(report["status"], "complete")
        self.assertEqual(report["batch_size"], 500)
        self.assertEqual(report["concurrency"], 32)
        self.assertEqual(report["timeout_seconds"], 1200)
        self.assertEqual(report["hosted_fake"], {"provider_latency_ms": 7, "batch_wait_ms": 0})
        self.assertEqual(report["provider_log"]["requests"], 0)
        self.assertEqual(len(list(self.stack.directory.glob("archive-measure-*.json"))), 1)
        self.assertEqual(self.stack.events, ["stop", "start", "stop", "start"])
        for name, value in self.original.items():
            path = self.stack.directory / name
            self.assertEqual(path.read_text(), value)
            self.assertEqual(path.stat().st_mode & 0o777, self.modes[name])

    def test_hosted_fake_restores_configuration_when_measurement_fails(self):
        with self.assertRaisesRegex(RuntimeError, "Archive measurement API POST failed"):
            self._run(["--hosted-fake", "--stack", "demo", "--items", "2"], _ArchiveAPI(fail_on_corpus=True), hosted=True)

        for name, value in self.original.items():
            path = self.stack.directory / name
            self.assertEqual(path.read_text(), value)
            self.assertEqual(path.stat().st_mode & 0o777, self.modes[name])
        self.assertEqual(self.stack.events, ["stop", "start", "stop", "start"])


if __name__ == "__main__":
    unittest.main()
