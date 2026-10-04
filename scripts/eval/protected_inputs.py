"""Verified encrypted TREC inputs; callers own ephemeral storage and admission."""
import pathlib
import subprocess

import trec


def decrypt(entry, ciphertext, identity, directory):
    """Verify ciphertext before age, then verify the extracted TREC fingerprint."""
    if trec.sha256_file(ciphertext) != entry['digest']:
        raise ValueError('ciphertext checksum mismatch')
    archive = pathlib.Path(directory) / 'input.tar.gz'
    with archive.open('wb') as out:
        subprocess.run(['age', '--decrypt', '--identity', str(identity), str(ciphertext)],
                       stdout=out, stderr=subprocess.DEVNULL, check=True, timeout=60)
    loaded = trec.materialize(archive, pathlib.Path(directory) / 'input')
    if trec.fingerprint(loaded) != entry['fingerprint']:
        raise ValueError('protected content fingerprint mismatch')
    return loaded
