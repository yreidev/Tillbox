import { payTypeLabel, payTypeText, type ChannelView } from './types'

/** 支付方式的固定顺序，和后端 channel.TypeOrder 一致 */
export const payTypeOrder = Object.keys(payTypeText)

/** Route 是商户开放的一种支付方式和它走的渠道 */
export interface Route {
  type: string
  label: string
  /** 渠道已删除时为空 */
  channel?: ChannelView
  /** 这种方式现在收不了款的原因，能收款时为空 */
  problem: string
}

/** routeList 按固定顺序列出商户开放的支付方式，判断方法和后端 payroute.Methods 一样 */
export function routeList(routes: Record<string, number> | null | undefined, channels: ChannelView[]): Route[] {
  return payTypeOrder
    .filter((t) => routes?.[t])
    .map((t) => {
      const c = channels.find((x) => x.id === routes![t])
      const label = payTypeLabel(t)
      const problem = !c
        ? '渠道已删除'
        : !(c.pay_types ?? []).includes(t)
          ? `渠道「${c.name}」不再开放${label}`
          : !c.enabled
            ? '渠道已停用'
            : c.problem
      return { type: t, label, channel: c, problem }
    })
}

/** defaultRoutes 是新建商户默认的收款方式：每种方式走第一个启用、并且开放了这种方式的渠道，和后端 DefaultRoutes 一样 */
export function defaultRoutes(channels: ChannelView[]): Record<string, number> {
  const out: Record<string, number> = {}
  for (const c of channels) {
    if (!c.enabled) continue
    for (const t of c.pay_types ?? []) {
      if (!out[t] && t in payTypeText) out[t] = c.id
    }
  }
  return out
}
