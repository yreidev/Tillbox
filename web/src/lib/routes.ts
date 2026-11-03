import { payTypeLabel, payTypeText, type ChannelView, type Routes } from './types'

/** 支付方式的固定顺序，和后端 channel.TypeOrder 一致 */
export const payTypeOrder = Object.keys(payTypeText)

/** RouteChannel 是一种支付方式选的一个渠道 */
export interface RouteChannel {
  id: number
  weight: number
  /** 渠道已删除时为空 */
  channel?: ChannelView
  /** 这个渠道现在收不了这种方式的原因，能收款时为空 */
  problem: string
  /** 在能收款的渠道里分到新订单的比例（0–1），收不了款时为 0 */
  share: number
}

/** Route 是商户开放的一种支付方式和它选的渠道 */
export interface Route {
  type: string
  label: string
  channels: RouteChannel[]
  /** 选的渠道都收不了款时为真 */
  down: boolean
}

/** channelProblem 是渠道现在收不了这种方式的原因，能收款时为空；判断方法和后端 payroute.Methods 一样 */
export function channelProblem(c: ChannelView | undefined, type: string): string {
  if (!c) return '渠道已删除'
  if (!(c.pay_types ?? []).includes(type)) return `渠道「${c.name}」不再开放${payTypeLabel(type)}`
  if (!c.enabled) return '渠道已停用'
  return c.problem
}

/** routeList 按固定顺序列出商户开放的支付方式，以及各自选的渠道能不能收款、按权重分到多少新订单 */
export function routeList(routes: Routes | null | undefined, channels: ChannelView[]): Route[] {
  return payTypeOrder
    .filter((t) => routes?.[t]?.length)
    .map((t) => {
      const list = routes![t].map((r) => {
        const c = channels.find((x) => x.id === r.channel_id)
        return { id: r.channel_id, weight: r.weight, channel: c, problem: channelProblem(c, t), share: 0 }
      })
      const total = list.reduce((n, c) => n + (c.problem ? 0 : c.weight), 0)
      for (const c of list) c.share = c.problem || !total ? 0 : c.weight / total
      return { type: t, label: payTypeLabel(t), channels: list, down: list.every((c) => c.problem) }
    })
}

/** percent 把比例写成百分数，例如 0.75 → “75%” */
export function percent(share: number): string {
  return `${Math.round(share * 100)}%`
}

/** defaultRoutes 是新建商户默认的收款方式：每种方式走第一个启用、并且开放了这种方式的渠道，和后端 DefaultRoutes 一样 */
export function defaultRoutes(channels: ChannelView[]): Routes {
  const out: Routes = {}
  for (const c of channels) {
    if (!c.enabled) continue
    for (const t of c.pay_types ?? []) {
      if (!out[t] && t in payTypeText) out[t] = [{ channel_id: c.id, weight: 1 }]
    }
  }
  return out
}
