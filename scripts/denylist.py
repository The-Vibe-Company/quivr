"""Fail when a denylisted term appears in the repository or in piped text.

The denylist stores salted SHA-256 digests, never the terms themselves, so the
list can live in a public repository. Terms are compared as whole words after
splitting on punctuation, and also on case and digit changes, one or two words
long. File paths are checked as well as file contents.

    python3 scripts/denylist.py                 # scan tracked and untracked files
    python3 scripts/denylist.py --stdin < msg   # check a commit message or PR body
    python3 scripts/denylist.py --hash "term"   # print the line to add to the list

Hashing hides the terms from casual reading and search, not from a brute-force
guess of short words.
"""
import argparse
import hashlib
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
DENYLIST = ROOT / 'scripts' / 'denylist.sha256'
SALT = 'quivr-denylist:'
MAX_WORDS = 2
_CASE = [
    (re.compile(r'([a-z0-9])([A-Z])'), r'\1 \2'),
    (re.compile(r'([A-Z]+)([A-Z][a-z])'), r'\1 \2'),
    (re.compile(r'([A-Za-z])([0-9])'), r'\1 \2'),
    (re.compile(r'([0-9])([A-Za-z])'), r'\1 \2'),
]
_WORD = re.compile(r'[A-Za-z0-9]+')
# Checksums are opaque, not prose: go.sum/lock-file digests and long standalone
# base64 or hex tokens. Paths and identifiers keep being split into words.
_CHECKSUM = re.compile(r'(?:\bh1:|\bsha(?:1|256|384|512)-)[A-Za-z0-9+/]+=*')
_BLOB = re.compile(r'''(?<![^\s"'`])[A-Za-z0-9+/]{40,}=*(?![^\s"'`,])''')


def _opaque(text):
    text = _CHECKSUM.sub(' ', text)
    # Random base64 has many digits; long identifiers and paths rarely have four.
    return _BLOB.sub(lambda m: ' ' if len(re.findall(r'[0-9]', m.group())) >= 4 else m.group(), text)


def words(text):
    return [word.lower() for word in _WORD.findall(text)]


def tokens(text):
    text = _opaque(text)
    for pattern, repl in _CASE:
        text = pattern.sub(repl, text)
    return words(text)


def digest(term):
    return hashlib.sha256((SALT + ' '.join(words(term))).encode()).hexdigest()


def load(path):
    lines = pathlib.Path(path).read_text().splitlines()
    return {line.strip() for line in lines if line.strip() and not line.startswith('#')}


def find_in_text(text, hashes):
    for number, line in enumerate(text.splitlines(), 1):
        seen = set()
        # Check both "WordJoined" as one word and its case-split parts.
        for split in (words(line), tokens(line)):
            for size in range(1, MAX_WORDS + 1):
                for start in range(len(split) - size + 1):
                    term = ' '.join(split[start:start + size])
                    if term not in seen and digest(term) in hashes:
                        seen.add(term)
                        yield number, term


def files(root):
    out = subprocess.run(
        ['git', '-C', str(root), 'ls-files', '-z', '--cached', '--others', '--exclude-standard'],
        check=True, capture_output=True,
    ).stdout
    return [pathlib.Path(name) for name in out.decode().split('\0') if name]


def scan(root, hashes):
    root = pathlib.Path(root)
    found = []
    for rel in files(root):
        for _, term in find_in_text(str(rel), hashes):
            found.append((rel, 0, term))
        path = root / rel
        if not path.is_file() or path.is_symlink():
            continue
        data = path.read_bytes()
        if b'\0' in data:
            continue
        for number, term in find_in_text(data.decode('utf-8', 'replace'), hashes):
            found.append((rel, number, term))
    return found


def main(argv=None, stdin=sys.stdin, stdout=sys.stdout):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--denylist', default=str(DENYLIST))
    parser.add_argument('--root', default=str(ROOT))
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument('--stdin', action='store_true', help='check text read from standard input')
    mode.add_argument('--hash', metavar='TERM', help='print the digest line for TERM')
    args = parser.parse_args(argv)
    if args.hash is not None:
        if not 1 <= len(words(args.hash)) <= MAX_WORDS:
            parser.error(f'a term must be 1 to {MAX_WORDS} words')
        print(digest(args.hash), file=stdout)
        return 0
    hashes = load(args.denylist)
    if args.stdin:
        found = [('<stdin>', n, t) for n, t in find_in_text(stdin.read(), hashes)]
    else:
        found = scan(args.root, hashes)
    for path, number, term in found:
        print(f'{path}:{number}: denylisted term "{term}"', file=stdout)
    if found:
        print(f'{len(found)} denylisted occurrence(s); see scripts/denylist.py', file=stdout)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
