import { describe, expect, it } from 'vitest'
import { defaultRoutes, routeList } from './routes'
import type { ChannelView } from './types'

const ch = (id: number, name: string, payTypes: string[], more: Partial<ChannelView> = {}) =>
  ({ id, name, type: 'epay', enabled: true, problem: '', pay_types: payTypes, ...more }) as ChannelView

describe('defaultRoutes', () => {
  it('每种方式走第一个启用并开放它的渠道', () => {
    const channels = [
      ch(1, '停用的', ['alipay'], { enabled: false }),
      ch(2, '经营码', ['alipay'], { type: 'alipay_qr' }),
      ch(3, '第三方', ['alipay', 'wxpay', 'unknown']),
    ]
    expect(defaultRoutes(channels)).toEqual({ alipay: 2, wxpay: 3 })
  })
})

describe('routeList', () => {
  it('按固定顺序列出，标出收不了款的原因', () => {
    const channels = [
      ch(2, '经营码', ['alipay'], { problem: '没填收款码' }),
      ch(3, '第三方', ['alipay'], { enabled: false }),
    ]
    const got = routeList({ qqpay: 9, wxpay: 3, alipay: 2, jdpay: 0 }, channels).map((r) => [r.type, r.problem])
    expect(got).toEqual([
      ['alipay', '没填收款码'],
      ['wxpay', '渠道「第三方」不再开放微信支付'],
      ['qqpay', '渠道已删除'],
    ])
  })

  it('能收款时没有问题', () => {
    expect(routeList({ alipay: 2 }, [ch(2, '经营码', ['alipay'])])[0].problem).toBe('')
    expect(routeList(undefined, [])).toEqual([])
  })
})
