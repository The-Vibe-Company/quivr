"""Unit checks for the gate of make measure-upgrade (THE-786)."""
import copy
import unittest

import measure_upgrade as m


def passing():
    """A report in which every exit criterion holds."""
    record = {'key': 'k1', 'receipt_id': 'r1', 'record_id': 'rec1', 'phase': 'upgrade', 'searchable': True, 'current': True, 'enriched': True,
              'versions': 1, 'hits': ['rec1'], 'segmented_by': '0.2.0', 'expected_version': '0.2.0'}
    return {'records': [record], 'quarantined': 0, 'samples': [{'ok': True, 'restart': False}], 'client_failures': [],
            'expectations': {'upgrade': {'since': 1.0, 'version': '0.2.0'}},
            'drains': [{'name': 'a_after_upgrade', 'inactive': True, 'seconds': 12.0, 'worker_killed': True,
                        'at_switch': {'state': 'draining', 'pinned_work': 2}, 'restart_while_draining': True}],
            'backfill': {'state': 'succeeded', 'counters': {'versions_in_scope': 3, 'versions_done': 3}, 'estimated_versions': 3, 'window_records': 3,
                         'state_at_restart': 'running'},
            'plans_after_restart': [{'want': 'plan_1', 'got': 'plan_1'}]}


def failing(report):
    return sorted(c['name'] for c in m.verdict(report) if not c['ok'])


class Verdict(unittest.TestCase):
    def test_a_clean_run_passes(self):
        self.assertEqual(failing(passing()), [])

    def test_failures_count_only_outside_api_restarts(self):
        r = passing()
        r['samples'].append({'ok': False, 'restart': True, 'status': None})
        r['client_failures'].append({'restart': True, 'status': None})
        self.assertEqual(failing(r), [])
        r['samples'].append({'ok': False, 'restart': False, 'status': 503})
        self.assertEqual(failing(r), ['api_available'])

    def test_lost_and_duplicated_records_fail(self):
        r = passing()
        lost = {'key': 'k2'}
        twice = dict(copy.deepcopy(r['records'][0]), key='k3', hits=['rec1', 'rec1'])
        r['records'] += [lost, twice]
        self.assertEqual(failing(r), ['every_record_accepted', 'exactly_once'])

    def test_work_on_the_drained_version_fails(self):
        r = passing()
        r['records'][0]['segmented_by'] = '0.1.0'
        self.assertEqual(failing(r), ['new_work_on_active_version'])

    def test_a_phase_with_no_work_after_its_drain_proves_nothing(self):
        r = passing()
        del r['records'][0]['expected_version']
        self.assertEqual(failing(r), ['new_work_on_active_version'])

    def test_a_switch_with_nothing_in_flight_proves_nothing(self):
        r = passing()
        r['drains'][0]['at_switch'] = {'state': 'inactive', 'pinned_work': 0}
        self.assertEqual(failing(r), ['transitions_under_load'])

    def test_a_dead_worker_holding_a_drain_past_its_heartbeat_timeout_fails(self):
        r = passing()
        r['drains'][0]['seconds'] = 31.7
        self.assertEqual(failing(r), ['drain_after_worker_kill_bounded'])
        r['drains'][0]['worker_killed'] = False
        self.assertEqual(failing(r), [])


if __name__ == '__main__':
    unittest.main()
