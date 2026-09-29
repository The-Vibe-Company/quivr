"""A Markdown normalizer: one title Part and one Part per section.

The first level-1 heading, when nothing precedes it, becomes the ``title``
Part. Every heading then starts a ``body`` Part keyed ``section-<n>`` that
holds the heading and its text; Quivr indexes ``title`` and ``body`` text
Parts. Headings inside fenced code blocks are ignored.
"""
from __future__ import annotations

import re
from pathlib import Path

from quivr_plugin import (
    Invocation,
    ManifestContent,
    NormalizerResponse,
    Part,
    Plugin,
    ResponseWarning,
    TerminalError,
    TextContent,
)

MANIFEST = Path(__file__).resolve().parent.parent / "quivr-plugin.yaml"
DEFAULT_MAX_SECTIONS = 255

# An ATX heading; a closing run of # counts only after whitespace ("C#" stays).
HEADING = re.compile(r"^ {0,3}(#{1,6})[ \t]+(.*?)(?:[ \t]+#+)?[ \t]*$")
FENCE = re.compile(r"^ {0,3}(`{3,}|~{3,})")

plugin = Plugin(MANIFEST)


def split_markdown(text: str) -> tuple[str | None, list[str]]:
    """Return the document title (or None) and the non-empty section texts."""
    title: str | None = None
    sections: list[str] = []
    current: list[str] = []
    fence: str | None = None

    def flush() -> None:
        body = "\n".join(current).strip()
        if body:
            sections.append(body)
        current.clear()

    for line in text.splitlines():
        marker = FENCE.match(line)
        if marker:
            if fence is None:
                fence = marker.group(1)
            elif marker.group(1).startswith(fence[0] * len(fence)):
                fence = None
            current.append(line)
            continue
        heading = None if fence else HEADING.match(line)
        if heading and len(heading.group(1)) == 1 and title is None and not sections and not "".join(current).strip():
            title = heading.group(2).strip() or None
            current.clear()
            continue
        if heading:
            flush()
        current.append(line)
    flush()
    return title, sections


@plugin.normalizer
def normalize(invocation: Invocation) -> NormalizerResponse:
    try:
        text = invocation.read_input().decode("utf-8")
    except UnicodeDecodeError as exc:
        raise TerminalError("invalid_encoding", f"the document is not UTF-8: {exc}") from exc
    title, sections = split_markdown(text)
    if title is None and not sections:
        raise TerminalError("empty_document", "the document has no text")

    warnings: list[ResponseWarning] = []
    limit = invocation.configuration.get("max_sections", DEFAULT_MAX_SECTIONS)
    if len(sections) > limit:
        sections = sections[: limit - 1] + ["\n\n".join(sections[limit - 1 :])]
        warnings.append(ResponseWarning(code="sections_merged", message=f"sections after the {limit - 1}th were merged into one"))

    parts: list[Part] = []
    if title:
        parts.append(Part(key="title", role="title", content=TextContent(text=title)))
    for number, section in enumerate(sections, start=1):
        parts.append(Part(key=f"section-{number}", role="body", content=TextContent(text=section)))
    invocation.logger.info("normalized", extra={"parts": len(parts)})
    return NormalizerResponse(manifest=ManifestContent(parts=parts), warnings=warnings or None)
