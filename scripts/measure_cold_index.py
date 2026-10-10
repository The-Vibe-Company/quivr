#!/usr/bin/env python3
"""Measure pinned Weaviate query profiles on a bounded, isolated fixture.

The fixture compares explicit RQ-8 ``rescoreLimit`` values of 20 and 0 using
the same seeded 768-dimensional vectors. Every mode and trial owns a fresh
data directory, and queries use ``weaviate-client==4.23.1``'s supported gRPC
transport with ``query_profile`` enabled. Results are observations from a
small local fixture; they are not scale or end-to-end latency claims.

On Linux, ``--pageout-owned-mappings`` requires root and pages out only the
owned store's persisted object/posting mappings; anonymous vector caches are
untouched. Without that option, active mappings can prevent cache eviction.

The script is intentionally opt-in and accepts an owned artifact directory;
it starts only the pinned local store needed for the measurement. It is not a
product startup service or a warm-up binary. An invocation with the reserved
measurement lane is:

    python3.12 scripts/measure_cold_index.py \
      --ports 54397,54398,54399,54400,54401,54402 \
      --collection QuivrColdIndexProofV1 --out .context/revised-measure-output \
      --objects 2000 --deadline-seconds 3600 --without-cgroup-limits \
      --lsm-access-strategy pread
"""

from __future__ import annotations

import argparse
import ctypes
import hashlib
import json
import mmap
import os
import platform
from pathlib import Path
import random
import re
import shutil
import signal
import socket
import struct
import subprocess
import sys
import time
import urllib.error
import urllib.request
import uuid


ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))

from deploy import infrastructure


RESERVED_PORTS = (54397, 54398, 54399, 54400, 54401, 54402)
RESERVED_COLLECTION = "QuivrColdIndexProofV1"
DIMENSIONS = 768
OBJECT_CAP = 100_000
DEFAULT_OBJECTS = 2_000
DATASET_SEED = 1_483
MODES = ("lexical", "semantic", "hybrid")
TRIALS = ("after-import", "restart-first-query", "file-cache-cold")
RESCORE_LIMITS = (20, 0)
QUERY_TEXT = "Harbor ferry"
VECTOR_NAME = "primary"
QUERY_LIMIT = 10
POSIX_FADV_DONTNEED = 4


class MeasurementError(RuntimeError):
    """An expected, controlled measurement failure."""


class MeasurementInterrupted(MeasurementError):
    """The bounded run was interrupted by its owner."""


def write_json(path: Path, value: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")


def relative_artifact(path: Path, root: Path) -> str:
    return str(path.resolve().relative_to(root.resolve()))


def remaining(deadline: float, minimum: float = 1.0) -> float:
    value = deadline - time.monotonic()
    if value < minimum:
        raise MeasurementError("measurement deadline exceeded")
    return value


def parse_ports(value: str) -> tuple[int, ...]:
    try:
        ports = tuple(int(part) for part in value.split(","))
    except ValueError as error:
        raise argparse.ArgumentTypeError("ports must be comma-separated integers") from error
    if len(ports) != 6 or len(set(ports)) != 6 or any(not 1024 <= port <= 65535 for port in ports):
        raise argparse.ArgumentTypeError("ports must be six distinct values between 1024 and 65535")
    return ports


def validate_collection(value: str) -> str:
    if not re.fullmatch(r"[A-Z][A-Za-z0-9_]{0,254}", value):
        raise argparse.ArgumentTypeError("collection must be an ASCII Weaviate class name")
    return value


def run_docker(deadline: float, *arguments: str, parse_json: bool = False) -> object:
    """Run an owned Docker operation without exposing command output."""

    try:
        result = subprocess.run(
            ["docker", *arguments],
            capture_output=True,
            text=True,
            timeout=max(1.0, min(60.0, remaining(deadline))),
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as error:
        raise MeasurementError("owned Docker operation failed") from error
    if result.returncode != 0:
        raise MeasurementError("owned Docker operation failed")
    if not parse_json:
        return result.stdout
    try:
        return json.loads(result.stdout)
    except (TypeError, ValueError) as error:
        raise MeasurementError("owned Docker operation returned invalid metadata") from error


class HttpAPI:
    def __init__(self, base_url: str, deadline: float):
        self.base_url = base_url.rstrip("/")
        self.deadline = deadline
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def request(self, method: str, path: str, body: object | None = None) -> object:
        payload = None if body is None else json.dumps(body, separators=(",", ":")).encode()
        request = urllib.request.Request(
            self.base_url + path,
            data=payload,
            method=method,
            headers={"Content-Type": "application/json"},
        )
        try:
            with self.opener.open(request, timeout=max(1.0, min(30.0, remaining(self.deadline)))) as response:
                raw = response.read(8 << 20)
        except (OSError, urllib.error.URLError, urllib.error.HTTPError) as error:
            raise MeasurementError("pinned store HTTP operation failed") from error
        if not raw:
            return None
        try:
            decoded = json.loads(raw)
        except (TypeError, ValueError) as error:
            raise MeasurementError("pinned store returned invalid JSON") from error
        if isinstance(decoded, dict) and decoded.get("errors"):
            raise MeasurementError("pinned store rejected an operation")
        return decoded

    def wait_ready(self, container_name: str, docker_deadline: float) -> None:
        while time.monotonic() < self.deadline:
            state = str(run_docker(docker_deadline, "inspect", "--format", "{{.State.Running}}", container_name)).strip()
            if state != "true":
                raise MeasurementError("owned pinned store exited before readiness")
            try:
                self.request("GET", "/v1/.well-known/ready")
                nodes = self.request("GET", "/v1/nodes")
                if not isinstance(nodes, dict) or not any(node.get("name") == container_name
                                                          for node in nodes.get("nodes", [])):
                    raise MeasurementError("HTTP endpoint does not belong to the owned fixture")
                return
            except MeasurementError:
                # Readiness is an observable condition in this measurement lane.
                time.sleep(0.1)
        raise MeasurementError("pinned store did not become ready before the run deadline")


def vector_for(index: int) -> list[float]:
    # Per-index seeding keeps the fixture reproducible without retaining all
    # objects in Python memory when --objects is raised toward its cap.
    generator = random.Random(DATASET_SEED + index * 1_000_003)
    return [generator.uniform(-1.0, 1.0) for _ in range(DIMENSIONS)]


def object_id(index: int) -> str:
    return str(uuid.uuid5(uuid.NAMESPACE_URL, f"quivr-cold-index-proof/{index}"))


def object_text(index: int) -> str:
    return f"Harbor ferry document {index} seed {index % 17}"


def vector_bytes(vector: list[float]) -> bytes:
    return struct.pack(f"<{DIMENSIONS}f", *vector)


def dataset_manifest(objects: int) -> dict[str, object]:
    digest = hashlib.sha256()
    query_vector = vector_for(0)
    for index in range(objects):
        digest.update(index.to_bytes(8, "little"))
        digest.update(object_id(index).encode())
        digest.update(b"\0")
        digest.update(object_text(index).encode())
        digest.update(b"\0")
        digest.update(vector_bytes(vector_for(index)))
    return {
        "objects": objects,
        "dimensions": DIMENSIONS,
        "seed": DATASET_SEED,
        "vector_name": VECTOR_NAME,
        "digest_sha256": digest.hexdigest(),
        "query_vector_sha256": hashlib.sha256(vector_bytes(query_vector)).hexdigest(),
    }


def schema_payload(collection: str, rescore_limit: int) -> dict[str, object]:
    return {
        "class": collection,
        "description": "Bounded generic query profile fixture",
        "properties": [
            {"name": "text", "dataType": ["text"], "indexSearchable": True}
        ],
        "invertedIndexConfig": {"stopwords": {"preset": "none"}},
        "vectorConfig": {
            VECTOR_NAME: {
                "vectorizer": {"none": None},
                "vectorIndexType": "hnsw",
                "vectorIndexConfig": {
                    "distance": "cosine",
                    "rq": {"enabled": True, "bits": 8, "rescoreLimit": rescore_limit},
                },
            }
        },
    }


def schema_readback(schema: object, expected_rescore: int) -> dict[str, object]:
    result: dict[str, object] = {
        "expected_rescore_limit": expected_rescore,
        "rescore_limit": {"present": False, "value": None, "reason": "field absent"},
        "vector_index_type": None,
        "rq_enabled": None,
        "rq_bits": None,
    }
    if not isinstance(schema, dict):
        result["rescore_limit"] = {"present": False, "value": None, "reason": "schema is not an object"}
        return result
    vectors = schema.get("vectorConfig")
    vector = vectors.get(VECTOR_NAME) if isinstance(vectors, dict) else None
    index_config = vector.get("vectorIndexConfig") if isinstance(vector, dict) else None
    rq = index_config.get("rq") if isinstance(index_config, dict) else None
    result["vector_index_type"] = vector.get("vectorIndexType") if isinstance(vector, dict) else None
    result["rq_enabled"] = rq.get("enabled") if isinstance(rq, dict) else None
    result["rq_bits"] = rq.get("bits") if isinstance(rq, dict) else None
    if isinstance(rq, dict) and "rescoreLimit" in rq:
        value = rq.get("rescoreLimit")
        if isinstance(value, bool) or not isinstance(value, (int, float)):
            result["rescore_limit"] = {"present": True, "value": value, "reason": "field is not numeric"}
        else:
            result["rescore_limit"] = {"present": True, "value": int(value), "reason": None}
    return result


def assert_schema_readback(readback: dict[str, object], expected: int) -> None:
    observed = readback.get("rescore_limit")
    value = observed.get("value") if isinstance(observed, dict) else None
    if (
        value != expected
        or readback.get("vector_index_type") != "hnsw"
        or readback.get("rq_enabled") is not True
        or readback.get("rq_bits") != 8
    ):
        raise MeasurementError("schema readback did not prove the requested RQ setting")


def import_fixture(api: HttpAPI, collection: str, objects: int, batch_size: int = 100) -> None:
    for start in range(0, objects, batch_size):
        batch: list[dict[str, object]] = []
        for index in range(start, min(objects, start + batch_size)):
            batch.append(
                {
                    "class": collection,
                    "id": object_id(index),
                    "properties": {"text": object_text(index)},
                    "vectors": {VECTOR_NAME: vector_for(index)},
                }
            )
        response = api.request("POST", "/v1/batch/objects", {"objects": batch})
        if isinstance(response, list):
            items = response
        elif isinstance(response, dict) and isinstance(response.get("objects"), list):
            # Keep the parser tolerant of a compatible wrapped response while
            # the pinned REST endpoint remains the list form.
            items = response["objects"]
        else:
            raise MeasurementError("fixture import returned an invalid response")
        if len(items) != len(batch):
            raise MeasurementError("fixture import response cardinality differs")
        expected = {item["id"] for item in batch}
        observed = set()
        for item in items:
            if not isinstance(item, dict):
                raise MeasurementError("fixture import result is not an object")
            identity = item.get("id")
            result = item.get("result")
            if (identity not in expected or identity in observed or not isinstance(result, dict)
                    or result.get("status") != "SUCCESS" or result.get("errors")):
                raise MeasurementError("fixture import identity or success status differs")
            observed.add(identity)


def directory_stats(path: Path) -> dict[str, int]:
    total = 0
    files = 0
    if path.exists():
        for child in path.rglob("*"):
            if child.is_file() and not child.is_symlink():
                try:
                    total += child.stat().st_size
                    files += 1
                except OSError:
                    continue
    return {"files": files, "bytes": total}


def owned_files(path: Path) -> list[Path]:
    root = path.resolve()
    result: list[Path] = []
    if not path.exists():
        return result
    for child in path.rglob("*"):
        if child.is_symlink() or not child.is_file():
            continue
        try:
            child.resolve().relative_to(root)
        except ValueError:
            continue
        result.append(child)
    return result


def file_residency(path: Path, files: list[Path] | None = None) -> dict[str, object]:
    """Best-effort mincore accounting restricted to the owned fixture tree."""

    page_size = int(os.sysconf("SC_PAGE_SIZE"))
    mincore = None
    try:
        libc = ctypes.CDLL(None, use_errno=True)
        mincore = libc.mincore
        mincore.argtypes = [ctypes.c_void_p, ctypes.c_size_t, ctypes.POINTER(ctypes.c_ubyte)]
        mincore.restype = ctypes.c_int
    except (AttributeError, OSError):
        mincore = None
    resident_bytes = 0
    resident_pages = 0
    mapped_files = 0
    errors = 0
    hidden_files = 0
    for child in owned_files(path) if files is None else files:
        try:
            stat = child.stat()
            # Linux masks mincore as all-resident without ownership or write
            # permission. Never interpret that security response as evidence.
            if stat.st_uid != os.geteuid() and not os.access(child, os.W_OK):
                hidden_files += 1
                continue
            size = stat.st_size
            if size <= 0:
                continue
            with child.open("rb") as handle:
                # ACCESS_COPY gives ctypes a writable buffer for the address;
                # no bytes are modified and the mapping remains private.
                with mmap.mmap(handle.fileno(), size, access=mmap.ACCESS_COPY) as mapping:
                    mapped_files += 1
                    if mincore is None:
                        continue
                    pages = (size + page_size - 1) // page_size
                    vector = (ctypes.c_ubyte * pages)()
                    address = ctypes.addressof(ctypes.c_char.from_buffer(mapping))
                    if mincore(address, size, vector) != 0:
                        errors += 1
                        continue
                    resident = sum(byte & 1 for byte in vector)
                    resident_pages += resident
                    resident_bytes += resident * page_size
        except (OSError, ValueError, BufferError):
            errors += 1
    return {
        "supported": mincore is not None and hidden_files == 0,
        "permission_hidden_files": hidden_files,
        "page_size": page_size,
        "mapped_files": mapped_files,
        "resident_pages": resident_pages,
        "resident_bytes": resident_bytes,
        "errors": errors,
    }


def bucket_evidence(path: Path) -> dict[str, object]:
    """Measure persisted object/posting segments, excluding replay logs."""
    result = {}
    for bucket in ("objects", "property_text_searchable"):
        files = [child for child in owned_files(path)
                 if child.suffix == ".db" and child.parent.name == bucket]
        result[bucket] = {
            "segments": [{"path": relative_artifact(child, path), "bytes": child.stat().st_size}
                         for child in files],
            "residency": file_residency(path, files),
        }
    return result


def pageout_owned_mappings(path: Path, container_name: str) -> dict[str, object]:
    """Page out only this fixture's object/posting maps; preserve anonymous caches."""
    pid = int(str(run_docker(time.monotonic() + 10, "inspect", "--format", "{{.State.Pid}}", container_name)).strip())
    descriptor = os.pidfd_open(pid)
    try:
        # Bind the process before reading address ranges; a recycled numeric PID
        # must never receive advice intended for the fixture process.
        current_pid = int(str(run_docker(time.monotonic() + 10, "inspect", "--format", "{{.State.Pid}}", container_name)).strip())
        if current_pid != pid:
            raise MeasurementError("owned fixture process changed before mapping inspection")
        ranges = []
        for line in Path(f"/proc/{pid}/maps").read_text().splitlines():
            fields = line.split()
            if len(fields) < 6 or not fields[-1].startswith("/var/lib/weaviate/"):
                continue
            relative = Path(fields[-1]).relative_to("/var/lib/weaviate")
            candidate = path / relative
            if (relative.parent.name not in ("objects", "property_text_searchable")
                    or relative.suffix != ".db" or candidate.is_symlink() or not candidate.is_file()):
                continue
            candidate.resolve().relative_to(path.resolve())
            if candidate.stat().st_ino != int(fields[4]):
                continue
            start, end = (int(value, 16) for value in fields[0].split("-"))
            ranges.append((start, end - start))
        if not ranges:
            raise MeasurementError("owned object/posting mappings are absent")
        class IOVec(ctypes.Structure):
            _fields_ = [("base", ctypes.c_void_p), ("length", ctypes.c_size_t)]
        vectors = (IOVec * len(ranges))(*(IOVec(start, length) for start, length in ranges))
        libc = ctypes.CDLL(None, use_errno=True)
        libc.syscall.restype = ctypes.c_long
        # Linux x86_64/aarch64: process_madvise=440, MADV_PAGEOUT=21.
        advised = libc.syscall(ctypes.c_long(440), ctypes.c_int(descriptor), ctypes.byref(vectors),
                               ctypes.c_ulong(len(ranges)), ctypes.c_int(21), ctypes.c_uint(0))
        error = ctypes.get_errno() if advised < 0 else 0
    finally:
        os.close(descriptor)
    return {"scope": "owned object/posting file mappings only", "mapped_ranges": len(ranges),
            "bytes_advised": advised, "errno": error, "anonymous_vector_cache_touched": False}


def evict_owned_file_cache(path: Path, container_name: str | None = None) -> dict[str, object]:
    """Issue DONTNEED only for files below the fixture directory."""

    attempted = 0
    succeeded = 0
    unsupported = 0
    errors = 0
    for child in owned_files(path):
        try:
            with child.open("rb") as handle:
                attempted += 1
                # DONTNEED skips dirty pages. Sync only this owned file so
                # eviction can establish cold reads without host-wide sync.
                os.fsync(handle.fileno())
                if hasattr(os, "posix_fadvise") and hasattr(os, "POSIX_FADV_DONTNEED"):
                    result = os.posix_fadvise(handle.fileno(), 0, 0, POSIX_FADV_DONTNEED)
                    # Python returns None on success on some libc builds and
                    # integer zero on others.
                    if result in (None, 0):
                        succeeded += 1
                    else:
                        errors += 1
                else:
                    unsupported += 1
        except OSError:
            errors += 1
    pageout = pageout_owned_mappings(path, container_name) if container_name else None
    return {
        "scope": "owned fixture files only",
        "pageout": pageout,
        "attempted_files": attempted,
        "succeeded_files": succeeded,
        "unsupported_files": unsupported,
        "errors": errors,
    }


def profile_to_dict(profile: object) -> dict[str, object] | None:
    if profile is None:
        return None
    shards: list[dict[str, object]] = []
    for shard in getattr(profile, "shards", []) or []:
        searches: dict[str, object] = {}
        for name, search in (getattr(shard, "searches", {}) or {}).items():
            searches[str(name)] = {"details": dict(getattr(search, "details", {}) or {})}
        shards.append(
            {
                "name": getattr(shard, "name", None),
                "node": getattr(shard, "node", None),
                "searches": searches,
            }
        )
    return {"shards": shards}


PHASE_EXPECTATIONS = {
    "hnsw_traversal": ("vector", "knn_search_layer_0_took"),
    "rescore": ("vector", "knn_search_rescore_took"),
    "object_fetch": ("*", "objects_took"),
    "keyword_scoring": ("keyword", "kwd_1_tok_time"),
}


def profile_summary(profile: dict[str, object] | None, mode: str) -> dict[str, object]:
    """Group server profile keys and make missing phases explicit."""

    details_by_search: list[tuple[str, dict[str, str]]] = []
    for shard in (profile or {}).get("shards", []) if profile else []:
        if not isinstance(shard, dict):
            continue
        searches = shard.get("searches", {})
        if not isinstance(searches, dict):
            continue
        for search_name, value in searches.items():
            details = value.get("details", {}) if isinstance(value, dict) else {}
            if isinstance(details, dict):
                details_by_search.append((str(search_name), {str(k): str(v) for k, v in details.items()}))

    result: dict[str, object] = {"profile_present": profile is not None, "phases": {}}
    for phase, (expected_search, expected_key) in PHASE_EXPECTATIONS.items():
        matches: dict[str, str] = {}
        search_names: set[str] = set()
        for search_name, details in details_by_search:
            if expected_search != "*" and search_name != expected_search:
                continue
            search_names.add(search_name)
            for key, value in details.items():
                if phase == "hnsw_traversal" and (
                    key.startswith("knn_search_layer_") or key == "hnsw_flat_search" or key.startswith("flat_search_")
                ):
                    matches[key] = value
                elif phase == "rescore" and "rescore" in key:
                    matches[key] = value
                elif phase == "object_fetch" and (
                    key == "objects_took" or key.startswith("objects_") or key == "objects_by_doc_ids_took" or key == "kwd_5_objects_time"
                ):
                    matches[key] = value
                elif phase == "keyword_scoring" and (
                    (key.startswith("kwd_") and key != "kwd_5_objects_time") or key.startswith("build_allow_list") or key.startswith("sort_")
                ):
                    matches[key] = value
        if matches:
            phase_value: dict[str, object] = {
                "present": True,
                "search_types": sorted(search_names),
                "fields": dict(sorted(matches.items())),
                "absent_fields": [],
                "reason": None,
            }
        else:
            if phase in ("hnsw_traversal", "rescore") and mode == "lexical":
                reason = "not applicable to lexical mode"
            elif phase == "keyword_scoring" and mode == "semantic":
                reason = "not applicable to semantic mode"
            elif profile is None:
                reason = "query_profile field absent from the gRPC response"
            else:
                reason = "server profile did not expose a matching field"
            phase_value = {
                "present": False,
                "search_types": sorted(search_names),
                "fields": {},
                "absent_fields": [expected_key],
                "reason": reason,
            }
        result["phases"][phase] = phase_value
    return result


def inspect_limits(deadline: float, container_name: str, without_cgroup_limits: bool) -> dict[str, object]:
    inspected = run_docker(deadline, "inspect", container_name, parse_json=True)
    item = inspected[0] if isinstance(inspected, list) and inspected else {}
    host_config = item.get("HostConfig", {}) if isinstance(item, dict) else {}
    memory = host_config.get("Memory") if isinstance(host_config, dict) else None
    nano_cpus = host_config.get("NanoCpus") if isinstance(host_config, dict) else None
    return {
        "without_cgroup_limits": without_cgroup_limits,
        "requested": None if without_cgroup_limits else {"memory": "2GiB", "cpus": 2},
        "observed": {
            "memory_bytes": int(memory) if isinstance(memory, (int, float)) else None,
            "nano_cpus": int(nano_cpus) if isinstance(nano_cpus, (int, float)) else None,
        },
        "go_heap_target": "1700MiB",
    }


class PinnedContainer:
    def __init__(
        self,
        image: str,
        ports: tuple[int, ...],
        data_dir: Path,
        name: str,
        without_cgroup_limits: bool,
        deadline: float,
        lsm_access_strategy: str = "mmap",
    ):
        self.image = image
        self.ports = ports
        self.data_dir = data_dir
        self.name = name
        self.without_cgroup_limits = without_cgroup_limits
        self.deadline = deadline
        self.started = False
        self.lsm_access_strategy = lsm_access_strategy

    def start(self) -> None:
        for port in self.ports:
            try:
                with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as probe:
                    probe.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
                    probe.bind(("0.0.0.0", port))
            except OSError as error:
                raise MeasurementError("fixture port is already occupied") from error
        self.data_dir.mkdir(parents=True, exist_ok=True)
        services = infrastructure.resolve(profile="small", environ={})
        env = dict(services["weaviate"].get("environment", {}))
        env.update(
            {
                "CLUSTER_HOSTNAME": self.name,
                "CLUSTER_ADVERTISE_ADDR": "127.0.0.1",
                "CLUSTER_ADVERTISE_PORT": str(self.ports[2]),
                "CLUSTER_GOSSIP_BIND_PORT": str(self.ports[2]),
                "CLUSTER_DATA_BIND_PORT": str(self.ports[3]),
                "GRPC_PORT": str(self.ports[1]),
                "RAFT_PORT": str(self.ports[4]),
                "RAFT_INTERNAL_RPC_PORT": str(self.ports[5]),
                "DISABLE_TELEMETRY": "true",
                # Fixture only: persist even tiny posting buckets on shutdown.
                "PERSISTENCE_MAX_REUSE_WAL_SIZE": "0",
                "PERSISTENCE_LSM_ACCESS_STRATEGY": self.lsm_access_strategy,
            }
        )
        arguments = ["run", "-d", "--name", self.name, "--network", "host",
                     "--user", f"{os.getuid()}:{os.getgid()}"]
        if not self.without_cgroup_limits:
            arguments += ["--memory", "2g", "--cpus", "2"]
        arguments += [
            "--mount",
            f"type=bind,src={self.data_dir.resolve()},dst=/var/lib/weaviate",
        ]
        for key, value in sorted(env.items()):
            arguments += ["-e", f"{key}={value}"]
        arguments += [
            self.image,
            "--host",
            "127.0.0.1",
            "--port",
            str(self.ports[0]),
            "--scheme",
            "http",
        ]
        try:
            run_docker(self.deadline, *arguments)
        except MeasurementError:
            # Docker can create a named container before a later start error;
            # keep cleanup ownership so the caller does not remove its mount
            # while that container might still exist.
            self.started = True
            raise
        self.started = True

    def flush_for_restart(self) -> None:
        # Docker can return success after killing a timed-out process. Inspect
        # its exit code so a crash/replay cannot masquerade as a durable flush.
        run_docker(self.deadline, "stop", "-t", "10", self.name)
        code = str(run_docker(self.deadline, "inspect", "--format", "{{.State.ExitCode}}", self.name)).strip()
        if code != "0":
            raise MeasurementError("fixture did not shut down gracefully for segment flush")
        run_docker(self.deadline, "rm", self.name)
        self.started = False

    def stop(self) -> bool:
        if not self.started:
            return True
        cleanup_deadline = time.monotonic() + 30.0
        removed = False
        try:
            # Flush the mounted store before a restart trial. A forced remove
            # would turn a restart observation into a crash-recovery test.
            try:
                run_docker(cleanup_deadline, "stop", "-t", "10", self.name)
            except MeasurementError:
                pass
            try:
                run_docker(cleanup_deadline, "rm", self.name)
            except MeasurementError:
                try:
                    run_docker(cleanup_deadline, "rm", "-f", self.name)
                except MeasurementError:
                    return False
            removed = True
            return True
        except MeasurementError:
            # Cleanup must not hide the original measured failure.
            return False
        finally:
            if removed:
                self.started = False


class GrpcQueryClient:
    def __init__(self, ports: tuple[int, ...], collection: str, deadline: float):
        self.ports = ports
        self.collection_name = collection
        self.deadline = deadline
        self.client = None
        self.collection = None

    def connect(self) -> None:
        try:
            import weaviate
            from weaviate.config import AdditionalConfig
            from weaviate.classes.query import MetadataQuery
        except (ImportError, ModuleNotFoundError) as error:
            raise MeasurementError(".context/venv lacks weaviate-client") from error
        self._metadata_query = MetadataQuery
        try:
            self.client = weaviate.connect_to_custom(
                http_host="127.0.0.1",
                http_port=self.ports[0],
                http_secure=False,
                grpc_host="127.0.0.1",
                grpc_port=self.ports[1],
                grpc_secure=False,
                additional_config=AdditionalConfig(timeout=(10, 120), trust_env=False),
                skip_init_checks=True,
            )
            self.collection = self.client.collections.get(self.collection_name)
        except Exception as error:
            raise MeasurementError("supported Weaviate gRPC client could not connect") from error

    def close(self) -> None:
        if self.client is not None:
            try:
                self.client.close()
            except Exception:
                pass
        self.client = None
        self.collection = None

    def query(self, mode: str, vector: list[float], with_profile: bool) -> dict[str, object]:
        if self.collection is None:
            raise MeasurementError("gRPC query client is not connected")
        metadata = self._metadata_query(query_profile=True) if with_profile else None
        started = time.perf_counter()
        try:
            if mode == "lexical":
                result = self.collection.query.bm25(
                    QUERY_TEXT,
                    query_properties=["text"],
                    limit=QUERY_LIMIT,
                    return_properties=["text"],
                    return_metadata=metadata,
                )
            elif mode == "semantic":
                result = self.collection.query.near_vector(
                    vector,
                    target_vector=VECTOR_NAME,
                    limit=QUERY_LIMIT,
                    return_properties=["text"],
                    return_metadata=metadata,
                )
            elif mode == "hybrid":
                result = self.collection.query.hybrid(
                    QUERY_TEXT,
                    alpha=0.5,
                    vector=vector,
                    query_properties=["text"],
                    target_vector=VECTOR_NAME,
                    limit=QUERY_LIMIT,
                    return_properties=["text"],
                    return_metadata=metadata,
                )
            else:
                raise MeasurementError("unsupported query mode")
        except Exception as error:
            raise MeasurementError("pinned store gRPC query failed") from error
        seconds = time.perf_counter() - started
        objects = list(getattr(result, "objects", []) or [])
        if not objects:
            raise MeasurementError("fixture query returned no objects")
        profile = profile_to_dict(getattr(result, "query_profile", None)) if with_profile else None
        if with_profile and profile is None:
            raise MeasurementError("query_profile was requested but absent from the gRPC response")
        return {
            "client_seconds": seconds,
            "hits": len(objects),
            "ids": [str(getattr(item, "uuid", "")) for item in objects],
            "profile": profile,
        }


def save_profile_artifacts(
    out: Path,
    trial_key: str,
    mode: str,
    query_result: dict[str, object],
    mode_metadata: dict[str, object],
    artifact_name: str | None = None,
) -> dict[str, object]:
    profile = query_result.get("profile")
    summary = profile_summary(profile if isinstance(profile, dict) else None, mode)
    raw_path = out / "raw" / trial_key / f"{artifact_name or mode}.json"
    write_json(
        raw_path,
        {
            "query": mode_metadata,
            "timing": {
                "client_seconds": query_result.get("client_seconds"),
                "hits": query_result.get("hits"),
            },
            "returned_ids": query_result.get("ids", []),
            "query_profile": profile,
        },
    )
    return {
        "client_seconds": query_result.get("client_seconds"),
        "hits": query_result.get("hits"),
        "raw_profile": relative_artifact(raw_path, out),
        "profile_summary": summary,
    }


def remove_owned_fixture(path: Path, out: Path) -> None:
    try:
        resolved = path.resolve()
        resolved.relative_to(out.resolve())
    except ValueError:
        return
    if not resolved.name.startswith("fixture-"):
        return
    if resolved.exists():
        shutil.rmtree(resolved)


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description=__doc__)
    result.add_argument(
        "--ports",
        type=parse_ports,
        default=RESERVED_PORTS,
        help="HTTP,gRPC,gossip,data,Raft,internal-Raft ports (the job uses reserved values)",
    )
    result.add_argument("--collection", type=validate_collection, default=RESERVED_COLLECTION, help="ASCII Weaviate class name")
    result.add_argument("--out", type=Path, required=True, help="owned artifact directory")
    result.add_argument("--objects", type=int, default=DEFAULT_OBJECTS)
    result.add_argument("--pageout-owned-mappings", action="store_true",
                        help="Linux root only: evict owned object/posting mappings, preserving anonymous vector cache")
    result.add_argument("--lsm-access-strategy", choices=("mmap", "pread"), default="mmap",
                        help="fixture-only LSM setting; verify actual mappings and eviction evidence")
    result.add_argument("--deadline-seconds", type=int, default=3600)
    result.add_argument(
        "--without-cgroup-limits",
        action="store_true",
        help="run without Docker memory/CPU controllers and record that limitation",
    )
    result.add_argument(
        "--keep-fixtures",
        action="store_true",
        help="retain owned per-trial data directories for follow-up inspection",
    )
    return result


def main(argv: list[str] | None = None) -> int:
    args = parser().parse_args(argv)
    if not 1 <= args.objects <= OBJECT_CAP:
        parser().error(f"--objects must be between 1 and {OBJECT_CAP}")
    if not 60 <= args.deadline_seconds <= 24 * 60 * 60:
        parser().error("--deadline-seconds must be between 60 and 86400")
    if args.pageout_owned_mappings and (os.geteuid() != 0 or platform.system() != "Linux"
                                       or platform.machine() not in ("x86_64", "aarch64")):
        parser().error("--pageout-owned-mappings requires Linux root on x86_64/aarch64")
    args.out = args.out.resolve()
    args.out.mkdir(parents=True, exist_ok=True)
    deadline = time.monotonic() + args.deadline_seconds

    report: dict[str, object] = {
        "kind": "cold-index-query-profile",
        "result": "running",
        "image": None,
        "collection": args.collection,
        "ports": list(args.ports),
        "dataset": None,
        "rescore_limits": list(RESCORE_LIMITS),
        "modes": list(MODES),
        "trials": list(TRIALS),
        "fixture_only_settings": {"PERSISTENCE_MAX_REUSE_WAL_SIZE": "0",
                                  "PERSISTENCE_LSM_ACCESS_STRATEGY": args.lsm_access_strategy,
                                  "pageout_owned_mappings": args.pageout_owned_mappings},
        "transport": {
            "client": "weaviate-client",
            "protocol": "gRPC",
            "query_profile_requested": True,
        },
        "resource_limits": {
            "without_cgroup_limits": args.without_cgroup_limits,
            "requested": None if args.without_cgroup_limits else {"memory": "2GiB", "cpus": 2},
            "go_heap_target": "1700MiB",
        },
        "trials_run": [],
        "limitations": [
            "Small bounded local fixture; observations make no scale or universal latency claim.",
            "Direct pinned-store timings exclude the Quivr engine and API layers.",
            "File-cache coldness is reported from owned-file eviction evidence and is not inferred from restart alone.",
            "Unprofiled warm timings in after-import and restart trials are directional because query_profile adds response work.",
        ],
        "scale_claim": None,
    }
    summary: dict[str, object] = {
        "kind": "cold-index-query-profile-summary",
        "result": "running",
        "rows": [],
        "limitations": report["limitations"],
        "scale_claim": None,
    }
    if args.lsm_access_strategy == "pread":
        report["limitations"].append("Fixture-only pread is requested; verify actual mapping/cache evidence. Deployment access strategy is unchanged.")
    active_container: PinnedContainer | None = None
    active_client: GrpcQueryClient | None = None
    current_trial_key: str | None = None
    cleanup_safe = True

    def interrupted(_signum: int, _frame: object) -> None:
        raise MeasurementInterrupted("measurement interrupted")

    previous_term = signal.signal(signal.SIGTERM, interrupted)
    previous_int = signal.signal(signal.SIGINT, interrupted)
    try:
        resolved = infrastructure.resolve(profile="small", environ={})
        image = resolved["weaviate"]["image"]
        if ":1.39.10@sha256:" not in image:
            raise MeasurementError("infrastructure did not resolve pinned Weaviate 1.39.10")
        report["image"] = image
        report["dataset"] = dataset_manifest(args.objects)

        for rescore_limit in RESCORE_LIMITS:
            for trial_name in TRIALS:
                for mode in MODES:
                    trial_key = f"rq{rescore_limit}-{trial_name}-{mode}"
                    current_trial_key = trial_key
                    remaining(deadline)
                    fixture_dir = args.out / f"fixture-rq{rescore_limit}-{trial_name}-{mode}"
                    if fixture_dir.exists():
                        remove_owned_fixture(fixture_dir, args.out)
                    fixture_dir.mkdir(parents=True, exist_ok=False)
                    container_name = "quivr-cold-index-" + uuid.uuid4().hex[:12]
                    active_container = PinnedContainer(
                        image,
                        args.ports,
                        fixture_dir,
                        container_name,
                        args.without_cgroup_limits,
                        deadline,
                        args.lsm_access_strategy,
                    )
                    trial: dict[str, object] = {
                        "key": trial_key,
                        "rescore_limit": rescore_limit,
                        "mode": mode,
                        "trial": trial_name,
                        "independent_state": True,
                        "state_setup": "fresh schema and seeded import in an owned directory",
                        "same_process_for_file_cache_trial": trial_name == "file-cache-cold",
                        "disk_bytes": None,
                        "resource_limits": None,
                        "queries": {},
                    }
                    trial_cleanup_failed = False
                    try:
                        active_container.start()
                        trial["resource_limits"] = inspect_limits(deadline, container_name, args.without_cgroup_limits)
                        api = HttpAPI(f"http://127.0.0.1:{args.ports[0]}", deadline)
                        api.wait_ready(container_name, deadline)
                        api.request("POST", "/v1/schema", schema_payload(args.collection, rescore_limit))
                        import_fixture(api, args.collection, args.objects)

                        query_vector = vector_for(0)
                        active_client = GrpcQueryClient(args.ports, args.collection, deadline)
                        active_client.connect()

                        if trial_name in ("restart-first-query", "file-cache-cold"):
                            active_client.close()
                            active_client = None
                            active_container.flush_for_restart()
                            if trial_name == "file-cache-cold":
                                persisted = bucket_evidence(fixture_dir)
                                if any(not item["segments"] for item in persisted.values()):
                                    raise MeasurementError("fixture lacks persisted object or posting segments")
                                trial["persisted_buckets"] = persisted
                                trial["state_setup"] += "; graceful flush/restart before warming, then same-process eviction"
                            active_container = None
                            active_container = PinnedContainer(
                                image,
                                args.ports,
                                fixture_dir,
                                container_name,
                                args.without_cgroup_limits,
                                deadline,
                                args.lsm_access_strategy,
                            )
                            active_container.start()
                            api = HttpAPI(f"http://127.0.0.1:{args.ports[0]}", deadline)
                            api.wait_ready(container_name, deadline)
                            active_client = GrpcQueryClient(args.ports, args.collection, deadline)
                            active_client.connect()

                        readback = api.request("GET", f"/v1/schema/{args.collection}")
                        readback_value = schema_readback(readback, rescore_limit)
                        assert_schema_readback(readback_value, rescore_limit)
                        schema_file = args.out / "raw" / trial_key / "schema-before-measurement.json"
                        write_json(schema_file, readback)
                        trial["schema_readback"] = readback_value
                        trial["schema_artifact"] = relative_artifact(schema_file, args.out)

                        mode_metadata = {
                            "mode": mode,
                            "trial": trial_name,
                            "rescore_limit": rescore_limit,
                            "collection": args.collection,
                            "vector_name": VECTOR_NAME,
                            "query_text": QUERY_TEXT,
                            "query_vector_sha256": report["dataset"]["query_vector_sha256"],
                            "profile_transport": "weaviate-client gRPC",
                        }
                        if trial_name == "file-cache-cold":
                            warm = active_client.query(mode, query_vector, with_profile=True)
                            before = file_residency(fixture_dir)
                            buckets_before = bucket_evidence(fixture_dir)
                            eviction = evict_owned_file_cache(fixture_dir, container_name if args.pageout_owned_mappings else None)
                            after = file_residency(fixture_dir)
                            buckets_after = bucket_evidence(fixture_dir)
                            bucket_eviction = {}
                            for bucket in buckets_before:
                                prev = buckets_before[bucket]["residency"]
                                post = buckets_after[bucket]["residency"]
                                bucket_eviction[bucket] = {
                                    "before": buckets_before[bucket], "after": buckets_after[bucket],
                                    "eviction_effective": (prev["resident_bytes"] > post["resident_bytes"]
                                                           if prev["supported"] and post["supported"] else None),
                                }
                            effective = (
                                before.get("resident_bytes", 0) > after.get("resident_bytes", 0)
                                if before.get("supported") and after.get("supported")
                                else None
                            )
                            cache_evidence = {
                                "warm_query_seconds": warm["client_seconds"],
                                "same_process": True,
                                "residency_before": before,
                                "eviction": eviction,
                                "residency_after": after,
                                "eviction_effective": effective,
                                "persisted_bucket_eviction": bucket_eviction,
                            }
                            if effective is not True or any(item["eviction_effective"] is not True for item in bucket_eviction.values()):
                                cache_evidence["limitation"] = "owned-file DONTNEED did not prove residency reduction in every persisted object/posting bucket"
                                report["limitations"].append(
                                    f"{trial_key}: effective file-cache eviction was not demonstrated"
                                )
                            trial["file_cache_evidence"] = cache_evidence
                            cold = active_client.query(mode, query_vector, with_profile=True)
                            trial["queries"]["warm"] = save_profile_artifacts(
                                args.out,
                                trial_key,
                                mode,
                                warm,
                                dict(mode_metadata, query_phase="warm"),
                                artifact_name=f"{mode}-warm",
                            )
                            trial["queries"]["cold"] = save_profile_artifacts(
                                args.out,
                                trial_key,
                                mode,
                                cold,
                                dict(mode_metadata, query_phase="cold"),
                            )
                            summary["rows"].append(
                                {
                                    "rescore_limit": rescore_limit,
                                    "trial": trial_name,
                                    "mode": mode,
                                    "first_query_seconds": cold["client_seconds"],
                                    "warm_query_seconds": warm["client_seconds"],
                                    "warm_profile": trial["queries"]["warm"]["profile_summary"],
                                    "warm_raw_profile": trial["queries"]["warm"]["raw_profile"],
                                    "profile": trial["queries"]["cold"]["profile_summary"],
                                    "raw_profile": trial["queries"]["cold"]["raw_profile"],
                                }
                            )
                        else:
                            first = active_client.query(mode, query_vector, with_profile=True)
                            warm = active_client.query(mode, query_vector, with_profile=False)
                            trial["queries"]["first"] = save_profile_artifacts(
                                args.out,
                                trial_key,
                                mode,
                                first,
                                dict(mode_metadata, query_phase="first"),
                            )
                            trial["queries"]["warm"] = {
                                "client_seconds": warm["client_seconds"],
                                "hits": warm["hits"],
                            }
                            summary["rows"].append(
                                {
                                    "rescore_limit": rescore_limit,
                                    "trial": trial_name,
                                    "mode": mode,
                                    "first_query_seconds": first["client_seconds"],
                                    "warm_query_seconds": warm["client_seconds"],
                                    "profile": trial["queries"]["first"]["profile_summary"],
                                    "raw_profile": trial["queries"]["first"]["raw_profile"],
                                }
                            )
                        trial["disk_bytes"] = directory_stats(fixture_dir)
                        report["trials_run"].append(trial)
                    finally:
                        if active_client is not None:
                            active_client.close()
                            active_client = None
                        if active_container is not None:
                            if active_container.stop():
                                active_container = None
                            else:
                                cleanup_safe = False
                                trial_cleanup_failed = True
                                report["limitations"].append(
                                    f"{trial_key}: container removal was not confirmed; fixture was retained"
                                )
                        trial["disk_bytes"] = trial.get("disk_bytes") or directory_stats(fixture_dir)
                        if not args.keep_fixtures and cleanup_safe:
                            remove_owned_fixture(fixture_dir, args.out)
                    if trial_cleanup_failed:
                        raise MeasurementError("owned pinned store could not be removed after trial")

        report["result"] = "passed"
        summary["result"] = "passed"
    except MeasurementInterrupted:
        report["result"] = "interrupted"
        summary["result"] = "interrupted"
        report["failed_trial"] = current_trial_key
        summary["failed_trial"] = current_trial_key
        report["error_message"] = "measurement interrupted"
        summary["error_message"] = "measurement interrupted"
    except MeasurementError as error:
        report["result"] = "failed"
        summary["result"] = "failed"
        report["error_type"] = type(error).__name__
        summary["error_type"] = type(error).__name__
        report["failed_trial"] = current_trial_key
        summary["failed_trial"] = current_trial_key
        report["error_message"] = str(error)
        summary["error_message"] = str(error)
    except Exception as error:  # Keep diagnostics controlled and generic.
        report["result"] = "failed"
        summary["result"] = "failed"
        report["error_type"] = type(error).__name__
        summary["error_type"] = type(error).__name__
        report["failed_trial"] = current_trial_key
        summary["failed_trial"] = current_trial_key
    finally:
        if active_client is not None:
            active_client.close()
        if active_container is not None:
            if active_container.stop():
                active_container = None
            else:
                cleanup_safe = False
                report["limitations"].append(
                    f"{current_trial_key}: container removal was not confirmed; fixture was retained"
                )
        report["completed_at_utc"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
        summary["completed_at_utc"] = report["completed_at_utc"]
        write_json(args.out / "report.json", report)
        write_json(args.out / "summary.json", summary)
        signal.signal(signal.SIGTERM, previous_term)
        signal.signal(signal.SIGINT, previous_int)

    return 0 if report["result"] == "passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
