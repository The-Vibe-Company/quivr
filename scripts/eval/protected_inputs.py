"""Verified encrypted TREC inputs; callers own ephemeral storage and admission."""
import pathlib
import subprocess

import trec


class DecryptionError(ValueError):
    """Age rejected the identity or encrypted artifact; details remain private."""


class DecryptionTimeout(TimeoutError):
    """Age exceeded the ciphertext-size-based time allowance."""


def decrypt(entry, ciphertext, identity, directory):
    """Verify ciphertext before age, then verify the extracted TREC fingerprint."""
    if trec.sha256_file(ciphertext) != entry['digest']:
        raise ValueError('ciphertext checksum mismatch')
    archive = pathlib.Path(directory) / 'input.tar.gz'
    with archive.open('wb') as out:
        # Allow at least one minute and scale for large corpora at 8 MiB/s.
        timeout = max(60, pathlib.Path(ciphertext).stat().st_size / (8 * 1024 * 1024))
        try:
            subprocess.run(['age', '--decrypt', '--identity', str(identity), str(ciphertext)],
                           stdout=out, stderr=subprocess.DEVNULL, check=True, timeout=timeout)
        except subprocess.TimeoutExpired:
            raise DecryptionTimeout('protected input decryption timed out') from None
        except subprocess.CalledProcessError:
            raise DecryptionError('protected input identity or ciphertext rejected') from None
    loaded = trec.materialize(archive, pathlib.Path(directory) / 'input')
    if trec.fingerprint(loaded) != entry['fingerprint']:
        raise ValueError('protected content fingerprint mismatch')
    return loaded
