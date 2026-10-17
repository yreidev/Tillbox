// 后端接口返回的数据结构（和 Go 里的 json 标签一一对应）。

export type OrderState = 'pending' | 'paid' | 'expired' | 'closed' | 'disputed'

export interface Order {
  is_test: boolean
  id: number
  trade_no: string
  merchant_id: number
  out_trade_no: string
  version: number
  channel: string
  pay_type: string
  name: string
  money: string
  amount: number
  pay_amount: number
  notify_url: string
  return_url: string
  param: string
  client_ip: string
  device: string
  status: OrderState
  created_at: number
  expires_at: number
  ended_at: number
  released_at: number
  paid_at: number
  paid_amount: number
  txn_id: number
  api_trade_no: string
  buyer: string
  updated_at: number
}

export interface OrderView extends Order {
  state: OrderState
  merchant_name: string
  channel_name: string
}

/** 上游渠道订单的预下单记录 */
export interface UpstreamPayment {
  order_id: number
  channel_id: number
  pay_type: string
  trade_no: string
  info_type: 'qrcode' | 'jump' | 'urlscheme' | 'submit'
  info: string
  checked_at: number
  check_error: string
  created_at: number
  updated_at: number
}

export interface Transaction {
  id: number
  channel: string
  external_id: string
  direction: 'in' | 'out'
  amount: number
  occurred_at: number
  alipay_order_no: string
  merchant_order_no: string
  other_account: string
  memo: string
  biz_desc: string
  kind: string
  raw?: string
  state: 'new' | 'matched' | 'anomaly' | 'ignored'
  order_id: number
  created_at: number
}

export type AnomalyKind =
  'ambiguous' | 'duplicate' | 'late' | 'underpaid' | 'overpaid' | 'unmatched' | 'conflict' | 'unlinked'

export interface Anomaly {
  id: number
  kind: AnomalyKind
  transaction_id: number
  order_id: number
  detail: string
  status: 'open' | 'resolved'
  created_at: number
  resolved_at: number
  resolved_by: string
  note: string
  trade_no: string
  transaction?: Transaction
}

export interface Notification {
  order_id: number
  status: 'pending' | 'success' | 'dead'
  attempts: number
  first_at: number
  next_at: number
  last_at: number
  last_error: string
  updated_at: number
}

export interface NotificationView extends Notification {
  trade_no: string
  out_trade_no: string
  merchant_id: number
  notify_url: string
}

export interface NotifyAttempt {
  id: number
  order_id: number
  manual: boolean
  url: string
  http_status: number
  response: string
  error: string
  success: boolean
  duration_ms: number
  created_at: number
}

export interface Merchant {
  id: number
  name: string
  rsa_public_key: string
  key_type: number
  domains: string
  enabled: boolean
  channel_id: number
  remark: string
  created_at: number
  updated_at: number
}

export interface MerchantInput {
  name: string
  rsa_public_key: string
  key_type: number
  domains: string
  enabled: boolean
  channel_id: number
  remark: string
}

export interface LedgerStatus {
  last_end: number
  last_backfill: number[]
  backlog_segments: number
  backfill_error: string
  backfill_failures: number
  backfill_retry_at: number
  last_run_at: number
  last_ok_at: number
  last_error: string
  failures: number
  backoff_until: number
  running: boolean
  active: boolean
  next_at: number
  /** 渠道实例键 ch<编号> */
  source: string
  channel_id: number
  name: string
  enabled: boolean
}

/** 查账错误的说法：真的连续失败了才算失败；渠道停用、配置没填完时只是暂不查账。 */
export function ledgerErrorText(s: Pick<LedgerStatus, 'failures' | 'last_error'>) {
  return s.failures > 0 ? `连续失败 ${s.failures} 次：${s.last_error}` : `暂不查账：${s.last_error}`
}

/** 概览接口按天汇总的已支付订单（北京时间分日） */
export interface Day {
  date: string
  count: number
  amount: number
}

export interface Overview {
  today: Day
  days: Day[]
  merchants: { merchant_id: number; name: string; count: number; amount: number; days: Day[] }[]
  pending_orders: number
  open_anomalies: number
  dead_notifications: number
  retrying_notifications: number
  /** 未恢复的监控告警 */
  open_alerts: number
  ledgers: LedgerStatus[]
}

export interface Payment {
  order_ttl_seconds: number
  cooldown_seconds: number
  max_tail_cents: number
  normal_interval: number
  active_interval: number
  min_interval: number
  safety_delay_second: number
}

export interface Settings {
  payment: Payment
  defaults: Payment
  platform_public_key: string
  site: { name: string }
  demo: boolean
}

export type ChannelType = 'alipay_qr' | 'epay' | 'demo'

export interface AlipayQRConfig {
  app_id: string
  has_private_key: boolean
  app_public_key: string
  alipay_public_key: string
  gateway: string
  qrcode_url: string
  payee_name: string
}

export interface UpstreamConfig {
  api_url: string
  version: 1 | 2
  pid: string
  has_key: boolean
  has_private_key: boolean
  public_key: string
  platform_public_key: string
  types: string[]
  /** 各支付方式的单笔限额，没有限额的方式不出现 */
  limits: Record<string, UpstreamLimit>
}

/** 上游渠道一种支付方式的单笔限额（元，不限为空）；*_at 是自动识别的时刻，0 表示手动设置 */
export interface UpstreamLimit {
  min: string
  max: string
  min_at: number
  max_at: number
}

/** 上游渠道的预下单、查单和回调状态（存在数据库里，重启后保留） */
export interface UpstreamStatus {
  key: string
  channel_id: number
  prepay_ok_at: number
  prepay_err_at: number
  prepay_err: string
  /** 连续失败的预下单次数 */
  prepay_failures: number
  check_ok_at: number
  check_err_at: number
  check_err: string
  /** 连续失败的查单次数 */
  check_failures: number
  notify_at: number
  notify_err_at: number
  notify_err: string
}

export interface ChannelView {
  id: number
  type: ChannelType
  name: string
  enabled: boolean
  remark: string
  created_at: number
  updated_at: number
  key: string
  config: AlipayQRConfig | UpstreamConfig | Record<string, never>
  /** 不能收款的原因，能收款时为空 */
  problem: string
  pay_types: string[] | null
  orders: number
  transactions: number
  merchants: number
  /** 不能删除的原因，能删时为空 */
  delete_block: string
  ledger?: Omit<LedgerStatus, 'name' | 'enabled'>
  upstream?: UpstreamStatus
}

export const channelTypeText: Record<ChannelType, string> = {
  alipay_qr: '支付宝经营码',
  epay: '上游易支付',
  demo: '演示渠道',
}

/** 易支付 type 的中文名，和后端 channel.TypeNames 一致 */
export const payTypeText: Record<string, string> = {
  alipay: '支付宝',
  wxpay: '微信支付',
  qqpay: 'QQ钱包',
  bank: '云闪付',
  jdpay: '京东支付',
  paypal: 'PayPal',
  usdt: 'USDT',
}

export const payTypeLabel = (t: string) => payTypeText[t] ?? t

export interface AuditLog {
  id: number
  actor: string
  action: string
  target: string
  detail: string
  created_at: number
}

export interface Me {
  site_name: string
  /** TILLBOX_DEMO=true 启动：可以建演示渠道 */
  demo: boolean
  /** 对接方填的接口地址（按对外地址配置算出，以 / 结尾） */
  api_url: string
}

export interface Page<T> {
  /** 没有结果时是空数组（internal/admin/lists_test.go 保证） */
  items: T[]
  total: number
}

export const stateText: Record<OrderState, string> = {
  pending: '待支付',
  paid: '已支付',
  expired: '已过期',
  closed: '已关闭',
  disputed: '争议中',
}

export const stateColor: Record<OrderState, 'default' | 'accent' | 'success' | 'warning' | 'danger'> = {
  pending: 'accent',
  paid: 'success',
  expired: 'default',
  closed: 'default',
  disputed: 'danger',
}

export const anomalyText: Record<AnomalyKind, string> = {
  ambiguous: '匹配不唯一',
  duplicate: '重复付款',
  late: '订单结束后付款',
  underpaid: '少付',
  overpaid: '多付',
  unmatched: '无法匹配',
  conflict: '流水内容变了',
  unlinked: '撤销了关联',
}

export const txnStateText: Record<Transaction['state'], string> = {
  new: '未对账',
  matched: '已关联',
  anomaly: '异常',
  ignored: '不参与',
}

export const notifyText: Record<Notification['status'], string> = {
  pending: '待发送',
  success: '已送达',
  dead: '已放弃',
}
