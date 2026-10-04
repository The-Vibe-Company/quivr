"""Receive text records. Quivr authenticates the sender before calling us."""
from pathlib import Path

from quivr_plugin import (
    ConnectorCredentialResponse, ConnectorFetchResponse, ConnectorItem,
    ConnectorReceiveResponse, CredentialRequest, FetchRequest, ManifestContent,
    Part, Plugin, PushStatus, ReceiveRequest, ReceiveAnswer, TextContent,
)

plugin = Plugin(Path(__file__).resolve().parent.parent / "quivr-plugin.yaml")


@plugin.connector("events")
class PushSource:
    def fetch(self, request: FetchRequest) -> ConnectorFetchResponse:
        # This source has no upstream subscription to maintain or pull backfill.
        return ConnectorFetchResponse(items=[], checkpoint=request.checkpoint,
                                      more=False, push=PushStatus(state="active"))

    def check_credential(self, request: CredentialRequest) -> ConnectorCredentialResponse:
        # Instance tokens belong to Quivr; they are never plugin credentials.
        return ConnectorCredentialResponse()


@plugin.connector_route("events", "publish")
def publish(request: ReceiveRequest) -> ConnectorReceiveResponse:
    source = request.body
    if not source["text"].strip():
        return ConnectorReceiveResponse(verdict="refused", response=ReceiveAnswer(
            status=422, content_type="text/plain", body="text must contain a non-whitespace character"))
    return ConnectorReceiveResponse(
        verdict="accepted", response=ReceiveAnswer(status=200), reads=1,
        items=[ConnectorItem(record_key=source["key"], revision=source["revision"],
            content=ManifestContent(parts=[
                Part(key="title", role="title", content=TextContent(text=source["title"])),
                Part(key="body", role="body", content=TextContent(text=source["text"])),
            ]))],
    )
