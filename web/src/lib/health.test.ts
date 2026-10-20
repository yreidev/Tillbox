import { describe, expect, it } from 'vitest'
import { channelHealth, ledgerHealth, ledgerStale } from './health'
import type { LedgerStatus, UpstreamStatus } from './types'

const now = Date.UTC(2026, 9, 17, 12)

const upstream = (u: Partial<UpstreamStatus>): UpstreamStatus => ({
  key: 'ch1',
  channel_id: 1,
  prepay_ok_at: 0,
  prepay_err_at: 0,
  prepay_err: '',
  prepay_failures: 0,
  check_ok_at: 0,
  check_err_at: 0,
  check_err: '',
  check_failures: 0,
  notify_at: 0,
  notify_err_at: 0,
  notify_err: '',
  ...u,
})

describe('channelHealth', () => {
  it('上游的预下单和查单分开计数，到 3 次算严重', () => {
    const lines = channelHealth(
      {
        type: 'epay',
        enabled: true,
        upstream: upstream({ prepay_failures: 3, prepay_err: '签名错误', check_failures: 1, check_err: '超时' }),
      },
      now,
    )
    expect(lines).toEqual([
      { tone: 'danger', text: '预下单连续失败 3 次：签名错误' },
      { tone: 'warning', text: '查单连续失败 1 次：超时' },
    ])
  })

  it('没有失败时显示上次查单和回调', () => {
    const lines = channelHealth(
      {
        type: 'epay',
        enabled: true,
        upstream: upstream({ check_ok_at: now - 120_000, notify_at: now - 3600_000 }),
      },
      now,
    )
    expect(lines.map((l) => l.text)).toEqual(['2 分钟前查单成功', '1 小时前收到回调'])
  })

  it('最近一次回调不合格要提醒', () => {
    const lines = channelHealth(
      {
        type: 'epay',
        enabled: false,
        upstream: upstream({ notify_at: now - 7200_000, notify_err_at: now - 60_000, notify_err: '签名不对' }),
      },
      now,
    )
    expect(lines[1]).toEqual({ tone: 'warning', text: '1 分钟前收到不合格的回调：签名不对' })
  })
})

const ledger = (l: Partial<LedgerStatus>): LedgerStatus => ({
  last_end: 0,
  last_backfill: [0, 0, 0],
  backlog_segments: 0,
  backfill_error: '',
  backfill_failures: 0,
  backfill_retry_at: 0,
  last_run_at: 0,
  last_ok_at: 0,
  last_error: '',
  failures: 0,
  backoff_until: 0,
  running: false,
  active: false,
  next_at: 0,
  ready_since: 0,
  stale_after: 300_000,
  source: 'ch1',
  channel_id: 1,
  name: '经营码',
  enabled: true,
  ...l,
})

describe('ledgerHealth', () => {
  it('还没查过不说正常', () => {
    expect(ledgerHealth(ledger({ ready_since: now - 10_000 }), now)).toEqual({ tone: 'default', text: '还没查过' })
  })

  it('上次成功超过阈值算停摆，查询卡住时也算', () => {
    const s = ledger({ last_ok_at: now - 600_000, running: true })
    expect(ledgerStale(s, now)).toBe(true)
    expect(ledgerHealth(s, now)).toMatchObject({ tone: 'danger', text: '查账停摆' })
    expect(ledgerHealth(s, now).detail).toBe('超过 5 分钟没有成功查账（上次 10 分钟前），新付款可能不能及时确认到账')
  })

  it('从没成功过时从具备查账条件起算', () => {
    expect(ledgerStale(ledger({ ready_since: now - 200_000 }), now)).toBe(false)
    const s = ledger({ ready_since: now - 400_000 })
    expect(ledgerHealth(s, now).detail).toBe('启用后超过 5 分钟还没有成功查过账，新付款可能不能及时确认到账')
  })

  it('不具备查账条件或已有错误时按原来的说法', () => {
    expect(ledgerStale(ledger({}), now)).toBe(false)
    expect(ledgerHealth(ledger({ last_ok_at: now - 600_000, failures: 3, last_error: '超时' }), now).text).toBe(
      '查账失败',
    )
    expect(ledgerHealth(ledger({ last_ok_at: now - 60_000 }), now)).toEqual({
      tone: 'success',
      text: '正常 · 1 分钟前查过',
    })
  })

  it('渠道卡片和状态行用同一条规则', () => {
    const lines = channelHealth(
      { type: 'alipay_qr', enabled: true, ledger: ledger({ last_ok_at: now - 600_000 }) },
      now,
    )
    expect(lines).toEqual([{ tone: 'danger', text: '超过 5 分钟没有成功查账（上次 10 分钟前）' }])
  })
})
