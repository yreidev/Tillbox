import importlib.util
import json
import unittest
import unittest.mock
from pathlib import Path

spec = importlib.util.spec_from_file_location('monitor', Path(__file__).with_name('tillbox-monitor.py'))
monitor = importlib.util.module_from_spec(spec)
spec.loader.exec_module(monitor)


class MonitorTest(unittest.TestCase):
    def test_trigger_recovery_and_reminder(self):
        now = 100_000

        def ledgers(*states):
            return [{'name': f'经营码 {i}', 'state': json.dumps(st)} for i, st in enumerate(states, 1)]

        healthy = {'last_ok_at': now * 1000, 'failures': 0}
        data = {
            'ledgers': ledgers(healthy, healthy),
            'maintenance': json.dumps({'last_ok_at': now * 1000}),
            'retention': '{}',
            'payment': '{}',
            'dead': 0,
        }
        self.assertEqual(monitor.evaluate_db(data, now), {})
        data['dead'] = 2
        data['ledgers'] = ledgers(healthy, {'last_ok_at': (now - 601) * 1000, 'failures': 3, 'backfill_failures': 3})
        issues = monitor.evaluate_db(data, now)
        self.assertEqual(set(issues), {'ledger_failures', 'ledger_stale', 'backfill_failures', 'notifications'})
        self.assertTrue(issues['ledger_failures'].endswith('经营码 2'))
        # A newly enabled channel that has never polled successfully counts as stale.
        data['dead'] = 0
        data['ledgers'] = ledgers(healthy, {})
        self.assertEqual(set(monitor.evaluate_db(data, now)), {'ledger_stale'})
        # No enabled QR channel (for example only upstream channels): nothing to poll.
        data['ledgers'] = []
        self.assertEqual(monitor.evaluate_db(data, now), {})
        old = {'issues': issues, 'last_sent': {k: now for k in issues}}
        self.assertEqual(monitor.changes(issues, old, now + 60), ({}, {}))
        self.assertEqual(monitor.changes(issues, old, now + 3601)[0], issues)
        self.assertEqual(monitor.changes({}, old, now + 60)[1], issues)

    def test_backup_and_disk(self):
        cron = {'spec': {}, 'status': {'lastSuccessfulTime': '2026-10-16T00:00:00Z'}}
        now = monitor.timestamp('2026-10-16T02:00:00Z')
        self.assertEqual(monitor.evaluate_backup(cron, [], now), {})
        self.assertIn('backup', monitor.evaluate_backup(cron, [], now + 27 * 3600))
        cron['spec']['suspend'] = True
        self.assertIn('backup', monitor.evaluate_backup(cron, [], now))
        self.assertEqual(monitor.evaluate_disk(20 * 1024**3, 100 * 1024**3), {})
        self.assertIn('disk', monitor.evaluate_disk(5 * 1024**3, 100 * 1024**3))

    def test_automation_and_manual_support(self):
        now = 100_000
        data = {
            'ledgers': [],
            'maintenance': '{}',
            'retention': '{"enabled":false}',
            'payment': '{}',
            'dead': 0,
            'automation': '{"enabled":true,"interval_seconds":60}',
            'automation_state': json.dumps({'last_run_at': (now - 601) * 1000, 'last_error': 'failure'}),
            'assistance_manual': 2,
        }
        self.assertEqual(set(monitor.evaluate_db(data, now)), {'automation', 'automation_stale', 'assistance_manual'})
        data['automation_state'] = json.dumps({'last_run_at': now * 1000})
        data['assistance_manual'] = 0
        self.assertEqual(monitor.evaluate_db(data, now), {})

    def test_alert_target_from_environment(self):
        with unittest.mock.patch.dict(
            'os.environ',
            {'TILLBOX_MONITOR_TELEGRAM_TOKEN': ' t ', 'TILLBOX_MONITOR_TELEGRAM_CHAT_IDS': '1, 2,'},
            clear=True,
        ):
            self.assertEqual(monitor.alert_target(), ('t', ['1', '2']))
        with unittest.mock.patch.dict('os.environ', {}, clear=True):
            self.assertEqual(monitor.alert_target(), ('', []))


if __name__ == '__main__':
    unittest.main()
