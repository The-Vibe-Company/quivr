"""Loopback ports for the services the local harness starts (THE-728).

The harness picks every service port (API, probes, worker probe, fake
servers, receivers, plugins, demo) before the service binds it, sometimes
minutes later. Asking the kernel for port 0 and releasing it is racy: the next
`bind(0)` of the same run may return the same port, and in the meantime any
outgoing connection or Docker published port may take it, because all of
those come from the kernel's ephemeral range.

This allocator removes both races:

* it remembers every port it has handed out, or that a stack loaded from its
  persisted state, and never hands one out twice in a process;
* it picks from a band below the ephemeral range, which the kernel never
  gives to `bind(0)`, to outgoing source ports or to Docker's published
  ports.

It still checks that the candidate is free on loopback, so a program already
listening there is skipped. Harness processes running side by side on one
host (parallel worktrees) coordinate through lease files under the system
temporary directory: a port leased by a live harness process is skipped, and
a lease whose process has exited is reclaimed. A stopped dev stack keeps its
ports in its state file but holds no lease on them, as before this allocator.
"""
import fcntl, os, pathlib, random, socket, stat, subprocess, sys, tempfile, threading

# The band is [LOWEST, HIGHEST), cut short where the kernel's ephemeral range
# starts. LOWEST sits above the registered services most developer machines
# run. HIGHEST is the Linux default ephemeral start; it also keeps the band
# clear of Docker Desktop's published ports on macOS, whose kernel range only
# starts at 49152.
LOWEST = 15000
HIGHEST = 32768
FALLBACK_RANGE = (32768, 60999)
MINIMUM_BAND = 1024
# Per user: /tmp is shared on Linux, and leases only coordinate one user's harness runs.
LEASES = pathlib.Path(tempfile.gettempdir()) / f'quivr-harness-ports-{os.getuid()}'

_lock = threading.Lock()
_reserved = set()


def _read_range():
    if sys.platform.startswith('linux'):
        with open('/proc/sys/net/ipv4/ip_local_port_range') as f:
            first, last = (int(v) for v in f.read().split())
        return first, last
    if sys.platform == 'darwin':
        values = subprocess.run(['sysctl', '-n', 'net.inet.ip.portrange.first', 'net.inet.ip.portrange.last'],
                                capture_output=True, text=True, check=True, timeout=5).stdout.split()
        return int(values[0]), int(values[1])
    raise OSError('unknown ephemeral port range on ' + sys.platform)


def ephemeral_range():
    """The kernel's ephemeral port range (first, last), or the Linux default when unreadable."""
    try:
        first, last = _read_range()
        if 1024 <= first <= last <= 65535:
            return first, last
    except (OSError, ValueError, IndexError, subprocess.SubprocessError):
        pass
    return FALLBACK_RANGE


def band():
    """The [low, high) band the harness allocates from: below the ephemeral range.

    When the kernel range starts too low to leave a usable band beneath it, the
    harness falls back to [LOWEST, HIGHEST): ports are still never handed out
    twice, but the kernel may then give one away before its service binds it.
    """
    first, _ = ephemeral_range()
    high = min(first, HIGHEST)
    if high - LOWEST >= MINIMUM_BAND:
        return LOWEST, high
    if first - 1024 >= MINIMUM_BAND:
        return 1024, first
    return LOWEST, HIGHEST


def _alive(pid):
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def _lease(candidate):
    """Lease a port to this process; False while another live harness process holds it.

    Leases are small files naming the holder's PID. A host-wide lock serializes
    leasing, so two processes can never both claim, or both reclaim, one port.
    """
    LEASES.mkdir(mode=0o700, parents=True, exist_ok=True)
    info = os.lstat(LEASES)
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) & 0o077:
        raise RuntimeError(f'{LEASES} must be a private directory owned by this user')
    path = LEASES / str(candidate)
    lock = os.open(LEASES / '.lock', os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    try:
        fcntl.flock(lock, fcntl.LOCK_EX)
        try:
            fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
            with os.fdopen(fd) as f:
                holder = int(f.read() or 0)
        except (OSError, ValueError):
            holder = 0
        if holder and holder != os.getpid() and _alive(holder):
            return False
        # Free, stale (its holder exited) or already ours: take it.
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, 'w') as f:
            f.write(str(os.getpid()))
        return True
    finally:
        os.close(lock)


def _free(candidate):
    with socket.socket() as s:
        try:
            s.bind(('127.0.0.1', candidate))
        except OSError:
            return False
    return True


def reserve(taken):
    """Record ports already assigned (for example loaded from a stack's state) so they are never handed out."""
    with _lock:
        _reserved.update(int(p) for p in taken if p)


def allocate():
    """A loopback port that is free now, outside the ephemeral range, and never handed out before in this process."""
    low, high = band()
    with _lock:
        for _ in range(4 * (high - low)):
            candidate = random.randrange(low, high)
            if candidate in _reserved or not _free(candidate) or not _lease(candidate):
                continue
            _reserved.add(candidate)
            return candidate
    raise RuntimeError(f'no free loopback port between {low} and {high}')
