"""Observe API, durable workflow and plugin traces through a local OTLP receiver."""
import json

from fake_api import Fake


def verify(stack):
    # Run late in the core lane, so enabling tracing cannot affect timed scenarios.
    originals = {name: (stack.directory / name).read_bytes() for name in ("config.json", "worker.json")}
    stack.stop_processes()
    with Fake("otlp") as collector:
        try:
            for name, raw in originals.items():
                config = json.loads(raw)
                config["telemetry"] = {"endpoint": collector.url, "sampling_ratio": 1}
                # Privacy is part of this trace contract: this deployment opts out.
                config["observability"]["record_query_text"] = False
                (stack.directory / name).write_text(json.dumps(config))
            stack.start_processes()
            stack.tests("^TestTraceIngestionAndPluginSearch$", {"QUIVR_TEST_OTLP_URL": collector.url})
        finally:
            stack.stop_processes()
            for name, raw in originals.items():
                (stack.directory / name).write_bytes(raw)
    stack.start_processes()
