"""Bounded retries for connection setup and acknowledged, read-only remote calls."""
import contextlib
import os
import time
import threading


class Outage(RuntimeError):
    """The network recovery window elapsed; preserve uncertain work for cleanup."""


class AdmissionPaused(RuntimeError):
    """Roll back a reservation before waiting for transport recovery."""


class AdmissionGate:
    """Pause all local campaign reservations while a Modal transport recovers."""
    def __init__(self):
        self.condition = threading.Condition()
        self.pending = 0
        self.failed = False

    def pause(self):
        with self.condition:
            self.pending += 1

    def finish(self, failed):
        with self.condition:
            self.pending -= 1
            self.failed |= failed
            self.condition.notify_all()

    @contextlib.contextmanager
    def commit(self):
        # Serialize the final write/commit with outage detection. Never wait
        # for recovery while holding a database transaction or campaign lock.
        with self.condition:
            if self.pending or self.failed:
                raise AdmissionPaused()
            yield

    def wait(self):
        with self.condition:
            ready = self.condition.wait_for(lambda: not self.pending or self.failed, timeout=window())
            if not ready or self.failed:
                raise Outage('campaign network recovery failed; paid admission refused')


_admissions = {}
_admission_lock = threading.Lock()


def admission(campaign):
    # A gate is process-lifetime state: concurrent creation or eviction could
    # split the campaign's pause/failure fence across different identities.
    with _admission_lock:
        if campaign not in _admissions:
            _admissions[campaign] = AdmissionGate()
        return _admissions[campaign]


def window():
    try:
        seconds = int(os.environ.get('EVAL_NETWORK_OUTAGE_SECONDS', '600'))
    except ValueError:
        raise ValueError('EVAL_NETWORK_OUTAGE_SECONDS must be an integer from 0 to 3600') from None
    if not 0 <= seconds <= 3600:
        raise ValueError('EVAL_NETWORK_OUTAGE_SECONDS must be an integer from 0 to 3600')
    return seconds


def retry(operation, unreachable, *, seconds=None, campaign=None):
    """Never use around admission, app creation, spawn or uncertain SQL commits."""
    seconds = window() if seconds is None else seconds
    deadline = None
    delay = 1
    gate = None
    failed = False
    try:
        while True:
            try:
                return operation()
            except Exception as error:
                if not unreachable(error):
                    raise
                if deadline is None:
                    deadline = time.monotonic() + seconds
                    if campaign is not None:
                        gate = admission(campaign)
                        gate.pause()
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    failed = True
                    raise Outage('network outage window elapsed; no new paid work admitted') from None
                time.sleep(min(delay, remaining))
                delay = min(delay * 2, 10)
    finally:
        if gate is not None:
            gate.finish(failed)


def postgres_unreachable(error):
    import psycopg
    if not isinstance(error, psycopg.OperationalError):
        return False
    if error.sqlstate is not None:
        return error.sqlstate.startswith('08') or error.sqlstate in ('57P01', '57P02', '57P03')
    # libpq connection failures often have no SQLSTATE, including authentication
    # failures. Inspect only locally; never emit dependency text or credentials.
    message = str(error).lower()
    refusals = ('password authentication failed', 'no password supplied', 'no pg_hba.conf entry',
                'does not exist', 'certificate verify failed', 'root certificate file',
                'private key file', 'invalid connection option', 'invalid sslmode')
    return not any(refusal in message for refusal in refusals)


def connect(dsn, **kwargs):
    import psycopg
    return retry(lambda: psycopg.connect(dsn, **kwargs), postgres_unreachable)

def modal_unreachable(error):
    import modal
    from grpclib import Status
    from modal._utils.grpc_utils import RetryTimeoutError
    if isinstance(error, RetryTimeoutError):
        error = error.final_exception
    if isinstance(error, modal.exception.ConnectionError):
        return True
    # SDK 1.x retains the underlying status on ServiceError; UNKNOWN and
    # CANCELLED also map here, so the class alone is insufficient.
    return (isinstance(error, modal.exception.ServiceError)
            and getattr(error, '_grpc_status', None) in (Status.UNAVAILABLE, Status.DEADLINE_EXCEEDED))
