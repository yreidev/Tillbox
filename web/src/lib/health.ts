import { duration, relative } from './format'
import { ledgerErrorText, type ChannelView, type LedgerStatus } from './types'
import type { Tone } from './ui-base'

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
    else if (ledgerStale(l, now)) lines.push({ tone: 'danger', text: staleText(l, now) })
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

type StaleFields = Pick<LedgerStatus, 'last_ok_at' | 'ready_since' | 'stale_after'>

/**
 * ledgerStale 判断查账是不是停摆了：距上次成功查账超过阈值；从没成功过时从具备查账条件起算。
 * 和 internal/monitor/checks.go 的“查账停摆”告警是同一条规则。
 */
export function ledgerStale(l: StaleFields, now: number) {
  const since = l.last_ok_at || l.ready_since
  return since > 0 && now - since > l.stale_after
}

function staleText(l: StaleFields, now: number) {
  return l.last_ok_at
    ? `超过 ${duration(l.stale_after)}没有成功查账（上次 ${relative(l.last_ok_at, now)}）`
    : `启用后超过 ${duration(l.stale_after)}还没有成功查过账`
}

// ledgerHealth 把一个经营码渠道的查账状态归成一句话和一个颜色；停摆时 detail 是给人看的原因。
export function ledgerHealth(
  s: LedgerStatus,
  now = Date.now(),
): { tone: Tone; text: string; pulse?: boolean; detail?: string } {
  if (!s.enabled) return { tone: 'default', text: '已停用' }
  if (s.failures > 0) return { tone: 'danger', text: '查账失败' }
  if (s.last_error) return { tone: 'warning', text: '暂不查账' }
  // 停摆排在“查询中”前面：一轮查询卡住不返回时也要看得出来。
  if (ledgerStale(s, now))
    return { tone: 'danger', text: '查账停摆', detail: `${staleText(s, now)}，新付款可能不能及时确认到账` }
  if (s.running) return { tone: 'accent', text: '查询中', pulse: true }
  if (!s.last_ok_at) return { tone: 'default', text: '还没查过' }
  if (s.active) return { tone: 'success', text: '有待付订单，加快查询', pulse: true }
  if (s.backfill_error) return { tone: 'warning', text: '历史补扫出错' }
  return { tone: 'success', text: `正常 · ${relative(s.last_ok_at, now)}查过` }
}
