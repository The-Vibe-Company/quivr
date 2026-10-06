"""Modal transport for oss_bakeoff; importing or previewing never launches a job."""
import pathlib
import queue
import sys

import modal

STARTUP_TIMEOUT = 60


class JobFailed(Exception):
    """Sanitized failed-job identity for the local campaign artifact."""
    def __init__(self, app_id, error_type):
        # Exception text may contain provider inputs or credentials.
        super().__init__(f'Modal job failed ({error_type}); inspect operator console')
        self.app_id = app_id
        self.reason = {'kind': 'timeout' if error_type in ('TimeoutError', 'FunctionTimeoutError') else 'provider_error',
                       'error_type': error_type}


def remote_measure(label, hardware, sets, git_sha, max_tokens, timeout, restricted, started, reference=None):
    started.put(True)
    # Modal serialization imports this module by name; the repository is mounted
    # at the same fixed path in every container, without credentials or .git.
    sys.path.insert(0, '/workspace/scripts/eval')
    from oss_bakeoff import measure
    result = measure(label, hardware, sets, git_sha, max_tokens, timeout, restricted, reference)
    result['campaign']['modal_call_id'] = modal.current_function_call_id()
    return result


def dispatch(label, hardware, sets, git_sha, max_tokens, timeout, restricted, reference=None):
    from oss_bakeoff import image_for, report_campaign
    from embeddinggemma_server import LABELS
    # Only the local dispatcher needs the checkout. Modal imports this module
    # from /root/oss_modal.py, which has no repository-relative parent layout.
    root = pathlib.Path(__file__).resolve().parents[2]
    app = modal.App('quivr-embedding-measurement')
    gemma = label in LABELS
    base = (modal.Image.debian_slim(python_version='3.12') if gemma else
            modal.Image.from_registry(image_for(hardware), add_python='3.12').entrypoint([]))
    wheel = 'cu124' if gemma and hardware == 'L4' else 'cpu'
    image = (base
             .pip_install('torch==2.6.0', index_url='https://download.pytorch.org/whl/' + wheel)
             .env({'OMP_NUM_THREADS': '4', 'RAYON_NUM_THREADS': '4', 'TOKENIZERS_PARALLELISM': 'false',
                   'HF_HUB_DISABLE_TELEMETRY': '1', 'DO_NOT_TRACK': '1'})
             .add_local_dir(root / 'scripts/eval', '/workspace/scripts/eval', copy=True,
                            ignore=['test_*.py', '__pycache__'])
             .add_local_dir(root / 'plugins/hosted-embed/examples', '/workspace/plugins/hosted-embed/examples', copy=True)
             .add_local_file(root / 'plugins/core-ingest/profile.json', '/workspace/plugins/core-ingest/profile.json', copy=True)
             # Modal's requirements helper copies only the top-level file;
             # install here so relative -r requirements.txt resolves correctly.
             .run_commands('python -m pip install -r /workspace/scripts/eval/requirements-direct.txt'))
    if gemma:
        image = image.pip_install('transformers==5.19.0', 'sentence-transformers==6.1.0')
    worker = app.function(image=image, gpu=None if hardware == 'cpu' else 'L4',
                          cpu=(4, 4), memory=(8192, 8192), timeout=timeout,
                          startup_timeout=STARTUP_TIMEOUT,
                          max_containers=1, scaledown_window=2, retries=0)(remote_measure)
    # Ephemeral App.run scope ends on success, failure, or keyboard interruption.
    try:
        with app.run(), modal.Queue.ephemeral() as started:
            call = worker.spawn(label, hardware, sets, git_sha, max_tokens, timeout, restricted, started, reference)
            try:
                try:
                    started.get(timeout=STARTUP_TIMEOUT)
                except queue.Empty:
                    # Prefer the actual remote error if one is already available.
                    # Import failures can produce no result at all: never wait
                    # the full measurement lifetime for a function to start.
                    call.get(timeout=0)
                    raise TimeoutError('Modal function did not signal startup')
                result = call.get(timeout=timeout)
            except (TimeoutError, modal.exception.FunctionTimeoutError):
                call.cancel(terminate_containers=True)
                raise
            result['campaign']['modal_app_id'] = app.app_id
            for report in result['reports']:
                report['serving_campaign'] = report_campaign(result['campaign'])
            return result
    except Exception as error:
        raise JobFailed(app.app_id, type(error).__name__) from None
