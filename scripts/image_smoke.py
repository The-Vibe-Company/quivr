"""Exercise an actual image as its default user with a read-only root.

Release images run this check before signing. It needs Docker, but no paid
providers or infrastructure stack. Plugins must serve their exact manifest;
the engine must print its build identity without configuration.
"""
import argparse
import hashlib
import json
import pathlib
import subprocess
import tarfile
import time
import urllib.error
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parent.parent


def docker(*args):
    return subprocess.check_output(['docker', *args], text=True).strip()


def check(args):
    docker('pull', args.image) if '@sha256:' in args.image else None
    config = json.loads(docker('image', 'inspect', args.image))[0]['Config']
    if config['User'] != '10001:10001' or '/tmp' not in config.get('Volumes', {}):
        raise RuntimeError('image must default to UID/GID 10001 and declare /tmp')
    container = docker('create', '--read-only', '--tmpfs', '/tmp:rw,nosuid,nodev,size=64m',
                       '-p', '127.0.0.1::8080', args.image,
                       *([] if args.plugin else ['--version']))
    try:
        # Inspect the real filesystem, including inherited base layers.
        process = subprocess.Popen(['docker', 'export', container], stdout=subprocess.PIPE)
        try:
            with tarfile.open(fileobj=process.stdout, mode='r|') as archive:
                for member in archive:
                    path = pathlib.PurePosixPath(member.name)
                    if (path.name in {'go', 'gcc', 'g++', 'cc', 'make', 'pip', 'pip3', 'pip3.12'}
                            and any(part in {'bin', 'sbin'} for part in path.parts)):
                        raise RuntimeError(f'build tool in runtime: {path}')
        finally:
            process.stdout.close()
            if process.wait() != 0:
                raise RuntimeError('cannot inspect image filesystem')
        if not args.plugin:
            output = docker('start', '--attach', container)
            expected = f'quivr {args.version} (revision {args.revision}; API v0; plugin engine 0.2.0)'
            if output != expected:
                raise RuntimeError(f'build identity: {output!r}, expected {expected!r}')
            if docker('inspect', '--format', '{{.State.ExitCode}}', container) != '0':
                raise RuntimeError('version command failed')
            print(output)
            return
        docker('start', container)
        port = docker('port', container, '8080/tcp')
        deadline = time.monotonic() + 20
        while True:
            try:
                with urllib.request.urlopen(f'http://{port}/v0/discovery', timeout=1) as response:
                    discovery = json.load(response)
                break
            except (OSError, urllib.error.URLError):
                if time.monotonic() >= deadline or docker('inspect', '--format', '{{.State.Running}}', container) != 'true':
                    raise RuntimeError(f'plugin did not serve discovery: {docker("logs", container)}')
        manifest = ROOT / 'plugins' / args.plugin / 'quivr-plugin.yaml'
        digest = 'sha256:' + hashlib.sha256(manifest.read_bytes()).hexdigest()
        if discovery['manifest_digest'] != digest:
            raise RuntimeError(f'image serves a different manifest: {discovery}')
        with urllib.request.urlopen(f'http://{port}/v0/health', timeout=2) as response:
            if response.status != 200:
                raise RuntimeError('plugin health failed')
        print(f'{args.plugin}: read-only discovery and health passed ({digest})')
    finally:
        docker('rm', '--force', container)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('image')
    parser.add_argument('--plugin', default='')
    parser.add_argument('--version', required=True)
    parser.add_argument('--revision', required=True)
    check(parser.parse_args())
