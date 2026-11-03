import { describe, expect, it } from 'vitest'
import { defaultRoutes, percent, routeList } from './routes'
import type { ChannelView } from './types'

const ch = (id: number, name: string, payTypes: string[], more: Partial<ChannelView> = {}) =>
  ({ id, name, type: 'epay', enabled: true, problem: '', pay_types: payTypes, ...more }) as ChannelView

const one = (id: number) => [{ channel_id: id, weight: 1 }]

describe('defaultRoutes', () => {
  it('每种方式走第一个启用并开放它的渠道', () => {
    const channels = [
      ch(1, '停用的', ['alipay'], { enabled: false }),
      ch(2, '经营码', ['alipay'], { type: 'alipay_qr' }),
      ch(3, '第三方', ['alipay', 'wxpay', 'unknown']),
    ]
    expect(defaultRoutes(channels)).toEqual({ alipay: one(2), wxpay: one(3) })
  })
})

describe('routeList', () => {
  it('按固定顺序列出，标出收不了款的原因', () => {
    const channels = [
      ch(2, '经营码', ['alipay'], { problem: '没填收款码' }),
      ch(3, '第三方', ['alipay'], { enabled: false }),
    ]
    const got = routeList({ qqpay: one(9), wxpay: one(3), alipay: one(2), jdpay: [] }, channels).map((r) => [
      r.type,
      r.channels.map((c) => c.problem),
      r.down,
    ])
    expect(got).toEqual([
      ['alipay', ['没填收款码'], true],
      ['wxpay', ['渠道「第三方」不再开放微信支付'], true],
      ['qqpay', ['渠道已删除'], true],
    ])
  })

  it('选了几个渠道时按权重算比例，收不了款的不分', () => {
    const channels = [ch(1, 'A', ['alipay']), ch(2, 'B', ['alipay']), ch(3, 'C', ['alipay'], { enabled: false })]
    const [r] = routeList(
      {
        alipay: [
          { channel_id: 1, weight: 1 },
          { channel_id: 2, weight: 3 },
          { channel_id: 3, weight: 5 },
        ],
      },
      channels,
    )
    expect(r.channels.map((c) => percent(c.share))).toEqual(['25%', '75%', '0%'])
    expect(r.down).toBe(false)
  })

  it('能收款时没有问题', () => {
    expect(routeList({ alipay: one(2) }, [ch(2, '经营码', ['alipay'])])[0].channels[0]).toMatchObject({
      problem: '',
      share: 1,
    })
    expect(routeList(undefined, [])).toEqual([])
  })
})
