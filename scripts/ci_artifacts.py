"""Pack failure diagnostics into a small archive, without configurations or symlinks."""
import argparse
import io
import json
import os
from pathlib import Path
import tarfile

# Keep reports and log tails, not models, source bodies, configs or dependency trees.
SKIP_DIRS = {'node_modules', 'venv', '.venv', '.git', 'e5-model', 'tokenizer', 'query-encoder'}


def diagnostic(path):
    name = path.name.lower()
    if any(term in name for term in ('config', 'credential', 'secret', 'manifest', 'environment', 'services')):
        return False
    return ('.log' in path.suffixes or path.suffix in {'.jsonl', '.png', '.zip', '.txt', '.md'} or
            path.suffix == '.json' and any(term in name for term in ('vulnerabilities', 'report', 'readiness', 'inventory', 'metrics', 'journey', 'unittest', 'go-', 'guards', 'contracts', 'python', 'sdk-', 'eval-')))


def bundle(roots, output, max_files=200, max_bytes=20 * 1024 * 1024, file_bytes=1024 * 1024):
    candidates, scanned, scan_limited = [], 0, False
    for root in roots:
        root = Path(root)
        if root.is_symlink():
            continue
        if root.is_file():
            items = [(root.parent, [], [root.name])]
        else:
            items = os.walk(root, followlinks=False)
        for directory, directories, filenames in items:
            directory = Path(directory)
            directories[:] = sorted(d for d in directories if d not in SKIP_DIRS and not (directory / d).is_symlink())
            for name in sorted(filenames):
                scanned += 1
                if scanned > 10000:
                    scan_limited = True
                    break
                path = directory / name
                if path == output or path.is_symlink() or not path.is_file() or not diagnostic(path):
                    continue
                relative = str(Path(root.name) / path.relative_to(root)) if root.is_dir() else root.name
                priority = 0 if 'report' in name or 'readiness' in name else 1 if name.startswith(('worker', 'api', 'acceptance', 'browser', 'vulnerabilities')) else 2
                candidates.append((priority, relative, path))
            if scan_limited:
                break
        if scan_limited:
            break
    candidates.sort(key=lambda item: (item[0], item[1]))
    output.parent.mkdir(parents=True, exist_ok=True)
    included, truncated, total, omitted = [], [], 0, 0
    # Reserve bounded space for the index, so the cap includes metadata as well as evidence.
    reserve = min(65536, max_bytes // 4)
    budget = max_bytes - reserve
    with tarfile.open(output, 'w:gz') as archive:
        def add(name, data):
            info = tarfile.TarInfo(name)
            info.size = len(data)
            info.mode = 0o600
            archive.addfile(info, io.BytesIO(data))
        for _, name, path in candidates:
            limit = min(file_bytes, budget - total)
            if len(included) >= max_files or limit <= 0 or len(name.encode()) > 512:
                omitted += 1
                continue
            try:
                size = path.stat().st_size
                with path.open('rb') as source:
                    if size > limit and '.log' in path.suffixes:
                        source.seek(-limit, os.SEEK_END)
                    data = source.read(limit)
            except OSError:
                omitted += 1
                continue
            add(name, data)
            total += len(data)
            included.append(name)
            if size > len(data):
                truncated.append(name)
        index = {'included': included, 'truncated': truncated, 'omitted': omitted,
                 'scan_limited': scan_limited, 'evidence_bytes': total,
                 'limits': {'files': max_files, 'total_bytes': max_bytes, 'per_file_bytes': file_bytes}}
        data = json.dumps(index, separators=(',', ':')).encode()
        # Small custom limits are useful locally too; keep the index itself inside the cap.
        if len(data) > reserve:
            index['included'] = len(included)
            index['truncated'] = len(truncated)
            data = json.dumps(index, separators=(',', ':')).encode()
        if total + len(data) > max_bytes:
            raise ValueError('byte cap is too small for the evidence index')
        add('index.json', data)
    print(f'Failure bundle: {len(included)} files, {total} evidence bytes, {omitted} omitted, {len(truncated)} truncated')
    return index


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('roots', nargs='+', type=Path)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    bundle(args.roots, args.output)


if __name__ == '__main__':
    main()
