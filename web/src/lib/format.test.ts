import { describe, expect, it } from 'vitest'
import { bytes, duration, relative, time, yuan } from './format'

describe('yuan', () => {
  it('把分显示成元，固定两位小数', () => {
    expect(yuan(0)).toBe('0.00')
    expect(yuan(5)).toBe('0.05')
    expect(yuan(1234)).toBe('12.34')
    expect(yuan(100000)).toBe('1000.00')
    expect(yuan(-1205)).toBe('-12.05')
  })

  it('没有金额时显示空', () => {
    expect(yuan(undefined)).toBe('')
    expect(yuan(null)).toBe('')
  })
})

describe('time', () => {
  it('按本地时间显示', () => {
    const ms = new Date(2026, 9, 17, 8, 5, 9).getTime()
    expect(time(ms)).toBe('2026-10-17 08:05:09')
    expect(time(ms, false)).toBe('2026-10-17 08:05')
  })

  it('0 表示没有', () => {
    expect(time(0)).toBe('')
    expect(time(undefined)).toBe('')
  })
})

describe('relative', () => {
  const now = 1_800_000_000_000

  it.each([
    [now + 30_000, '30 秒后'],
    [now - 59_000, '59 秒前'],
    [now - 90_000, '2 分钟前'],
    [now + 2 * 3600_000, '2 小时后'],
    [now - 3 * 86400_000, '3 天前'],
  ])('%i', (ms, want) => {
    expect(relative(ms, now)).toBe(want)
  })
})

describe('duration', () => {
  it('按分钟、小时、天取整', () => {
    expect(duration(30_000)).toBe('不到 1 分钟')
    expect(duration(5 * 60_000)).toBe('5 分钟')
    expect(duration(2 * 3600_000)).toBe('2 小时')
    expect(duration(2 * 3600_000 + 5 * 60_000)).toBe('2 小时 5 分钟')
    expect(duration(27 * 3600_000)).toBe('1 天 3 小时')
    expect(duration(48 * 3600_000 + 59_000)).toBe('2 天')
  })
})

describe('bytes', () => {
  it('按 1024 进位，小于 10 的留一位小数', () => {
    expect(bytes(512)).toBe('512 B')
    expect(bytes(1536)).toBe('1.5 KB')
    expect(bytes(12 * 1024 * 1024)).toBe('12 MB')
    expect(bytes(3 * 1024 ** 3)).toBe('3.0 GB')
  })
})
