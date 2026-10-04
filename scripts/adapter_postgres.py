#!/usr/bin/env python3
"""make adapter-postgres: the PostgreSQL adapter suite on a bare, migrated database (THE-699).

Starts only the pinned `postgres` service of deploy/compose/compose.yaml in a
Compose project of its own, with its own password, and runs
`go test ./internal/adapters/postgres/...` with QUIVR_ADAPTER_CONFIG naming
that database. The suite's TestMain applies the migrations and the default
projection generation (app.BootstrapDatabase, the PostgreSQL part of
`quivr migrate`). Logs stay in .scratch/<project>/; the project and its volume
are removed on success, failure and interrupt, unless QUIVR_KEEP_ON_FAILURE=1
keeps a failed one. Extra arguments go to `go test`, e.g. `-run TestDelivery`.
"""
import json, os, pathlib, secrets, signal, subprocess, sys, uuid

import verify_report

ROOT = pathlib.Path(__file__).resolve().parents[1]
GO = os.environ.get('GO', 'go')
COMPOSE = 'deploy/compose/compose.yaml'
PACKAGES = './internal/adapters/postgres/...'


class Project:
    def __init__(self):
        self.name = 'quivr-adapter-pg-' + uuid.uuid4().hex[:10]
        self.password = secrets.token_hex(24)
        self.directory = ROOT / '.scratch' / self.name
        self.directory.mkdir(parents=True, mode=0o700)
        self.directory.chmod(0o700)

    def compose(self, *args, **kwargs):
        # Compose interpolates every service of the shared file; only postgres starts,
        # so the roots the other services mount point at this run's directory.
        env = {**os.environ, 'QUIVR_DB_PASSWORD': self.password, 'QUIVR_LOCAL_ROOT': str(self.directory), 'QUIVR_MODEL_ROOT': str(self.directory)}
        return subprocess.run(['docker', 'compose', '-p', self.name, '-f', COMPOSE, *args], check=True, cwd=ROOT, env=env, **kwargs)

    def start(self):
        """Start PostgreSQL and write the adapter configuration: this database only."""
        self.compose('up', '-d', '--wait', '--wait-timeout', '120', 'postgres')
        address = self.compose('port', 'postgres', '5432', capture_output=True, text=True).stdout.strip()
        config = self.directory / 'config.json'
        config.write_text(json.dumps({'database_url': f'postgres://quivr:{self.password}@{address}/quivr?sslmode=disable'}))
        config.chmod(0o600)
        return config

    def test(self, config, args):
        """Run the suite, streaming its output to the terminal and adapter-postgres.log."""
        # QUIVR_ADAPTER_POSTGRES_ONLY lets the tests that also need the tokenizer, TEI or S3 skip.
        env = {**os.environ, 'QUIVR_ADAPTER_CONFIG': str(config), 'QUIVR_ADAPTER_POSTGRES_ONLY': '1'}
        with (self.directory / 'adapter-postgres.log').open('w') as log:
            proc = subprocess.Popen([GO, 'test', '-count=1', *args, PACKAGES], cwd=ROOT, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
            try:
                for line in proc.stdout:
                    sys.stdout.write(line)
                    sys.stdout.flush()
                    log.write(line)
                code = proc.wait()
            finally:
                if proc.poll() is None:
                    # go test forwards SIGINT to its test binary; SIGTERM would orphan it.
                    for stop in (lambda: proc.send_signal(signal.SIGINT), proc.terminate, proc.kill):
                        stop()
                        try:
                            proc.wait(timeout=10)
                            break
                        except subprocess.TimeoutExpired:
                            pass
        if code:
            raise subprocess.CalledProcessError(code, 'go test')

    def capture(self):
        with (self.directory / 'postgres.log').open('w') as log:
            self.compose('logs', '--no-color', 'postgres', stdout=log, stderr=log)
        with (self.directory / 'services.json').open('w') as out:
            self.compose('ps', '--all', '--format', 'json', stdout=out)

    def down(self):
        # `-p` scopes removal to this project's containers, network and volume.
        self.compose('down', '--volumes')
        # The database is gone; its credentials need not stay on disk.
        (self.directory / 'config.json').unlink(missing_ok=True)


def main(args):
    def interrupted(*_):
        raise verify_report.Interrupted()
    previous = signal.signal(signal.SIGTERM, interrupted)
    project = Project()
    status = 'failed'
    try:
        print(f'Project {project.name}; logs in {project.directory}', flush=True)
        project.test(project.start(), args)
        status = 'passed'
    except (KeyboardInterrupt, verify_report.Interrupted):
        status = 'interrupted'
    except Exception as error:
        print('adapter-postgres failed:', verify_report.bounded(error), file=sys.stderr)
    finally:
        # A second Ctrl+C or a CI cancellation must not abort capture or cleanup.
        ignored = {sig: signal.signal(sig, signal.SIG_IGN) for sig in (signal.SIGINT, signal.SIGTERM)}
        kept = status == 'failed' and os.environ.get('QUIVR_KEEP_ON_FAILURE') == '1'
        try:
            try:
                project.capture()
            except Exception as error:
                print('log capture failed:', verify_report.bounded(error), file=sys.stderr)
            if not kept:
                try:
                    project.down()
                except Exception as error:
                    print(f'cleanup of {project.name} failed:', verify_report.bounded(error), file=sys.stderr)
            verify_report.redact_tree(project.directory, [project.password])
        finally:
            for sig, handler in ignored.items():
                signal.signal(sig, handler)
            signal.signal(signal.SIGTERM, previous)
    print(f'adapter-postgres {status}; logs in {project.directory}')
    if kept:
        print(f'Kept for inspection (QUIVR_KEEP_ON_FAILURE=1). Remove it with: docker compose -p {project.name} -f {COMPOSE} down --volumes')
    return {'passed': 0, 'interrupted': 130}.get(status, 1)


if __name__ == '__main__':
    sys.exit(main(sys.argv[1:]))
