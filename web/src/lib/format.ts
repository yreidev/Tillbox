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

// duration 把时长（毫秒）写成“5 分钟”“2 小时 5 分钟”“1 天 3 小时”。
export function duration(ms: number): string {
  const m = Math.max(0, Math.floor(ms / 60_000))
  if (m < 1) return '不到 1 分钟'
  if (m < 60) return `${m} 分钟`
  const h = Math.floor(m / 60)
  if (h < 24) return m % 60 ? `${h} 小时 ${m % 60} 分钟` : `${h} 小时`
  const d = Math.floor(h / 24)
  return h % 24 ? `${d} 天 ${h % 24} 小时` : `${d} 天`
}

// bytes 把文件大小写成“512 B”“1.5 KB”“12 MB”：小于 10 的留一位小数。
export function bytes(n: number): string {
  if (n < 1024) return `${n} B`
  const units = ['KB', 'MB', 'GB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[i]}`
}
