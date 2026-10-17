import importlib.util
import json
import sqlite3
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

    def test_sql_with_saved_settings(self):
        # 用巡检的真实 SQL 查一个最小的库：保存过 AI 配置后，automation 字段是嵌套对象而不是字符串
        now = 100_000
        with sqlite3.connect(':memory:') as conn:
            conn.executescript("""
                CREATE TABLE channels (id INTEGER PRIMARY KEY, type TEXT, name TEXT, enabled INTEGER);
                CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT);
                CREATE TABLE notifications (status TEXT);
                CREATE TABLE assistance_cases (status TEXT);
            """)
            conn.execute("INSERT INTO channels VALUES (1, 'alipay_qr', '支付宝经营码', 1)")
            conn.execute("INSERT INTO channels VALUES (2, 'upstream_epay', '上游', 1)")
            healthy = json.dumps({'last_ok_at': now * 1000, 'failures': 0})
            for key, value in [
                ('ledger_state:ch1', healthy),
                ('maintenance_state', json.dumps({'last_ok_at': now * 1000})),
                ('automation_state', json.dumps({'last_run_at': now * 1000})),
            ]:
                conn.execute('INSERT INTO settings VALUES (?, ?)', (key, value))

            def query():
                return json.loads(conn.execute(monitor.SQL).fetchone()[0])

            self.assertEqual(monitor.evaluate_db(query(), now), {})
            for enabled in (False, True):
                with self.subTest(enabled=enabled):
                    conn.execute(
                        'INSERT OR REPLACE INTO settings VALUES (?, ?)',
                        ('automation', json.dumps({'enabled': enabled, 'interval_seconds': 60})),
                    )
                    self.assertEqual(monitor.evaluate_db(query(), now), {})
            conn.execute(
                'UPDATE settings SET value = ? WHERE key = ?',
                (json.dumps({'last_run_at': (now - 601) * 1000, 'last_error': 'timeout'}), 'automation_state'),
            )
            conn.execute("INSERT INTO assistance_cases VALUES ('manual')")
            conn.execute('UPDATE settings SET value = ? WHERE key = ?', ('{"failures":3}', 'ledger_state:ch1'))
            self.assertEqual(
                set(monitor.evaluate_db(query(), now)),
                {'automation', 'automation_stale', 'assistance_manual', 'ledger_failures', 'ledger_stale'},
            )

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
