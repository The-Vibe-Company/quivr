"""The PDF → text normalizer: one ``body`` Part per page that has text.

Conventions (see README.md):

* Part ``page-<n>`` (role ``body``) holds the extracted text of page n, 1-based.
  Quivr indexes ``title`` and ``body`` text Parts, so every page is searchable.
* Part ``source`` (role ``source``) references the input PDF itself, unless
  ``include_source`` is false. The Record Version also keeps the input in
  ``provenance.source_blob_ids``.
* Pages without extractable text (blank or scanned) are skipped and reported in
  one ``empty_pages`` warning. Encrypted and unreadable PDFs are terminal errors.
* The Version extension ``pdf-text.document`` (schema version 1) records the
  page count and the number of pages with text.
"""
from __future__ import annotations

import io
import logging
import re
from pathlib import Path

from pypdf import PdfReader
from pypdf.errors import FileNotDecryptedError

from quivr_plugin import (
    BlobContent,
    ExtensionEntry,
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
# Quivr's built-in indexing reads at most 64 text Parts, 256 KiB of text and 256
# segments of about 384 tokens per Version. 128 KiB of text keeps ordinary prose
# well under the segment limit; densely tokenized text may need a lower budget.
DEFAULT_MAX_PAGE_PARTS = 64
DEFAULT_MAX_TEXT_BYTES = 128 * 1024
MAX_WARNING_CHARS = 1024
# The extension namespace this plugin declares in quivr-plugin.yaml.
DOCUMENT = "pdf-text.document"

# pypdf reports recoverable oddities of real-world files as log warnings; keep them out of the plugin log.
logging.getLogger("pypdf").setLevel(logging.ERROR)

plugin = Plugin(MANIFEST)

_SURROGATES = re.compile("[\ud800-\udfff]")
_BLANK_LINES = re.compile(r"\n{3,}")


def clean(text: str) -> str:
    """Text Quivr can index: no NUL, valid UTF-8, trimmed lines, at most one blank line in a row."""
    text = _SURROGATES.sub("", text.replace("\x00", ""))
    text = "\n".join(line.rstrip() for line in text.replace("\r\n", "\n").replace("\r", "\n").split("\n"))
    return _BLANK_LINES.sub("\n\n", text).strip()


def _page_list(numbers: list[int]) -> str:
    """Page numbers as short ranges, e.g. "3, 5-9"."""
    ranges: list[str] = []
    start = prev = numbers[0]
    for n in numbers[1:] + [None]:
        if n is not None and n == prev + 1:
            prev = n
            continue
        ranges.append(str(start) if start == prev else f"{start}-{prev}")
        if n is not None:
            start = prev = n
    return ", ".join(ranges)


def _bounded(message: str) -> str:
    return message if len(message) <= MAX_WARNING_CHARS else message[: MAX_WARNING_CHARS - 1] + "…"


def _open(data: bytes) -> PdfReader:
    try:
        reader = PdfReader(io.BytesIO(data))
        if reader.is_encrypted:
            try:
                opened = reader.decrypt("")
            except Exception as exc:  # unsupported algorithm or a broken encryption dictionary
                raise TerminalError("encrypted_pdf", f"the PDF is encrypted and cannot be opened: {exc}") from exc
            if not opened:
                raise TerminalError("encrypted_pdf", "the PDF is encrypted with a password")
        len(reader.pages)  # reads the page tree: a broken one fails here
        return reader
    except TerminalError:
        raise
    except FileNotDecryptedError as exc:
        raise TerminalError("encrypted_pdf", f"the PDF is encrypted: {exc}") from exc
    except Exception as exc:  # pypdf raises PdfReadError and various parsing errors on damaged files
        raise TerminalError("corrupt_pdf", f"the PDF cannot be read: {exc}") from exc


@plugin.normalizer
def normalize(invocation: Invocation) -> NormalizerResponse:
    config = invocation.configuration
    max_parts = config.get("max_page_parts", DEFAULT_MAX_PAGE_PARTS)
    budget = config.get("max_text_bytes", DEFAULT_MAX_TEXT_BYTES)
    include_source = config.get("include_source", True)

    reader = _open(invocation.read_input())
    pages: list[tuple[int, str]] = []
    empty: list[int] = []
    unreadable: list[int] = []
    for number, page in enumerate(reader.pages, start=1):
        try:
            text = clean(page.extract_text() or "")
        except Exception:  # one damaged page does not lose the others
            unreadable.append(number)
            continue
        if text:
            pages.append((number, text))
        else:
            empty.append(number)
    if not pages and unreadable and not empty:
        raise TerminalError("corrupt_pdf", "no page of the PDF can be read")
    document = {"page_count": len(reader.pages), "text_pages": len(pages)}

    warnings: list[ResponseWarning] = []
    if empty:
        warnings.append(ResponseWarning(code="empty_pages", message=_bounded(f"{len(empty)} page(s) without extractable text, possibly scanned: {_page_list(empty)}")))
    if unreadable:
        warnings.append(ResponseWarning(code="unreadable_pages", message=_bounded(f"{len(unreadable)} page(s) could not be read: {_page_list(unreadable)}")))

    # Keep at most max_parts Parts: later pages join the last kept one.
    if len(pages) > max_parts:
        head, tail = pages[: max_parts - 1], pages[max_parts - 1 :]
        pages = head + [(tail[0][0], "\n\n".join(text for _, text in tail))]
        warnings.append(ResponseWarning(code="pages_merged", message=f"pages from {tail[0][0]} on were merged into Part page-{tail[0][0]} (limit {max_parts} page Parts)"))

    # Keep at most budget UTF-8 bytes of text in total.
    kept: list[tuple[int, str]] = []
    remaining = budget
    for number, text in pages:
        encoded = text.encode("utf-8")
        if len(encoded) > remaining:
            text = encoded[:remaining].decode("utf-8", errors="ignore").rstrip()
            if text:
                kept.append((number, text))
            warnings.append(ResponseWarning(code="text_truncated", message=f"text after {budget} bytes was dropped, from page {number} on"))
            break
        kept.append((number, text))
        remaining -= len(encoded)

    parts = [Part(key=f"page-{number}", role="body", content=TextContent(text=text)) for number, text in kept]
    if not parts:
        if not include_source:
            raise TerminalError("no_text", "the PDF has no extractable text")
        warnings.append(ResponseWarning(code="no_text", message="the PDF has no extractable text; only the source PDF is kept, and it is not searchable"))
    if include_source:
        source = invocation.request.input
        parts.append(Part(key="source", role="source", content=BlobContent(blob_id=source.blob_id, media_type=source.media_type)))
    # Warnings travel in the response; logging them too keeps them visible next to the invocation id.
    for warning in warnings:
        invocation.logger.warning(warning.message, extra={"code": warning.code})
    invocation.logger.info("normalized", extra={"pages": len(reader.pages), "parts": len(parts)})
    return NormalizerResponse(
        manifest=ManifestContent(parts=parts),
        extensions={DOCUMENT: ExtensionEntry(schema_version="1", data=document)},
        warnings=warnings[:32] or None,
    )
