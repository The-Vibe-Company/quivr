"""Import a human spot-check into an aggregate-only quality report."""
import argparse
import hashlib
import json
import os
import pathlib
import subprocess

import news_set


def update(report, encrypted_sample, reviewed_csv, identity):
    news_set.validate_report(report, published=True)
    artifact = report['artifacts']['review']
    if hashlib.sha256(encrypted_sample).hexdigest() != artifact['sha256']:
        raise news_set.BuildError('review sample ciphertext checksum mismatch')
    result = subprocess.run(['age', '--decrypt', '--identity', str(identity)], input=encrypted_sample,
                            capture_output=True, timeout=60)
    if result.returncode or hashlib.sha256(result.stdout).hexdigest() != artifact['plaintext_sha256']:
        raise news_set.BuildError('review sample decryption or checksum failed')
    human = news_set.review_result(result.stdout.decode('utf-8'), reviewed_csv)
    if human['sampled'] != 100 or human['reviewed'] != 100:
        raise news_set.BuildError('all 100 exported judgments must be reviewed')
    updated = {**report, 'human_check': human}
    news_set.validate_report(updated, published=True)
    return updated


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--report', required=True, type=pathlib.Path)
    parser.add_argument('--sample', required=True, type=pathlib.Path, help='original encrypted review CSV')
    parser.add_argument('--reviewed', required=True, type=pathlib.Path, help='private completed CSV')
    parser.add_argument('--out', required=True, type=pathlib.Path)
    args = parser.parse_args(argv)
    if args.out.exists():
        parser.error('output exists; choose a new filename')
    identity = os.environ.get('QUIVR_NEWS_REVIEW_IDENTITY')
    if not identity:
        parser.error('provide QUIVR_NEWS_REVIEW_IDENTITY, a private age identity file path')
    try:
        report = update(json.loads(args.report.read_text()), args.sample.read_bytes(),
                        args.reviewed.read_text(), identity)
        args.out.parent.mkdir(parents=True, exist_ok=True)
        with args.out.open('x') as output:
            json.dump(report, output, indent=2, sort_keys=True, allow_nan=False)
            output.write('\n')
        print('100 human judgments validated; aggregate report written.')
        return 0
    except Exception:
        print('Human review import failed; check the sample, key, checksums and all 100 grades.')
        return 2


if __name__ == '__main__':
    raise SystemExit(main())
