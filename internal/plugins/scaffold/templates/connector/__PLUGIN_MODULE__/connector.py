"""Page through configured items using an offset checkpoint."""
from __future__ import annotations

from pathlib import Path

from quivr_plugin import (
    AccessError,
    CredentialRequest,
    ConnectorCredentialResponse,
    FetchRequest,
    ConnectorFetchResponse,
    ConnectorItem,
    ExtensionEntry,
    ManifestContent,
    Part,
    Plugin,
    SourceError,
    TextContent,
)

plugin = Plugin(Path(__file__).resolve().parent.parent / "quivr-plugin.yaml")


def authorize(credential) -> None:
    token = credential["token"]
    if token.startswith("revoked"):
        raise AccessError("token_rejected", "the source refused the token")


@plugin.connector("static")
class StaticSource:
    def fetch(self, request: FetchRequest) -> ConnectorFetchResponse:
        authorize(request.credential)
        checkpoint = request.checkpoint
        if checkpoint is None:
            offset = 0
        elif (
            isinstance(checkpoint, dict)
            and set(checkpoint) == {"offset"}
            and type(checkpoint["offset"]) is int
            and checkpoint["offset"] >= 0
        ):
            offset = checkpoint["offset"]
        else:
            raise SourceError("invalid_checkpoint", "the checkpoint is not a nonnegative offset")
        config = request.connector.config
        source_items = config["items"]
        start = min(offset, len(source_items))
        end = min(start + config.get("page_size", 10), len(source_items))
        items = []
        for source in source_items[start:end]:
            extensions = None
            if source.get("author"):
                extensions = {
                    "__PLUGIN_ID__.source": ExtensionEntry(
                        schema_version="1", data={"author": source["author"]}
                    )
                }
            items.append(
                ConnectorItem(
                    record_key=source["key"],
                    revision="1",
                    content=ManifestContent(
                        parts=[
                            Part(key="title", role="title", content=TextContent(text=source["title"])),
                            Part(key="body", role="body", content=TextContent(text=source["body"])),
                        ]
                    ),
                    extensions=extensions,
                )
            )
        request.logger.info("fetched a page", extra={"items": len(items), "offset": start})
        return ConnectorFetchResponse(
            items=items, checkpoint={"offset": end}, more=end < len(source_items), reads=end - start
        )

    def check_credential(self, request: CredentialRequest) -> ConnectorCredentialResponse:
        authorize(request.credential)
        return ConnectorCredentialResponse()
