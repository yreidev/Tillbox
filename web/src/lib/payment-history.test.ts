import { describe, expect, it } from 'vitest'
import { cleanPayments } from './payment-history'

const now = 1_800_000_000_000
const day = 86400_000
const good = {
  trade_no: '2026101712345678901',
  pay_amount: '12.34',
  state: 'paid',
  created_at: now - day,
  visited_at: now - day,
}

describe('cleanPayments', () => {
  it('不是数组时当作没有记录', () => {
    expect(cleanPayments(null, now)).toEqual([])
    expect(cleanPayments({ trade_no: good.trade_no }, now)).toEqual([])
  })

  it('最近看过的排在前面', () => {
    const older = { ...good, trade_no: '2026101700000000001', visited_at: now - 2 * day }
    const newer = { ...good, trade_no: '2026101700000000002', visited_at: now - 1000 }
    expect(cleanPayments([older, newer], now).map((p) => p.trade_no)).toEqual([newer.trade_no, older.trade_no])
  })

  it.each([
    ['订单号是数字', { ...good, trade_no: 123 }],
    ['订单号不是 19 位', { ...good, trade_no: '123' }],
    ['金额不是两位小数', { ...good, pay_amount: '12.3' }],
    ['金额是数字', { ...good, pay_amount: 12.34 }],
    ['未知状态', { ...good, state: 'refunded' }],
    ['创建时间不是数字', { ...good, created_at: '1' }],
    ['超过 30 天', { ...good, visited_at: now - 31 * day }],
    ['访问时间在未来', { ...good, visited_at: now + 2 * 60_000 }],
    ['不是对象', 'x'],
  ])('丢掉%s的记录', (_, bad) => {
    expect(cleanPayments([bad, good], now)).toEqual([good])
  })

  it('同一订单只留第一条', () => {
    const again = { ...good, visited_at: now }
    expect(cleanPayments([good, again], now)).toEqual([good])
  })

  it('最多留 20 条', () => {
    const many = Array.from({ length: 25 }, (_, i) => ({
      ...good,
      trade_no: `20261017${String(i).padStart(11, '0')}`,
      visited_at: now - i * 1000,
    }))
    expect(cleanPayments(many, now)).toHaveLength(20)
  })
})
