"""Attempt a dependency fetch at most twice, only after a recognizable network failure.

Use this around downloads/installations, never a build or test command. Integrity,
resolution, compilation and test failures do not qualify for another attempt.
"""
import os
from pathlib import Path
import re
import subprocess
import sys

NETWORK = re.compile(r'ECONNRESET|ETIMEDOUT|EAI_AGAIN|ECONNREFUSED|ENETUNREACH|connection (?:reset|timed out|refused)|temporary failure in name resolution|TLS handshake timeout|(?:Get|Post|Head) "?https?://[^\n]+unexpected EOF|HTTP(?:Error:|/\S*\s+| error\s+)(?:502|503|504)|(?:502|503|504) (?:Bad Gateway|Service Unavailable|Gateway Timeout)', re.I)
NON_NETWORK = re.compile(r'(?:^|\n)(?:#\s+\S+|[^\s:]+\.(?:go|c|h)(?::\d+){1,2}:)|EINTEGRITY|hash(?:es)? (?:do not match|mismatch)|Failed building wheel|Could not find a version that satisfies|No matching distribution found', re.I)
PREFIXES = [('npm', 'ci'), ('npm', 'install'), ('pip', 'install'), ('python3', '-m', 'pip', 'install'), ('python', '-m', 'pip', 'install'),
            ('go', 'mod', 'download'), ('go', 'install'), ('docker', 'pull'), ('playwright', 'install', 'chromium')]


def main(argv=None):
    command = list(sys.argv[1:] if argv is None else argv)
    if command[:1] == ['--']:
        command.pop(0)
    normalized = [Path(command[0]).name, *command[1:]] if command else []
    if normalized and normalized[0].startswith('python3.'):
        normalized[0] = 'python3'
    if not any(tuple(normalized[:len(prefix)]) == prefix for prefix in PREFIXES):
        print('ci_fetch accepts dependency downloads/installations only', file=sys.stderr)
        return 2
    # Disable nested pip/npm retry loops: this wrapper owns the single retry.
    environment = {**os.environ, 'PIP_RETRIES': '0', 'npm_config_fetch_retries': '0'}
    for attempt in range(2):
        try:
            result = subprocess.run(command, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, timeout=300, env=environment)
        except subprocess.TimeoutExpired:
            print('Dependency fetch exceeded its 300-second deadline', file=sys.stderr)
            return 1
        print(result.stdout, end='', flush=True)
        if result.returncode == 0 or attempt == 1 or NON_NETWORK.search(result.stdout) or not NETWORK.search(result.stdout):
            return result.returncode
        print('Network download failure: making the one bounded second attempt', flush=True)
    return 1


if __name__ == '__main__':
    sys.exit(main())
