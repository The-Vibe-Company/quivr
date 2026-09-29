"""Serialization round-trips against the normative Plugin Protocol fixtures."""
import importlib.util
import json
import unittest
from pathlib import Path

import yaml

from quivr_plugin import models
from quivr_plugin.schema import protocol_errors

REPO = Path(__file__).resolve().parents[3]
FIXTURES = REPO / "contracts/plugins/v0/fixtures"

MODELS = {
    "normalizer-request.schema.json": models.NormalizerRequest,
    "normalizer-response.schema.json": models.NormalizerResponse,
    "discovery.schema.json": models.Discovery,
    "health.schema.json": models.Health,
    "error.schema.json": models.ErrorEnvelope,
    "plugin-manifest.schema.json": models.PluginManifest,
    "plugin-fixture.schema.json": models.InvocationFixture,
    "subscription-request.schema.json": models.SubscriptionRequest,
    "subscription-response.schema.json": models.SubscriptionResponse,
    "subscription-fixture.schema.json": models.SubscriptionFixture,
    "connector-fetch-request.schema.json": models.ConnectorFetchRequest,
    "connector-fetch-response.schema.json": models.ConnectorFetchResponse,
    "connector-check-credential-request.schema.json": models.ConnectorCredentialRequest,
    "connector-check-credential-response.schema.json": models.ConnectorCredentialResponse,
    "connector-fixture.schema.json": models.ConnectorFixture,
    "connector-describe-attachment-request.schema.json": models.ConnectorDescribeAttachmentRequest,
    "connector-describe-attachment-response.schema.json": models.ConnectorDescribeAttachmentResponse,
    "connector-upload-attachment-request.schema.json": models.ConnectorUploadAttachmentRequest,
    "connector-upload-attachment-response.schema.json": models.ConnectorUploadAttachmentResponse,
}


def load(case):
    path = FIXTURES / case["file"]
    text = path.read_text(encoding="utf-8")
    return yaml.safe_load(text) if path.suffix == ".yaml" else json.loads(text)


class NormativeFixtureRoundTrip(unittest.TestCase):
    def cases(self):
        index = json.loads((FIXTURES / "index.json").read_text())
        return index["cases"]

    def test_every_fixture_schema_has_a_model(self):
        self.assertEqual({c["schema"] for c in self.cases()} - set(MODELS), set())

    def test_schema_valid_fixtures_round_trip_exactly(self):
        count = 0
        for case in self.cases():
            if not case["schema_valid"]:
                continue
            with self.subTest(case["file"]):
                value = load(case)
                model = MODELS[case["schema"]].from_dict(value)
                self.assertEqual(model.to_dict(), value)
                self.assertEqual(protocol_errors(case["schema"], model.to_dict()), [])
                count += 1
        self.assertGreater(count, 10)

    def test_schema_outcome_matches_the_index(self):
        for case in self.cases():
            with self.subTest(case["file"]):
                self.assertEqual(protocol_errors(case["schema"], load(case)) == [], case["schema_valid"])

    def test_decoding_rejects_unknown_fields_and_wrong_kinds(self):
        with self.assertRaisesRegex(ValueError, "unknown field"):
            models.NormalizerResponse.from_dict(json.loads((FIXTURES / "responses/unknown-field.json").read_text()))
        with self.assertRaisesRegex(ValueError, "expected one of"):
            models.NormalizerResponse.from_dict(json.loads((FIXTURES / "responses/wrong-manifest-kind.json").read_text()))
        with self.assertRaisesRegex(ValueError, "missing required field sha256"):
            models.NormalizerRequest.from_dict(json.loads((FIXTURES / "requests/missing-sha256.json").read_text()))

    def test_unions_decode_by_kind(self):
        request = models.NormalizerRequest.from_dict(json.loads((FIXTURES / "requests/signed-url.json").read_text()))
        self.assertIsInstance(request.input.reference, models.SignedUrlReference)
        request = models.NormalizerRequest.from_dict(json.loads((FIXTURES / "requests/file-reference.json").read_text()))
        self.assertIsInstance(request.input.reference, models.FileReference)
        part = models.Part(key="a", role="section", content=models.TextContent(text="hello"))
        self.assertEqual(part.to_dict(), {"key": "a", "role": "section", "content": {"kind": "text", "text": "hello"}})


class GeneratedFilesAreCurrent(unittest.TestCase):
    def test_generator_check_passes(self):
        spec = importlib.util.spec_from_file_location("generate", REPO / "sdks/python/scripts/generate.py")
        generate = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(generate)
        self.assertEqual(generate.main(["generate.py", "--check"]), 0)


if __name__ == "__main__":
    unittest.main()
