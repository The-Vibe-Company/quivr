"""Owner tests for the Python ingestion Contribution routes."""
import json
from pathlib import Path
import tempfile
import unittest

import yaml

from quivr_plugin import Plugin, SegmentAndEmbedRequest, EmbedQueryRequest

REPO = Path(__file__).resolve().parents[3]
SEGMENT = "/v0/contributions/ingestion/segment_and_embed"
QUERY = "/v0/contributions/ingestion/embed_query"


class Ingestion(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        manifest = yaml.safe_load((REPO / "contracts/plugins/v0/fixtures/manifests/valid/ingestion.yaml").read_text())
        manifest["configuration"] = {
            "schema": {
                "type": "object",
                "additionalProperties": False,
                "properties": {"mode": {"type": "string"}},
            }
        }
        self.path = Path(self.directory.name) / "quivr-plugin.yaml"
        self.path.write_text(yaml.safe_dump(manifest))
        self.plugin = Plugin(self.path)
        self.segment_request = {
            "invocation_id": "segment-1",
            "idempotency_key": "key-1",
            "contribution": "ingestion",
            "organization_id": "org",
            "configuration": {},
            "version": {"corpus_id": "corpus", "record_id": "record", "record_version_id": "version"},
            "parts": [{"key": "body", "role": "body", "text": "hello world"}],
            "spaces": ["example.words.small"],
        }
        self.query_request = {
            "invocation_id": "query-1",
            "contribution": "ingestion",
            "organization_id": "org",
            "configuration": {},
            "space": "example.words.small",
            "query": {"modality": "text", "text": "hello"},
        }

    def invoke(self, path, request):
        return self.plugin.handle("POST", path, json.dumps(request).encode())

    def test_normative_response_cases_match_the_contract_oracle(self):
        index = json.loads((REPO / "contracts/plugins/v0/fixtures/index.json").read_text())
        cases = [
            case for case in index["cases"]
            if case["schema"] in {
                "ingestion-segment-and-embed-response.schema.json",
                "ingestion-embed-query-response.schema.json",
            }
        ]
        self.assertGreater(len(cases), 1)
        for case in cases:
            with self.subTest(case=case["file"]):
                plugin = Plugin(REPO / "contracts/plugins/v0/fixtures" / case["manifest"])
                request = json.loads((REPO / "contracts/plugins/v0/fixtures" / case["request"]).read_text())
                response = json.loads((REPO / "contracts/plugins/v0/fixtures" / case["file"]).read_text())
                schema = case["schema"]
                seen = []
                if schema == "ingestion-segment-and-embed-response.schema.json":
                    def segment(typed_request):
                        seen.append(typed_request)
                        return response
                    plugin.segment_and_embed(segment)
                    path = SEGMENT
                else:
                    def query(typed_request):
                        seen.append(typed_request)
                        return response
                    plugin.embed_query(query)
                    path = QUERY
                reply = plugin.handle("POST", path, json.dumps(request).encode())
                self.assertEqual(len(seen), 1)
                typed_request = seen[0]
                if path == SEGMENT:
                    self.assertIsInstance(typed_request, SegmentAndEmbedRequest)
                    self.assertEqual(typed_request.parts[0].text, request["parts"][0]["text"])
                else:
                    self.assertIsInstance(typed_request, EmbedQueryRequest)
                    self.assertEqual(typed_request.space, request["space"])
                self.assertEqual(typed_request.to_dict(), request)
                self.assertEqual(reply.status == 200, case["valid"], reply.body)
                if not case["valid"]:
                    self.assertEqual(reply.body["code"], "invalid_response", reply.body)

    def test_unknown_space_is_rejected_before_the_handler_runs(self):
        @self.plugin.embed_query
        def query(request):
            self.fail("unknown space reached the handler")

        request = json.loads(json.dumps(self.query_request))
        request["space"] = "example.words.unknown"
        reply = self.invoke(QUERY, request)
        self.assertEqual((reply.status, reply.body["code"]), (400, "unknown_space"), reply.body)

    def test_provenance_bound_uses_stored_json_size(self):
        cases = [
            ("HTML-heavy provenance metadata", {"metadata": "<&" * 400}),
            ("Go fixed-float provenance exceeds stored 4 KiB", {"values": [1e-6] * 500}),
        ]
        for description, provenance in cases:
            with self.subTest(description=description):
                @self.plugin.segment_and_embed
                def segment(_request, provenance=provenance):
                    return {"segments": [{
                        "part_key": "body", "start": 0, "end": 5,
                        "vectors": {"example.words.small": [1, 0, 0, 0]},
                        "provenance": provenance,
                    }]}

                reply = self.invoke(SEGMENT, self.segment_request)
                self.assertEqual(reply.status, 500, reply.body)
                self.assertEqual(reply.body.get("code"), "invalid_response", reply.body)


if __name__ == "__main__":
    unittest.main()
