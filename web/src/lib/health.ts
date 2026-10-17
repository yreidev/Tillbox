import { relative } from './format'
import { ledgerErrorText, type ChannelView } from './types'

export interface HealthLine {
  text: string
  tone?: 'danger' | 'warning'
}

// 连续失败到这个次数会开告警，和 internal/monitor/checks.go 的 failureThreshold 一致。
const alertAfter = 3

function failureLine(what: string, failures: number, err: string): HealthLine {
  return { tone: failures >= alertAfter ? 'danger' : 'warning', text: `${what}连续失败 ${failures} 次：${err}` }
}

// channelHealth 是渠道的运行情况：经营码看查账，上游看预下单、查单和回调。渠道页卡片和监控页共用。
export function channelHealth(c: Pick<ChannelView, 'type' | 'enabled' | 'ledger' | 'upstream'>, now: number) {
  const lines: HealthLine[] = []
  if (c.ledger && c.enabled) {
    const l = c.ledger
    if (l.last_error) lines.push({ tone: l.failures > 0 ? 'danger' : 'warning', text: `查账${ledgerErrorText(l)}` })
    else lines.push({ text: l.last_ok_at ? `${relative(l.last_ok_at, now)}查账成功` : '还没查过账' })
    if (l.backlog_segments > 0) lines.push({ text: `历史补扫剩余 ${l.backlog_segments} 段` })
  } else if (c.upstream) {
    const u = c.upstream
    if (u.prepay_failures > 0) lines.push(failureLine('预下单', u.prepay_failures, u.prepay_err))
    if (u.check_failures > 0) lines.push(failureLine('查单', u.check_failures, u.check_err))
    if (!u.prepay_failures && !u.check_failures)
      lines.push({ text: u.check_ok_at ? `${relative(u.check_ok_at, now)}查单成功` : '还没查过单' })
    if (u.notify_err_at > u.notify_at)
      lines.push({ tone: 'warning', text: `${relative(u.notify_err_at, now)}收到不合格的回调：${u.notify_err}` })
    else if (u.notify_at) lines.push({ text: `${relative(u.notify_at, now)}收到回调` })
  } else if (c.type === 'demo') {
    lines.push({ text: '用 /web-api/dev/pay 模拟付款' })
  }
  return lines
}
