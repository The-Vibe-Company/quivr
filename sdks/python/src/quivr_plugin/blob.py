"""Reading the input Blob of an invocation and verifying its size and SHA-256."""
from __future__ import annotations

import hashlib
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

from .errors import RetryableError, TerminalError
from .models import FileReference, InputBlob

_CHUNK = 1 << 16
# HTTP statuses worth a retry: an expired signature is re-signed by the engine.
_RETRYABLE_STATUS = {401, 403, 408, 425, 429}


def _file_path(url: str) -> Path:
    parsed = urllib.parse.urlparse(url)
    if parsed.scheme != "file" or parsed.netloc not in ("", "localhost"):
        raise TerminalError("invalid_input_reference", f"unsupported file reference {url!r}")
    return Path(urllib.request.url2pathname(parsed.path))


def _read_limited(stream, limit: int) -> bytes:
    chunks, total = [], 0
    while total <= limit:
        chunk = stream.read(min(_CHUNK, limit + 1 - total))
        if not chunk:
            break
        chunks.append(chunk)
        total += len(chunk)
    return b"".join(chunks)


def read_input(blob: InputBlob, *, timeout: float = 30.0) -> bytes:
    """Return the input Blob bytes after checking its declared size and SHA-256.

    Supports signed http(s) references and, for local development, file://
    references. Transport failures raise RetryableError("input_unavailable");
    a size or checksum mismatch raises TerminalError. Reads at most
    ``size_bytes + 1`` bytes.
    """
    reference = blob.reference
    limit = blob.size_bytes
    if isinstance(reference, FileReference):
        path = _file_path(reference.url)
        try:
            with path.open("rb") as stream:
                data = _read_limited(stream, limit)
        except FileNotFoundError as exc:
            raise TerminalError("input_not_found", f"input file {path} does not exist") from exc
        except OSError as exc:
            raise RetryableError("input_unavailable", f"cannot read input file {path}: {exc}") from exc
    else:
        request = urllib.request.Request(reference.url, method="GET")
        try:
            with urllib.request.urlopen(request, timeout=timeout) as response:
                data = _read_limited(response, limit)
        except urllib.error.HTTPError as exc:
            exc.close()
            if exc.code >= 500 or exc.code in _RETRYABLE_STATUS:
                raise RetryableError("input_unavailable", f"input Blob download failed with HTTP {exc.code}") from exc
            raise TerminalError("input_not_found", f"input Blob download failed with HTTP {exc.code}") from exc
        except (urllib.error.URLError, TimeoutError, OSError) as exc:
            raise RetryableError("input_unavailable", f"input Blob download failed: {exc}") from exc
    if len(data) != blob.size_bytes:
        shown = f"more than {limit}" if len(data) > limit else str(len(data))
        raise TerminalError("input_size_mismatch", f"input Blob has {shown} bytes; the request declares {blob.size_bytes}")
    digest = hashlib.sha256(data).hexdigest()
    if digest != blob.sha256:
        raise TerminalError("input_checksum_mismatch", f"input Blob sha256 is {digest}; the request declares {blob.sha256}")
    return data
