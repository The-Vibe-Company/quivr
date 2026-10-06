"""Tracked Modal adapter. Creation intents survive lost app-ID acknowledgements."""
import json
import logging
import pathlib
import subprocess
import sys
import time

import campaign_store
import modal_search
import network_recovery

log = logging.getLogger(__name__)


def unreachable(error):
    if isinstance(error, subprocess.TimeoutExpired):
        return True
    if isinstance(error, subprocess.CalledProcessError):
        message = error.stderr or b''
        if isinstance(message, bytes):
            message = message.decode('utf-8', errors='replace')
        return any(marker in message.lower() for marker in (
            'connectionerror', 'deadline exceeded', 'temporary failure in name resolution',
            'nodename nor servname provided', 'network is unreachable', 'connection reset',
            'connection refused', 'tls handshake', 'failed to connect'))
    return False


class ModalCompute:
    def apps(self):
        # Capture provider output: reflected credentials never reach CLI output.
        try:
            result = network_recovery.retry(lambda: subprocess.run(
                [sys.executable, '-m', 'modal', 'app', 'list', '--json'],
                capture_output=True, text=True, timeout=30, check=True), unreachable)
            rows = json.loads(result.stdout)
            if not isinstance(rows, list):
                raise ValueError('invalid app listing')
            return rows
        except network_recovery.Outage:
            raise
        except Exception:
            raise campaign_store.CleanupPending('Modal resource listing unavailable') from None

    def find(self, label):
        return [row['app_id'] for row in self.apps() if row.get('description') == label]

    def running(self, app_id):
        matches = [row for row in self.apps() if row.get('app_id') == app_id]
        # Missing registered IDs do not count as acknowledged termination.
        if len(matches) != 1:
            raise campaign_store.CleanupPending('Modal app termination is not visible')
        return matches[0].get('state') != 'stopped' or str(matches[0].get('tasks')) != '0'

    def stop(self, app_id):
        if not self.running(app_id):
            return
        try:
            network_recovery.retry(lambda: subprocess.run(
                [sys.executable, '-m', 'modal', 'app', 'stop', '--yes', app_id],
                capture_output=True, timeout=30, check=True), unreachable)
        except network_recovery.Outage:
            raise
        except subprocess.CalledProcessError as error:
            message = error.stderr or b''
            if isinstance(message, bytes):
                message = message.decode('utf-8', errors='replace')
            _, summary = modal_search.failure_summary(RuntimeError(message))
            log.warning('Modal stop failed: %s', summary)
            # Detached apps can finish between the first listing and stop.
            # A failed command is acknowledged only by a fresh terminal listing.
            if not self.running(app_id):
                return
            raise campaign_store.CleanupPending('Modal stop acknowledgement unavailable') from None
        except Exception:
            raise campaign_store.CleanupPending('Modal stop acknowledgement unavailable') from None
        deadline = time.monotonic() + 30
        while self.running(app_id):
            if time.monotonic() >= deadline:
                raise campaign_store.CleanupPending('Modal termination is still pending')
            time.sleep(1)


class Measurement:
    def __init__(self, store, name, owner, outbox, compute=None):
        self.store, self.name, self.owner = store, name, owner
        self.outbox = pathlib.Path(outbox)
        self.compute = compute or ModalCompute()

    def __call__(self, config, *, on_report=lambda report: None):
        self.store.renew_owner(self.name, self.owner)
        state = self.store.snapshot(self.name)
        resource, attempted, report = None, False, None
        def app_name(slot):
            # Allocate an intent only after the campaign measurement slot admits us.
            nonlocal resource
            resource = self.store.intent(self.name, self.owner, lease=slot)
            return resource['label']
        def on_launch():
            nonlocal attempted
            # Entering SDK startup may already send AppCreate; failed entry
            # does not prove its absence. Preserve uncertain SDK attempts.
            attempted = True
        try:
            report = modal_search.launch(state['spec']['policy'], config, self.name, self.outbox, True,
                app_name=app_name, on_launch=on_launch,
                on_report=on_report,
                on_app=lambda app: self.store.bind(self.name, self.owner, resource['id'], app),
                check=lambda: self.store.renew_owner(self.name, self.owner), parallelism=state['spec']['parallelism'])
            return report
        except network_recovery.Outage:
            raise
        except Exception as error:
            if network_recovery.modal_unreachable(error):
                raise network_recovery.Outage('Modal app acknowledgement unavailable; reconcile before retrying') from None
            raise
        finally:
            # A bounded outage leaves detached compute and uncertain admissions
            # intact. The independent watchdog reconciles after ownership grace.
            # Do not spend another outage window on release/cleanup here.
            # Unknown creation/bind remains pending for the watchdog; local
            # failure before AppCreate can safely abandon its own intent.
            failure = sys.exc_info()[1]
            current = (self.store.snapshot(self.name)['resources'][resource['id']]
                       if resource and not isinstance(failure, network_recovery.Outage) else None)
            if resource and not attempted and not isinstance(failure, network_recovery.Outage):
                self.store.abandon_intent(self.name, self.owner, resource['id'])
            elif current and current['app_id']:
                try:
                    self.compute.stop(current['app_id'])
                    if self.compute.running(current['app_id']):
                        raise campaign_store.CleanupPending('Modal termination is still pending')
                    self.store.closed(self.name, resource['id'], owner=self.owner)
                except (campaign_store.CleanupPending, network_recovery.Outage):
                    # Canonical measurements are already durable. Let Loop persist
                    # the aggregate verdict before the supervisor drains compute.
                    # Also preserve an original measurement failure for its caller.
                    if report is None and failure is None:
                        raise
                    log.warning('Modal cleanup pending; resource retained for watchdog')
