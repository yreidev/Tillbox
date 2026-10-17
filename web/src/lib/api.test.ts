import { describe, expect, it } from 'vitest'
import { parseSearchValue, query } from './api'

describe('query', () => {
  it('空值不带上', () => {
    expect(query({ a: 'x', b: '', c: undefined, d: null, e: 0 })).toBe('?a=x&e=0')
    expect(query({ b: '' })).toBe('')
  })

  it('转义特殊字符', () => {
    expect(query({ q: 'a b&c' })).toBe('?q=a+b%26c')
  })
})

describe('parseSearchValue', () => {
  it('19 位订单号保持字符串，不丢精度', () => {
    expect(parseSearchValue('2026101712345678901')).toBe('2026101712345678901')
    expect(parseSearchValue('-2026101712345678901')).toBe('-2026101712345678901')
  })

  it('其他值按 JSON 解析', () => {
    expect(parseSearchValue('42')).toBe(42)
    expect(parseSearchValue('true')).toBe(true)
    expect(parseSearchValue('"x"')).toBe('x')
  })
})
