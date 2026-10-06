"""Keep migrations collision-free and ordered after main.

New migrations are named migrations/<UTC YYYYMMDDTHHMMZ>_<slug>.sql. The
numbered 0xx_ files are a closed legacy set (enforced by migrations_test.go).
Migrations apply in lexical filename order, so a migration added by a branch
must sort after the latest migration already on main; otherwise fresh and
upgraded databases would apply them in different orders.

    python3 scripts/migrations.py check          # guard used by make verify
    python3 scripts/migrations.py new <slug>     # create a stamped migration
    python3 scripts/migrations.py restamp <file> # move a migration after main

The base defaults to origin/main (override with MIGRATIONS_BASE). Only the
base tree is read, so a shallow `git fetch --depth=1 origin main` suffices.
"""
import datetime
import os
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
DIRECTORY = 'migrations'
STAMP = '%Y%m%dT%H%MZ'
STAMPED = re.compile(r'^([0-9]{8}T[0-9]{4}Z)_([a-z0-9_]+)\.sql$')
NUMBERED = re.compile(r'^([0-9]{3})_([a-z0-9_]+)\.sql$')
SLUG = re.compile(r'^[a-z0-9_]+$')


class BaseUnavailable(Exception):
    pass


def slug_of(name):
    match = STAMPED.match(name) or NUMBERED.match(name)
    if match:
        return match.group(2)
    slug = re.sub(r'[^a-z0-9_]+', '_', name.removesuffix('.sql').lower()).strip('_')
    return re.sub(r'^[0-9]+_?', '', slug) or 'migration'


def stamps_after(latest, now, count):
    """Return count distinct UTC stamps, all after latest and not before now."""
    start = now.astimezone(datetime.timezone.utc).replace(second=0, microsecond=0)
    match = STAMPED.match(latest or '')
    if match:
        floor = datetime.datetime.strptime(match.group(1), STAMP).replace(tzinfo=datetime.timezone.utc)
        start = max(start, floor + datetime.timedelta(minutes=1))
    return [(start + datetime.timedelta(minutes=i)).strftime(STAMP) for i in range(count)]


def ordering_key(name):
    return name.split('_', 1)[0]


def missing(base, head):
    """Migrations on main absent here: the branch is behind main, or removed one."""
    return sorted(set(base) - set(head))


def problems(base, head, now):
    """List guard failures for head's migrations against base's, with fixes."""
    base, head = sorted(base), sorted(head)
    found = []
    added = [name for name in head if name not in base]
    latest = base[-1] if base else None
    misplaced = []
    for name in added:
        if not STAMPED.match(name):
            misplaced.append((name, 'new migrations must be named <UTC YYYYMMDDTHHMMZ>_<slug>.sql (numbered 0xx_ files are closed)'))
        elif latest is not None and ordering_key(name) <= ordering_key(latest):
            misplaced.append((name, f"it does not sort after the latest migration on main (main's latest migration is {latest}), "
                                    'so fresh and upgraded databases would apply them in different orders'))
    if misplaced:
        # Restamp every added file so the branch keeps its own relative order.
        targets = dict(zip(added, stamps_after(latest, now, len(added))))
        single = len(added) == 1
        for name, reason in misplaced:
            new = f'{targets[name]}_{slug_of(name)}.sql'
            alternative = (f' (or make migration-restamp file={name}); no other file needs to change.'
                           if single else '; apply every git mv printed here to keep your migrations in order.')
            found.append(f'{name}: {reason}. Fix: git mv {DIRECTORY}/{name} {DIRECTORY}/{new}{alternative}')
        flagged = {name for name, _ in misplaced}
        for name in added:
            if name not in flagged:
                new = f'{targets[name]}_{slug_of(name)}.sql'
                found.append(f'{name}: restamp it too to keep your migrations in order. '
                             f'Fix: git mv {DIRECTORY}/{name} {DIRECTORY}/{new}')
    return found


def base_ref():
    return os.environ.get('MIGRATIONS_BASE', 'origin/main')


def base_names(root, ref):
    resolved = subprocess.run(['git', 'rev-parse', '--verify', '--quiet', f'{ref}^{{commit}}'],
                              cwd=root, capture_output=True, text=True)
    if resolved.returncode != 0:
        raise BaseUnavailable(
            f'cannot resolve {ref} to compare migrations against; run '
            f'`git fetch --no-tags --depth=1 origin main` (verify.yml does this) or set MIGRATIONS_BASE')
    listed = subprocess.run(['git', 'ls-tree', '--name-only', resolved.stdout.strip(), f'{DIRECTORY}/'],
                            cwd=root, capture_output=True, text=True, check=True)
    return sorted(p.split('/', 1)[1] for p in listed.stdout.split() if p.endswith('.sql'))


def head_names(root):
    return sorted(p.name for p in (root / DIRECTORY).glob('*.sql'))


def latest_known(root):
    names = head_names(root)
    try:
        names += base_names(root, base_ref())
    except BaseUnavailable:
        pass
    return max(names) if names else None


def check(root):
    try:
        base = base_names(root, base_ref())
    except BaseUnavailable as error:
        print(f'migrations: {error}', file=sys.stderr)
        return 1
    head = head_names(root)
    for name in missing(base, head):
        print(f'migrations: note: {name} is on main but not here; rebase if you are behind main', file=sys.stderr)
    found = problems(base, head, datetime.datetime.now(datetime.timezone.utc))
    from migration_policy import check as policy_check
    found.extend(policy_check(root, base_ref(), set(base)))
    for problem in found:
        print(f'migrations: {problem}', file=sys.stderr)
    return 1 if found else 0


def new(root, slug):
    if not SLUG.match(slug or ''):
        print('migrations: usage: make migration name=<lowercase_slug>', file=sys.stderr)
        return 2
    [stamp] = stamps_after(latest_known(root), datetime.datetime.now(datetime.timezone.utc), 1)
    path = root / DIRECTORY / f'{stamp}_{slug}.sql'
    path.write_text(f'-- {slug}\n-- Expand only: keep the previous binary compatible. See docs/agents/migrations.md.\n')
    print(path.relative_to(root))
    return 0


def restamp(root, name):
    name = pathlib.Path(name or '').name
    if not (root / DIRECTORY / name).is_file():
        print('migrations: usage: make migration-restamp file=<migration file name>', file=sys.stderr)
        return 2
    others = [n for n in head_names(root) if n != name]
    try:
        others += base_names(root, base_ref())
    except BaseUnavailable as error:
        print(f'migrations: {error}', file=sys.stderr)
        return 1
    latest = max(others) if others else None
    if STAMPED.match(name) and (latest is None or ordering_key(name) > ordering_key(latest)):
        print(f'{DIRECTORY}/{name} already sorts after every other migration; nothing to do')
        return 0
    [stamp] = stamps_after(latest, datetime.datetime.now(datetime.timezone.utc), 1)
    target = f'{stamp}_{slug_of(name)}.sql'
    tracked = subprocess.run(['git', 'ls-files', '--error-unmatch', f'{DIRECTORY}/{name}'],
                             cwd=root, capture_output=True).returncode == 0
    if tracked:
        subprocess.run(['git', 'mv', f'{DIRECTORY}/{name}', f'{DIRECTORY}/{target}'], cwd=root, check=True)
    else:
        (root / DIRECTORY / name).rename(root / DIRECTORY / target)
    print(f'{DIRECTORY}/{target}')
    return 0


def main(argv):
    command = argv[1] if len(argv) > 1 else 'check'
    argument = argv[2] if len(argv) > 2 else ''
    if command == 'check':
        return check(ROOT)
    if command == 'new':
        return new(ROOT, argument)
    if command == 'restamp':
        return restamp(ROOT, argument)
    print(__doc__, file=sys.stderr)
    return 2


if __name__ == '__main__':
    sys.exit(main(sys.argv))
