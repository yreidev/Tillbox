import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Button, Drawer } from '@yreidev/ui'
import { ArrowUpRightFromSquare, Bell, CircleXmark, LinkSlash } from '@gravity-ui/icons'
import { api } from '../lib/api'
import { relative, time, yuan } from '../lib/format'
import { useNow } from '../lib/now'
import type { Anomaly, Notification, NotifyAttempt, OrderView, Transaction, UpstreamPayment } from '../lib/types'
import { anomalyText, notifyText, payTypeLabel } from '../lib/types'
import {
  Confirm,
  Copyable,
  Desc,
  Loading,
  NoteModal,
  Notice,
  Pill,
  Section,
  StateTag,
  Timeline,
  stepDot,
  toastError,
  toastOK,
  type StepStatus,
  type TimelineItem,
} from '../lib/ui'

interface Detail {
  order: OrderView
  transaction?: Transaction
  upstream?: UpstreamPayment
  notification?: Notification
  attempts: NotifyAttempt[]
  anomalies: Anomaly[]
}

const upstreamInfoText: Record<UpstreamPayment['info_type'], string> = {
  qrcode: '二维码（收银台展示）',
  jump: '跳转到上游付款页',
  urlscheme: '唤起 App 付款',
  submit: '跳转到上游收银台',
}

// progress 把订单从下单到通知商户的经过排成时间线。
function progress(d: Detail, now: number): TimelineItem[] {
  const o = d.order
  const step = (key: string, status: StepStatus, title: string, meta?: string, body?: string): TimelineItem => ({
    key,
    title,
    meta,
    children: body,
    dot: stepDot(status),
  })
  const items = [
    step(
      'created',
      'done',
      '商户下单',
      time(o.created_at),
      `下单金额 ${o.money} 元${o.client_ip ? ` · ${o.client_ip}` : ''}`,
    ),
  ]
  items.push(
    o.pay_type
      ? step('type', 'done', `选择${payTypeLabel(o.pay_type)}`, undefined, o.channel_name || o.channel)
      : step(
          'type',
          o.state === 'pending' ? 'current' : 'wait',
          '付款人选择支付方式',
          undefined,
          o.state === 'pending' ? '付款人还没打开收银台或还没选' : undefined,
        ),
  )
  switch (o.state) {
    case 'paid':
      items.push(
        step(
          'paid',
          'done',
          '收到付款',
          time(o.paid_at),
          `实收 ${yuan(o.paid_amount)} 元${o.buyer ? ` · 付款方 ${o.buyer}` : ''}`,
        ),
      )
      break
    case 'pending':
      items.push(
        step(
          'paid',
          'current',
          '等待付款',
          undefined,
          o.expires_at > now ? `${relative(o.expires_at, now)}过期（${time(o.expires_at)}）` : '已到期，正在结束',
        ),
      )
      break
    case 'expired':
      items.push(step('paid', 'error', '超时未付款', time(o.expires_at), '订单已过期，之后再付款会记为异常'))
      break
    case 'closed':
      items.push(step('paid', 'error', '订单已关闭', time(o.ended_at), '之后再付款会记为异常'))
      break
    case 'disputed':
      items.push(
        step('paid', 'error', '争议中', time(o.updated_at), '到账关联已撤销，不再通知商户；流水回到了异常列表'),
      )
      break
  }
  const n = d.notification
  if (!n) items.push(step('notify', 'wait', '通知商户', undefined, '付款后自动通知'))
  else if (n.status === 'success')
    items.push(step('notify', 'done', '商户已确认', time(n.last_at), `共发送 ${n.attempts} 次`))
  else if (n.status === 'dead')
    items.push(
      step('notify', 'error', '通知失败，已放弃', time(n.last_at), n.last_error || `发送了 ${n.attempts} 次都没成功`),
    )
  else
    items.push(
      step(
        'notify',
        'current',
        '正在通知商户',
        undefined,
        `已发送 ${n.attempts} 次${n.next_at ? `，下次 ${relative(n.next_at, now)}` : ''}`,
      ),
    )
  return items
}

// OrderDetail 是订单详情抽屉：金额和状态、进度时间线、订单字段、到账流水、相关异常和通知记录，以及关单、撤销关联、重发通知。
export function OrderDetail({ id, onClose }: { id: number | null; onClose: () => void }) {
  const qc = useQueryClient()
  const q = useQuery({
    queryKey: ['order', id],
    queryFn: () => api.get<Detail>(`/web-api/admin/orders/${id}`),
    enabled: id !== null,
  })
  const [unlinkOpen, setUnlinkOpen] = useState(false)
  const refresh = () => void qc.invalidateQueries()
  const close = useMutation({
    mutationFn: () => api.post(`/web-api/admin/orders/${id}/close`),
    onSuccess: () => {
      toastOK('订单已关闭')
      refresh()
    },
    onError: toastError,
  })
  const resend = useMutation({
    mutationFn: () => api.post<{ attempt: NotifyAttempt }>(`/web-api/admin/notifications/${id}/resend`),
    onSuccess: (r) => {
      if (r.attempt.success) toastOK('通知已送达')
      else
        toastError(
          new Error(`商户没有确认：${r.attempt.error || r.attempt.response || `HTTP ${r.attempt.http_status}`}`),
        )
      refresh()
    },
    onError: toastError,
  })

  const d = q.data?.order.id === id ? q.data : undefined
  const o = d?.order
  const now = useNow()
  return (
    <Drawer.Backdrop isOpen={id !== null} onOpenChange={(v) => !v && onClose()}>
      <Drawer.Content placement="right">
        <Drawer.Dialog className="w-[min(560px,100vw)] max-w-[100vw] p-0 sm:w-[min(560px,100vw)]">
          <Drawer.CloseTrigger />
          <Drawer.Header className="gap-3 border-b border-separator px-5 pt-5 pb-4 sm:px-6">
            <Drawer.Heading className="pr-10 text-sm font-normal text-muted">
              {o ? (
                <span className="flex items-center gap-1">
                  订单 <Copyable text={o.trade_no} mono />
                </span>
              ) : (
                '订单详情'
              )}
            </Drawer.Heading>
            {o && (
              <>
                <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                  <span className="tb-num text-3xl font-semibold tracking-tight">¥{yuan(o.pay_amount)}</span>
                  <StateTag state={o.state} />
                  {o.is_test && <Pill>测试订单</Pill>}
                </div>
                <p className="truncate text-sm">
                  {o.name}
                  <span className="text-muted"> · {o.merchant_name || `已删除的商户（${o.merchant_id}）`}</span>
                </p>
                <div className="flex flex-wrap gap-2">
                  {o.state === 'pending' && (
                    <Confirm
                      title="关闭这笔订单？"
                      content="关闭后付款人再付款会记为异常，需要人工处理。"
                      okText="关闭订单"
                      danger
                      onConfirm={() => close.mutate()}
                    >
                      <Button size="sm" variant="danger-soft" isPending={close.isPending}>
                        <CircleXmark />
                        关闭订单
                      </Button>
                    </Confirm>
                  )}
                  {o.status === 'paid' && (
                    <>
                      <Button
                        size="sm"
                        variant="secondary"
                        isPending={resend.isPending}
                        onPress={() => resend.mutate()}
                      >
                        <Bell />
                        重发通知
                      </Button>
                      <Button size="sm" variant="danger-soft" onPress={() => setUnlinkOpen(true)}>
                        <LinkSlash />
                        撤销到账关联
                      </Button>
                    </>
                  )}
                  <Button
                    size="sm"
                    variant="ghost"
                    onPress={() => window.open(`/cashier/${o.trade_no}`, '_blank', 'noopener')}
                  >
                    <ArrowUpRightFromSquare />
                    打开收银台
                  </Button>
                </div>
              </>
            )}
          </Drawer.Header>
          <Drawer.Body className="flex flex-col gap-7 px-5 py-5 sm:px-6">
            {q.isPending && <Loading />}
            {q.isError && <Notice status="danger">{q.error.message}</Notice>}
            {o && d && (
              <>
                {d.anomalies.some((a) => a.status === 'open') && (
                  <Notice status="warning" title="这笔订单有待处理的异常">
                    到“流水与异常”页处理；处理完这里的提示会消失。
                  </Notice>
                )}
                <Section title="进度">
                  <Timeline items={progress(d, now)} />
                </Section>

                <Section title="订单信息">
                  <Desc
                    data={[
                      { key: '商户订单号', value: <Copyable text={o.out_trade_no} mono /> },
                      { key: '商户', value: `${o.merchant_name || '已删除'}（${o.merchant_id}）` },
                      { key: '接口', value: o.version === 2 ? 'V2（RSA）' : 'V1（MD5）' },
                      { key: '支付渠道', value: o.channel_name || o.channel },
                      { key: '支付方式', value: o.pay_type ? payTypeLabel(o.pay_type) : '付款人还没选' },
                      { key: '订单金额', value: <span className="tb-num">{yuan(o.amount)} 元</span> },
                      o.amount !== o.pay_amount && {
                        key: '应付金额',
                        value: <span className="tb-num">{yuan(o.pay_amount)} 元（加了尾数区分订单）</span>,
                      },
                      o.paid_amount > 0 && {
                        key: '实收金额',
                        value: (
                          <span className={o.paid_amount === o.pay_amount ? 'tb-num' : 'tb-num text-warning'}>
                            {yuan(o.paid_amount)} 元
                          </span>
                        ),
                      },
                      { key: '创建时间', value: <span className="tb-num">{time(o.created_at)}</span> },
                      { key: '到期时间', value: <span className="tb-num">{time(o.expires_at)}</span> },
                      o.paid_at > 0 && { key: '支付时间', value: <span className="tb-num">{time(o.paid_at)}</span> },
                      o.api_trade_no && { key: '支付交易号', value: <Copyable text={o.api_trade_no} mono /> },
                      o.buyer && { key: '付款方', value: o.buyer },
                      o.client_ip && { key: '下单 IP', value: <span className="tb-num">{o.client_ip}</span> },
                      o.device && { key: '设备', value: o.device },
                      o.param && { key: '附加参数', value: <span className="break-all">{o.param}</span> },
                      { key: '通知地址', value: <span className="tb-mono break-all">{o.notify_url}</span> },
                      o.return_url && {
                        key: '回跳地址',
                        value: <span className="tb-mono break-all">{o.return_url}</span>,
                      },
                    ]}
                  />
                </Section>

                {d.upstream && (
                  <Section title="上游预下单">
                    <Desc
                      data={[
                        { key: '上游订单号', value: <Copyable text={d.upstream.trade_no} mono /> },
                        { key: '支付方式', value: payTypeLabel(d.upstream.pay_type) },
                        { key: '付款信息', value: upstreamInfoText[d.upstream.info_type] ?? d.upstream.info_type },
                        { key: '预下单时间', value: <span className="tb-num">{time(d.upstream.created_at)}</span> },
                        {
                          key: '上次查单',
                          value: d.upstream.checked_at ? (
                            <span className="tb-num">{time(d.upstream.checked_at)}</span>
                          ) : (
                            '还没查过'
                          ),
                        },
                        d.upstream.check_error
                          ? { key: '查单出错', value: <span className="text-danger">{d.upstream.check_error}</span> }
                          : null,
                      ]}
                    />
                  </Section>
                )}

                <Section title="到账流水">
                  {d.transaction ? (
                    <Desc
                      data={[
                        { key: '流水号', value: <Copyable text={d.transaction.external_id} mono /> },
                        { key: '金额', value: <span className="tb-num">{yuan(d.transaction.amount)} 元</span> },
                        { key: '到账时间', value: <span className="tb-num">{time(d.transaction.occurred_at)}</span> },
                        d.transaction.other_account && { key: '付款方', value: d.transaction.other_account },
                        d.transaction.memo && { key: '备注', value: d.transaction.memo },
                      ]}
                    />
                  ) : (
                    <p className="text-sm text-muted">还没有关联到账流水。</p>
                  )}
                </Section>

                {d.anomalies.length > 0 && (
                  <Section title="相关异常">
                    <ul className="flex flex-col gap-2">
                      {d.anomalies.map((a) => (
                        <li key={a.id} className="rounded-xl border border-separator p-3 text-sm">
                          <div className="flex flex-wrap items-center gap-2">
                            <Pill tone={a.status === 'open' ? 'warning' : 'default'}>{anomalyText[a.kind]}</Pill>
                            <span className="text-xs text-muted">
                              {a.status === 'open' ? '待处理' : `${a.resolved_by} 已处理`}
                            </span>
                            <span className="tb-num ms-auto text-xs text-muted">{time(a.created_at, false)}</span>
                          </div>
                          <p className="mt-2">{a.detail}</p>
                          {a.note && <p className="mt-1 text-xs text-muted">处理说明：{a.note}</p>}
                        </li>
                      ))}
                    </ul>
                  </Section>
                )}

                <Section
                  title="通知记录"
                  extra={
                    d.notification && (
                      <Pill
                        tone={
                          d.notification.status === 'success'
                            ? 'success'
                            : d.notification.status === 'dead'
                              ? 'danger'
                              : 'accent'
                        }
                      >
                        {notifyText[d.notification.status]}
                      </Pill>
                    )
                  }
                >
                  {d.attempts.length === 0 ? (
                    <p className="text-sm text-muted">{d.notification ? '还没发过。' : '付款后才会通知商户。'}</p>
                  ) : (
                    <ul className="flex flex-col divide-y divide-separator">
                      {d.attempts.map((a) => (
                        <li key={a.id} className="flex items-start gap-3 py-2.5 first:pt-0 last:pb-0">
                          <span
                            className={
                              a.success
                                ? 'mt-1.5 size-2 shrink-0 rounded-full bg-success'
                                : 'mt-1.5 size-2 shrink-0 rounded-full bg-danger'
                            }
                          />
                          <div className="min-w-0 flex-1">
                            <div className="flex flex-wrap items-center gap-x-2 text-sm">
                              <span className="font-medium">{a.success ? '商户已确认' : '失败'}</span>
                              {a.manual && <Pill>手动</Pill>}
                              <span className="tb-num ms-auto text-xs text-muted">
                                {time(a.created_at)} · {a.duration_ms} 毫秒
                              </span>
                            </div>
                            <p
                              className="mt-0.5 line-clamp-2 text-xs break-all text-muted"
                              title={a.error || a.response}
                            >
                              {a.error || `HTTP ${a.http_status}${a.response ? `：${a.response}` : ''}`}
                            </p>
                          </div>
                        </li>
                      ))}
                    </ul>
                  )}
                </Section>

                <NoteModal
                  title="撤销到账关联"
                  danger
                  okText="撤销关联"
                  visible={unlinkOpen}
                  onCancel={() => setUnlinkOpen(false)}
                  description="订单会变成「争议中」，停止通知商户；流水回到异常列表，可以再关联到正确的订单。已经送达的通知无法撤回，请自行联系商户处理。"
                  onOK={async (note) => {
                    await api.post(`/web-api/admin/orders/${o.id}/unlink`, { note })
                    toastOK('已撤销关联')
                    setUnlinkOpen(false)
                    refresh()
                  }}
                />
              </>
            )}
          </Drawer.Body>
        </Drawer.Dialog>
      </Drawer.Content>
    </Drawer.Backdrop>
  )
}
