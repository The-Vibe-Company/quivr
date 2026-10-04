"""A small test plugin whose behavior is selected by its configuration."""
import hashlib
from pathlib import Path

from quivr_plugin import (
    FileReference,
    InputBlob,
    Invocation,
    ManifestContent,
    NormalizerRequest,
    NormalizerResponse,
    Part,
    Plugin,
    RetryableError,
    SourceIdentity,
    TerminalError,
    TextContent,
)

DATA = Path(__file__).resolve().parent / "data"
MANIFEST = DATA / "quivr-plugin.yaml"


def make_plugin() -> Plugin:
    plugin = Plugin(MANIFEST)

    @plugin.normalizer
    def normalize(invocation: Invocation):
        mode = invocation.configuration.get("mode", "ok")
        if mode == "retry":
            raise RetryableError("backend_busy", "try again later")
        if mode == "terminal":
            raise TerminalError("unreadable_document", "cannot parse this document")
        if mode == "crash":
            raise RuntimeError("boom")
        text = invocation.read_input().decode()
        invocation.logger.info("normalizing", extra={"chars": len(text)})
        if mode == "invalid":
            return {"manifest": {"kind": "manifest", "parts": []}}
        if mode == "large":
            text = text * 400
        if mode == "dict":
            return {"manifest": {"kind": "manifest", "parts": [{"key": "body", "role": "body", "content": {"kind": "text", "text": text}}]}}
        repeat = invocation.configuration.get("repeat", 1)
        parts = [Part(key=f"p{i}", role="body", content=TextContent(text=text)) for i in range(repeat)]
        return NormalizerResponse(manifest=ManifestContent(parts=parts))

    return plugin


def make_request(path: Path, *, media_type: str = "text/markdown", configuration=None, sha256=None, size=None) -> NormalizerRequest:
    data = path.read_bytes()
    return NormalizerRequest(
        invocation_id="inv-1",
        idempotency_key="key-1",
        organization_id="org",
        corpus_id="corpus",
        record_id="rec",
        record_version_id="ver",
        source=SourceIdentity(corpus_id="corpus", namespace="tests", record_key=path.name),
        input=InputBlob(
            blob_id="blob-1",
            media_type=media_type,
            size_bytes=len(data) if size is None else size,
            sha256=sha256 or hashlib.sha256(data).hexdigest(),
            reference=FileReference(url=path.resolve().as_uri()),
        ),
        configuration=configuration or {},
    )
