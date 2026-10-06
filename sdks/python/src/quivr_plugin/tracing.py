"""Continue engine W3C context without configuring an exporter in the SDK."""
from __future__ import annotations

import contextlib
import contextvars
from collections.abc import Iterator, Mapping

from opentelemetry import context, trace
from opentelemetry.trace.propagation.tracecontext import TraceContextTextMapPropagator

_request_id: contextvars.ContextVar[str] = contextvars.ContextVar("quivr_request_id", default="")
_propagator = TraceContextTextMapPropagator()


@contextlib.contextmanager
def request_context(headers: Mapping[str, str]) -> Iterator[None]:
    """Extract W3C context, isolating concurrent invocations and their logs."""
    request_id = headers.get("X-Request-ID", "")
    if len(request_id) > 128 or not request_id or any(not (c.isascii() and (c.isalnum() or c in "-_.")) for c in request_id):
        request_id = ""
    request_token = _request_id.set(request_id)
    token = context.attach(_propagator.extract(headers))
    try:
        with trace.get_tracer("quivr-plugin").start_as_current_span(
            "plugin.request", kind=trace.SpanKind.SERVER,
            record_exception=False, set_status_on_exception=False,
        ):
            yield
    finally:
        context.detach(token)
        _request_id.reset(request_token)


def inject_headers(headers: dict[str, str]) -> None:
    """Continue the current invocation on an outgoing HTTP request, without baggage."""
    _propagator.inject(headers)
    if request_id := _request_id.get():
        headers["X-Request-ID"] = request_id


def log_fields() -> dict[str, str]:
    span = trace.get_current_span().get_span_context()
    return {
        "trace_id": f"{span.trace_id:032x}" if span.is_valid else "",
        "span_id": f"{span.span_id:016x}" if span.is_valid else "",
        "request_id": _request_id.get(),
    }
