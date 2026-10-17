#!/usr/bin/env python3
"""Production checks for a Kubernetes (k3s) deployment.

--dry-run never sends alerts or updates deduplication state.

Deployment details come from the environment (systemd EnvironmentFile), see docs/operations.md:
  TILLBOX_MONITOR_URL               public readiness URL, e.g. https://pay.example.com/readyz (required)
  TILLBOX_MONITOR_KUBECTL           kubectl command, default "k3s kubectl"
  TILLBOX_MONITOR_NAMESPACE         default "tillbox"
  TILLBOX_MONITOR_DEPLOYMENT        default "tillbox"
  TILLBOX_MONITOR_DB                SQLite path inside the container, default /data/tillbox.db
  TILLBOX_MONITOR_BACKUP_CRONJOB    backup CronJob name, default "tillbox-backup"; empty skips the backup check
  TILLBOX_MONITOR_TELEGRAM_SECRET   "namespace/name" of a Secret with keys telegram-token and
                                    telegram-chat-ids (JSON array)
  TILLBOX_MONITOR_TELEGRAM_TOKEN / TILLBOX_MONITOR_TELEGRAM_CHAT_IDS
                                    the same values given directly (chat ids comma separated);
                                    used when no Secret is set
"""

import argparse
import base64
import datetime as dt
import json
import os
import shlex
import subprocess
import time
import urllib.request
from pathlib import Path

SQL = """SELECT json_object(
'ledgers',(SELECT json_group_array(json_object('name',c.name,'state',coalesce(s.value,'{}')))
  FROM channels c LEFT JOIN settings s ON s.key='ledger_state:ch'||c.id
  WHERE c.type='alipay_qr' AND c.enabled=1),
'maintenance',coalesce((SELECT value FROM settings WHERE key='maintenance_state'),'{}'),
'retention',coalesce((SELECT value FROM settings WHERE key='retention'),'{}'),
'payment',coalesce((SELECT value FROM settings WHERE key='payment'),'{}'),
'dead',(SELECT count(*) FROM notifications WHERE status='dead'),
'automation',coalesce((SELECT json_object('enabled',json_extract(value,'$.enabled'),
    'interval_seconds',json_extract(value,'$.interval_seconds')) FROM settings WHERE key='automation'),'{}'),
'automation_state',coalesce((SELECT value FROM settings WHERE key='automation_state'),'{}'),
'assistance_manual',(SELECT count(*) FROM assistance_cases WHERE status='manual'),
'monitor_state',coalesce((SELECT value FROM settings WHERE key='monitor_state'),'{}'),
'telegram_ready',(SELECT count(*) FROM telegram_chats) > 0
    AND coalesce((SELECT json_extract(value,'$.token') FROM settings WHERE key='telegram'),'') != '');
"""


def env(name, default=''):
    return os.environ.get(name, default).strip()


def kc(*args):
    result = subprocess.run(
        [*shlex.split(env('TILLBOX_MONITOR_KUBECTL', 'k3s kubectl')), '--request-timeout=20s', *args],
        capture_output=True,
        text=True,
        timeout=45,
    )
    if result.returncode:
        raise RuntimeError('Kubernetes check failed')
    return result.stdout


def timestamp(value):
    return dt.datetime.fromisoformat(value.replace('Z', '+00:00')).timestamp() if value else 0


def as_object(value):
    # 设置项是文本，要再解析一次；SQL 里用 json_object 拼出来的字段（automation）已经是对象
    return value if isinstance(value, dict) else json.loads(value or '{}')


def evaluate_alerting(state, now):
    # Tillbox 自己发业务告警时，这里只确认它的告警任务还在跑、发得出去。错误原文在后台监控页看，这里不转发。
    issues = {}
    if now - state.get('last_run_at', 0) / 1000 > 300:
        issues['alerting_stale'] = 'Tillbox 告警任务超过 5 分钟没有运行，业务告警可能发不出来'
    if state.get('send_error_at', 0) > state.get('sent_at', 0):
        issues['alerting_send'] = 'Tillbox 发不出 Telegram 告警，请到后台“监控”页查看原因'
    return issues


def evaluate_db(data, now):
    # 后台配好了 Telegram 并绑定了会话时，查账、通知、AI 处理、归档这些业务问题由 Tillbox 自己发，这里不重复发。
    if data.get('telegram_ready'):
        return evaluate_alerting(as_object(data.get('monitor_state')), now)
    issues = {}
    maintenance = as_object(data['maintenance'])
    retention = as_object(data['retention'])
    payment = as_object(data['payment'])
    # Every enabled Alipay QR channel has its own ledger poller and state.
    failing, backfill, stale = [], [], []
    for channel in data['ledgers']:  # json_group_array arrives as a real array
        ledger = as_object(channel['state'])
        if ledger.get('failures', 0) >= 3:
            failing.append(channel['name'])
        if ledger.get('backfill_failures', 0) >= 3:
            backfill.append(channel['name'])
        if now - ledger.get('last_ok_at', 0) / 1000 > max(300, payment.get('normal_interval', 60) * 3):
            stale.append(channel['name'])
    if failing:
        issues['ledger_failures'] = f'支付宝主窗口查账连续失败至少 3 次：{"、".join(failing)}'
    if backfill:
        issues['backfill_failures'] = f'支付宝历史补扫连续失败至少 3 次：{"、".join(backfill)}'
    if stale:
        issues['ledger_stale'] = f'支付宝查账成功时间超过阈值，请检查是否影响到账确认：{"、".join(stale)}'
    if data['dead']:
        issues['notifications'] = f'存在 {data["dead"]} 笔通知死信，请处理并重发'
    automation = as_object(data.get('automation'))
    automation_state = as_object(data.get('automation_state'))
    if automation.get('enabled'):
        if automation_state.get('last_error'):
            issues['automation'] = 'AI 自动处理巡检失败，请检查模型配置和处理记录'
        age = now - automation_state.get('last_run_at', 0) / 1000
        if age > max(600, automation.get('interval_seconds', 60) * 3):
            issues['automation_stale'] = 'AI 自动处理超过巡检间隔无运行记录'
    if data.get('assistance_manual', 0):
        issues['assistance_manual'] = f'存在 {data["assistance_manual"]} 项付款异常或申诉需要人工处理'
    if retention.get('enabled', True):
        if maintenance.get('last_error'):
            issues['archive'] = '历史记录归档失败，在线原记录保留'
        if now - maintenance.get('last_ok_at', 0) / 1000 > 48 * 3600:
            issues['archive_stale'] = '历史记录归档超过 48 小时无成功执行'
    return issues


def evaluate_backup(cron, jobs, now, name='tillbox-backup'):
    last = timestamp(cron.get('status', {}).get('lastSuccessfulTime'))
    issues = {}
    if cron['spec'].get('suspend') or now - last > 26 * 3600:
        issues['backup'] = 'Tillbox 备份已暂停或超过 26 小时无成功执行'
    failed = [
        j
        for j in jobs
        if any(o.get('name') == name for o in j['metadata'].get('ownerReferences', []))
        and timestamp(j['metadata']['creationTimestamp']) > last
        and any(c['type'] == 'Failed' and c['status'] == 'True' for c in j.get('status', {}).get('conditions', []))
    ]
    if failed:
        issues['backup'] = 'Tillbox 最近备份失败，尚无后续成功备份'
    return issues


def evaluate_disk(available, total):
    if available < 1024**3 or available / max(total, 1) < 0.10:
        return {'disk': f'Tillbox 数据所在磁盘可用 {available / 1024**3:.1f} GiB，低于 1 GiB 或 10%'}
    return {}


def changes(issues, old, now):
    previous = old.get('issues', {})
    active = {k: v for k, v in issues.items() if k not in previous or now - old.get('last_sent', {}).get(k, 0) >= 3600}
    recovered = {k: previous[k] for k in previous.keys() - issues.keys()}
    return active, recovered


def alert_target():
    # Never log tokens or request URLs.
    ref = env('TILLBOX_MONITOR_TELEGRAM_SECRET')
    if ref:
        namespace, _, name = ref.partition('/')
        secret = json.loads(kc('-n', namespace, 'get', 'secret', name, '-o', 'json'))['data']
        return base64.b64decode(secret['telegram-token']).decode().strip(), json.loads(
            base64.b64decode(secret['telegram-chat-ids'])
        )
    ids = [i.strip() for i in env('TILLBOX_MONITOR_TELEGRAM_CHAT_IDS').split(',') if i.strip()]
    return env('TILLBOX_MONITOR_TELEGRAM_TOKEN'), ids


def notify(message):
    token, destinations = alert_target()
    if not token or not destinations:
        raise RuntimeError('Alert destinations unavailable')
    for chat_id in destinations:
        body = json.dumps({'chat_id': str(chat_id), 'text': message, 'disable_web_page_preview': True}).encode()
        request = urllib.request.Request(
            'https://api.telegram.org/bot' + token + '/sendMessage',
            data=body,
            headers={'Content-Type': 'application/json'},
        )
        try:
            with urllib.request.urlopen(request, timeout=15) as response:
                if not json.load(response).get('ok'):
                    raise RuntimeError('Alert rejected')
        except Exception:
            raise RuntimeError('Alert delivery failed') from None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dry-run', action='store_true')
    parser.add_argument('--state', default='/var/lib/tillbox-monitor/state.json')
    parser.add_argument('--disk-path', default='/var/lib/rancher/k3s/storage')
    args = parser.parse_args()
    url = env('TILLBOX_MONITOR_URL')
    if not url:
        parser.error('TILLBOX_MONITOR_URL is required')
    ns, deployment, cronjob = (
        env('TILLBOX_MONITOR_NAMESPACE', 'tillbox'),
        env('TILLBOX_MONITOR_DEPLOYMENT', 'tillbox'),
        env('TILLBOX_MONITOR_BACKUP_CRONJOB', 'tillbox-backup'),
    )
    os.umask(0o077)
    now = time.time()
    issues = {}
    try:
        data = json.loads(
            kc(
                '-n',
                ns,
                'exec',
                'deploy/' + deployment,
                '--',
                'sqlite3',
                '-readonly',
                env('TILLBOX_MONITOR_DB', '/data/tillbox.db'),
                SQL,
            )
        )
        issues.update(evaluate_db(data, now))
    except Exception:
        issues['database'] = 'Tillbox 数据库或查账状态巡检失败'
    if cronjob:
        try:
            cron = json.loads(kc('-n', ns, 'get', 'cronjob', cronjob, '-o', 'json'))
            jobs = json.loads(kc('-n', ns, 'get', 'jobs', '-o', 'json'))['items']
            issues.update(evaluate_backup(cron, jobs, now, cronjob))
        except Exception:
            issues['backup_check'] = 'Tillbox 备份状态巡检失败'
    try:
        deploy = json.loads(kc('-n', ns, 'get', 'deploy', deployment, '-o', 'json'))
        if deploy.get('status', {}).get('availableReplicas', 0) != 1:
            issues['instance'] = 'Tillbox 没有可用实例'
    except Exception:
        issues['instance'] = 'Tillbox 实例巡检失败'
    try:
        stats = os.statvfs(args.disk_path)
        issues.update(evaluate_disk(stats.f_bavail * stats.f_frsize, stats.f_blocks * stats.f_frsize))
    except Exception:
        issues['disk'] = 'Tillbox 数据磁盘巡检失败'
    try:
        with urllib.request.urlopen(url, timeout=15) as response:
            if response.status != 200 or response.read(64) != b'ok':
                issues['https'] = 'Tillbox 公网就绪检查失败'
    except Exception:
        issues['https'] = 'Tillbox 公网 HTTPS 或数据库就绪检查失败'
    state = Path(args.state)
    old = json.loads(state.read_text()) if state.exists() else {}
    active, recovered = changes(issues, old, now)
    print(json.dumps({'checked_at': now, 'dry_run': args.dry_run, 'issues': issues}, ensure_ascii=False), flush=True)
    if args.dry_run:
        return
    if active or recovered:
        lines = ['[Tillbox 巡检] ' + dt.datetime.now(dt.timezone(dt.timedelta(hours=8))).strftime('%m-%d %H:%M')]
        lines += ['🔴 ' + value for value in active.values()]
        lines += ['🟢 已恢复：' + value for value in recovered.values()]
        notify('\n'.join(lines))
    state.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    tmp = state.with_suffix('.tmp')
    tmp.write_text(
        json.dumps(
            {
                'issues': issues,
                'last_sent': {k: now if k in active else old.get('last_sent', {}).get(k, now) for k in issues},
            }
        )
    )
    tmp.replace(state)


if __name__ == '__main__':
    main()
