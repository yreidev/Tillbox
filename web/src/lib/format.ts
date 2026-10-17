import dayjs from 'dayjs'

// 金额统一用分存，显示成元。
export function yuan(cents: number | undefined | null): string {
  if (cents === undefined || cents === null) return ''
  const neg = cents < 0
  const abs = Math.abs(cents)
  const s = `${Math.floor(abs / 100)}.${String(abs % 100).padStart(2, '0')}`
  return neg ? `-${s}` : s
}

// 毫秒时间戳显示成本地时间；0 表示没有。
export function time(ms: number | undefined | null, withSeconds = true): string {
  if (!ms) return ''
  return dayjs(ms).format(withSeconds ? 'YYYY-MM-DD HH:mm:ss' : 'YYYY-MM-DD HH:mm')
}

export function relative(ms: number, now = Date.now()): string {
  const d = Math.round((ms - now) / 1000)
  const abs = Math.abs(d)
  let s: string
  if (abs < 60) s = `${abs} 秒`
  else if (abs < 3600) s = `${Math.round(abs / 60)} 分钟`
  else if (abs < 86400) s = `${Math.round(abs / 3600)} 小时`
  else s = `${Math.round(abs / 86400)} 天`
  return d >= 0 ? `${s}后` : `${s}前`
}
