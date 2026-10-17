import { describe, expect, it } from 'vitest'
import { channelHealth } from './health'
import type { UpstreamStatus } from './types'

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
