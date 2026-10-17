import { describe, expect, it } from 'vitest'
import { ledgerErrorText } from './types'

describe('ledgerErrorText', () => {
  it('连续失败才说失败', () => {
    expect(ledgerErrorText({ failures: 3, last_error: '接口超时' })).toBe('连续失败 3 次：接口超时')
  })

  it('没有失败只是暂不查账', () => {
    expect(ledgerErrorText({ failures: 0, last_error: '渠道配置没填完' })).toBe('暂不查账：渠道配置没填完')
  })
})
