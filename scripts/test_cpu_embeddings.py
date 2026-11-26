"""Owner-boundary tests for the network-bound CPU embedding service."""

import contextlib
import hashlib
import http.client
import io
import json
import pathlib
import tempfile
import threading
import unittest
from unittest.mock import patch

from deploy.modal import embedding_api
from deploy.cpu import embedding_server
from scripts import measure_cpu_embeddings
from scripts import prepare_embeddings


TOKEN = "fixture-embedding-token"
METADATA = {
    "status": "ok",
    "model": embedding_api.MODEL,
    "revision": embedding_api.REVISION,
    "model_revision": "914f7f89142e33e7",
    "source_revision": embedding_api.REVISION,
    "dimensions": embedding_api.DIMENSIONS,
    "threads": 4,
    "backend": "onnx",
    "precision": "int8",
    "onnxruntime_version": "1.32.0",
    "tokenizers_version": "0.23.2",
    "numpy_version": "2.5.2",
    "ignored_secret": "do-not-expose",
}


class FakeArray:
    def __init__(self, vectors):
        self._vectors = vectors
        self.shape = (len(vectors), embedding_api.DIMENSIONS)

    def tolist(self):
        return self._vectors


class FakeModel:
    def __init__(self, vectors=None):
        self.vectors = vectors or [
            [1.0] + [0.0] * (embedding_api.DIMENSIONS - 1),
        ]
        self.error = None
        self.calls = []

    def encode(self, texts, **kwargs):
        self.calls.append((texts, kwargs))
        if self.error is not None:
            raise self.error
        return FakeArray(self.vectors)


class CpuEmbeddingHttpTest(unittest.TestCase):
    def setUp(self):
        self.model = FakeModel()
        self.server = embedding_server.create_server(
            self.model, token=TOKEN, metadata=METADATA, host="127.0.0.1", port=0,
        )
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.host, self.port = self.server.server_address

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=2)

    def request(self, method, path, *, body=None, token=TOKEN):
        connection = http.client.HTTPConnection(self.host, self.port, timeout=2)
        headers = {}
        if token is not None:
            headers["Authorization"] = "Bearer " + token
        if body is not None:
            raw = json.dumps(body).encode("utf-8")
            headers["Content-Type"] = "application/json"
            headers["Content-Length"] = str(len(raw))
        else:
            raw = None
        connection.request(method, path, body=raw, headers=headers)
        response = connection.getresponse()
        payload = json.loads(response.read())
        connection.close()
        return response.status, payload

    def test_health_requires_bearer_and_exposes_verified_identity(self):
        status, body = self.request("GET", "/health", token=None)
        self.assertEqual(status, 401)
        self.assertEqual(body["error"]["code"], "unauthorized")
        self.assertEqual(self.model.calls, [])

        status, body = self.request("GET", "/health")
        self.assertEqual(status, 200)
        self.assertEqual(
            {key: body[key] for key in ("status", "model", "revision", "dimensions",
                                        "backend", "precision", "onnxruntime_version",
                                        "tokenizers_version", "numpy_version")},
            {"status": "ok", "model": embedding_api.MODEL,
             "revision": embedding_api.REVISION, "dimensions": 768,
             "backend": "onnx", "precision": "int8", "onnxruntime_version": "1.32.0",
             "tokenizers_version": "0.23.2", "numpy_version": "2.5.2"},
        )
        self.assertNotIn("ignored_secret", body)

    def test_embedding_response_keeps_input_order_and_sanitizes_failures(self):
        vectors = [[1.0] + [0.0] * 767, [0.0, 1.0] + [0.0] * 766]
        self.model.vectors = vectors
        request = {
            "model": embedding_api.MODEL,
            "input": ["first passage", "second passage"],
            "dimensions": embedding_api.DIMENSIONS,
            "encoding_format": "float",
        }
        status, body = self.request("POST", "/v1/embeddings", body=request)
        self.assertEqual(status, 200)
        self.assertEqual(body["model"], embedding_api.MODEL)
        self.assertEqual([row["index"] for row in body["data"]], [0, 1])
        self.assertEqual([row["embedding"] for row in body["data"]], vectors)
        self.assertEqual(self.model.calls[0][0], request["input"])

        self.model.error = RuntimeError("private model input")
        status, body = self.request("POST", "/v1/embeddings", body={
            "model": embedding_api.MODEL, "input": "one passage",
        })
        self.assertEqual(status, 503)
        self.assertEqual(body["error"]["code"], "encoding_failed")
        self.assertNotIn("private model input", json.dumps(body))


class CpuEmbeddingCliTest(unittest.TestCase):
    def test_e5_preparation_uses_revision_relative_mirror_and_verify_only(self):
        payload = b"e5 fixture"
        lock = {
            "model_revision": "revision",
            "tei_image": "image",
            "files": {"config.json": hashlib.sha256(payload).hexdigest()},
        }

        class Response(io.BytesIO):
            def __enter__(self):
                return self

            def __exit__(self, *_args):
                return False

        with tempfile.TemporaryDirectory() as directory, \
                patch.object(prepare_embeddings, "LOCK", lock), \
                patch.object(prepare_embeddings.platform, "system", return_value="Linux"), \
                patch.object(prepare_embeddings.platform, "machine", return_value="x86_64"), \
                patch.object(prepare_embeddings.urllib.request, "urlopen",
                             return_value=Response(payload)) as download:
            output = pathlib.Path(directory) / "e5"
            result = prepare_embeddings.prepare(output, mirror="https://mirror.example/hf")
            self.assertEqual(result["downloaded_bytes"], len(payload))
            self.assertEqual(
                download.call_args.args[0],
                "https://mirror.example/hf/intfloat/multilingual-e5-small/resolve/revision/config.json",
            )
            with patch.object(prepare_embeddings.urllib.request, "urlopen",
                              side_effect=AssertionError("verify-only downloaded")):
                self.assertEqual(prepare_embeddings.prepare(output, verify_only=True)["downloaded_bytes"], 0)

    def test_serve_requires_cache_marker_before_loading_model(self):
        class FakeServer:
            def serve_forever(self):
                raise KeyboardInterrupt

            def server_close(self):
                pass

        with tempfile.TemporaryDirectory() as directory, \
                patch.dict("os.environ", {"EMBED_API_KEY": TOKEN}, clear=False), \
                patch.object(embedding_server, "load_model", return_value=(object(), METADATA)) as load, \
                patch.object(embedding_server, "create_server", return_value=FakeServer()):
            model_dir = pathlib.Path(directory) / "gemma"
            with contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(embedding_server.main([
                    "serve", "--model-dir", str(model_dir), "--port", "8080",
                ]), 1)
            load.assert_not_called()

            marker = model_dir.parent / embedding_server.MARKER_NAME
            marker.parent.mkdir(parents=True, exist_ok=True)
            marker.write_text(json.dumps({"model": "gemma"}))
            with contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(embedding_server.main([
                    "serve", "--model-dir", str(model_dir), "--port", "8080",
                ]), 0)
            load.assert_called_once()


if __name__ == "__main__":
    unittest.main()
