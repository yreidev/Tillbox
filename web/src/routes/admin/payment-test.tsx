import { useState, type CSSProperties } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Button, Card, Form, ListView, Stepper } from '@yreidev/ui'
import { ArrowUpRightFromSquare, CircleCheck, Flask, Xmark } from '@gravity-ui/icons'
import { api, channelsQuery } from '../../lib/api'
import { relative, time, yuan } from '../../lib/format'
import type { Merchant, Notification, NotifyAttempt, Order, OrderView, Page, Transaction } from '../../lib/types'
import {
  Confirm,
  Copyable,
  Desc,
  Empty,
  Loading,
  Notice,
  PageHeader,
  Panel,
  RadioInput,
  SelectInput,
  StateTag,
  TextInput,
  cx,
  toastError,
  toastOK,
  type StepStatus,
} from '../../lib/ui'
import { OrderDetail } from '../../components/order-detail'

export const Route = createFileRoute('/admin/payment-test')({ component: PaymentTestPage })

/** 出错的那一步改用红色。 */
const dangerStep = { '--stepper-active-color': 'var(--color-danger)' } as CSSProperties

/** 最近测试单默认显示的笔数，其余点“显示全部”展开。 */
const recentShown = 6

type Detail = { order: OrderView; transaction?: Transaction; notification?: Notification; attempts: NotifyAttempt[] }

function PaymentTestPage() {
  const qc = useQueryClient()
  const channels = useQuery(channelsQuery).data?.items ?? []
  const [selected, setSelected] = useState<number | null>(null)
  const [showAll, setShowAll] = useState(false)
  // 窄屏上进度卡片排在列表下面，选中后滚过去，不然看不到。
  const pick = (id: number) => {
    setSelected(id)
    if (window.matchMedia('(max-width: 1023px)').matches) {
      requestAnimationFrame(() =>
        document.getElementById('test-progress')?.scrollIntoView({ behavior: 'smooth', block: 'start' }),
      )
    }
  }
  const [detail, setDetail] = useState<number | null>(null)
  const [merchantID, setMerchantID] = useState<number | undefined>()
  const [money, setMoney] = useState('1.00')
  const [version, setVersion] = useState<'1' | '2'>('1')
  const merchants = useQuery({
    queryKey: ['merchants'],
    queryFn: () => api.get<{ items: { merchant: Merchant }[] }>('/web-api/admin/merchants'),
  })
  const merchantChannel = (id?: number) =>
    channels.find((c) => c.id === merchants.data?.items.find(({ merchant: m }) => m.id === id)?.merchant.channel_id)
  const picked = merchantChannel(merchantID)
  const tests = useQuery({
    queryKey: ['payment-tests'],
    queryFn: () => api.get<Page<OrderView>>('/web-api/admin/orders?is_test=true&page_size=20'),
    refetchInterval: 5000,
  })
  const recent = tests.data?.items ?? []
  const create = useMutation({
    mutationFn: () =>
      api.post<{ order: Order }>('/web-api/admin/payment-tests', {
        merchant_id: merchantID,
        money: money.trim(),
        version: Number(version),
      }),
    onSuccess: async (res) => {
      pick(res.order.id)
      await qc.invalidateQueries({ queryKey: ['payment-tests'] })
      toastOK('测试单已创建，请打开收银台付款')
    },
    onError: toastError,
  })
  const enabled = (merchants.data?.items ?? []).filter(({ merchant: m }) => m.enabled)

  return (
    <div className="flex flex-col gap-4">
      <PageHeader description="用自己的商户下一笔小额测试单，走一遍付款、到账确认和回调验签，确认整条链路是通的。" />
      <Notice status="warning">
        测试单按所选商户的支付渠道下单，会真实收款，请严格按收银台的应付金额付款。回调发回本站验签，不会给业务站充值；测试单不计入营业统计。
      </Notice>
      <div className="grid items-start gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.4fr)]">
        <div className="flex flex-col gap-4">
          <Panel title="新建测试单">
            {merchants.isError && <Notice status="danger">{merchants.error.message}</Notice>}
            <Form
              className="flex flex-col gap-4"
              onSubmit={(e) => {
                e.preventDefault()
                create.mutate()
              }}
            >
              <SelectInput<number>
                label="商户"
                placeholder={enabled.length ? '选择已启用的商户' : '没有已启用的商户'}
                isRequired
                value={merchantID}
                onChange={setMerchantID}
                options={enabled.map(({ merchant: m }) => ({ value: m.id, label: `${m.name}（${m.id}）` }))}
                description={
                  picked ? (
                    <span className={cx(picked.enabled && !picked.problem ? undefined : 'text-danger')}>
                      走渠道「{picked.name}」
                      {!picked.enabled ? '，渠道已停用' : picked.problem ? `，不能收款：${picked.problem}` : ''}
                    </span>
                  ) : (
                    '用这个商户的密钥签名下单，走它选的支付渠道'
                  )
                }
              />
              <TextInput
                label="订单金额（元）"
                inputMode="decimal"
                isRequired
                value={money}
                onChange={setMoney}
                validate={(v) => (/^\d+(\.\d{1,2})?$/.test(v.trim()) ? null : '填 0.01–100.00 之间的金额')}
                description={
                  picked?.type === 'epay'
                    ? '0.01–100.00 元，上游渠道按原价付款'
                    : '0.01–100.00 元，经营码渠道的应付金额会加上尾数'
                }
              />
              <RadioInput
                label="回调签名"
                horizontal
                value={version}
                onChange={setVersion}
                options={[
                  { value: '1', label: 'V1（MD5）' },
                  { value: '2', label: 'V2（RSA）' },
                ]}
              />
              <div>
                <Button type="submit" isPending={create.isPending}>
                  创建测试单
                </Button>
              </div>
            </Form>
          </Panel>
          <Panel title="最近的测试单" description="点一笔看进度" flush>
            {tests.isError && (
              <div className="px-4 pb-4">
                <Notice status="danger">{tests.error.message}</Notice>
              </div>
            )}
            {tests.isPending ? (
              <Loading />
            ) : recent.length === 0 ? (
              <Empty description="还没有测试单" />
            ) : (
              <div className="px-2 pb-2">
                <ListView
                  aria-label="最近的测试单"
                  variant="secondary"
                  selectionMode="single"
                  selectionBehavior="replace"
                  disallowEmptySelection
                  selectedKeys={selected === null ? [] : [selected]}
                  onSelectionChange={(keys) => {
                    const [k] = keys === 'all' ? [] : keys
                    if (k != null) pick(Number(k))
                  }}
                >
                  {(showAll ? recent : recent.slice(0, recentShown)).map((r) => (
                    <ListView.Item
                      key={r.id}
                      id={r.id}
                      textValue={`${r.merchant_name || `商户 ${r.merchant_id}`} ${yuan(r.pay_amount)} 元`}
                    >
                      <ListView.ItemContent className="flex-1">
                        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                          <div className="flex items-baseline justify-between gap-3">
                            <ListView.Title>{r.merchant_name || `商户 ${r.merchant_id}`}</ListView.Title>
                            <span className="tb-num shrink-0 text-sm font-medium">¥{yuan(r.pay_amount)}</span>
                          </div>
                          <div className="flex items-center justify-between gap-3">
                            <ListView.Description className="tb-num">{time(r.created_at, false)}</ListView.Description>
                            <StateTag state={r.state} />
                          </div>
                        </div>
                      </ListView.ItemContent>
                    </ListView.Item>
                  ))}
                </ListView>
                {recent.length > recentShown && (
                  <Button
                    size="sm"
                    variant="ghost"
                    fullWidth
                    className="mt-1 text-muted"
                    onPress={() => setShowAll((x) => !x)}
                  >
                    {showAll ? '收起' : `显示全部 ${recent.length} 笔`}
                  </Button>
                )}
              </div>
            )}
          </Panel>
        </div>
        <div id="test-progress" className="min-w-0 scroll-mt-20">
          {selected === null ? (
            <Card className="rounded-2xl">
              <Empty icon={<Flask />} title="还没选测试单" description="新建一笔，或点一笔最近的测试单看进度。">
                <Stepper aria-label="测试步骤" size="sm" currentStep={0} className="w-full max-w-md">
                  {['选商户和金额', '在收银台付款', '本站接收回调并验签'].map((t) => (
                    <Stepper.Step key={t}>
                      <Stepper.Indicator />
                      <Stepper.Content>
                        <Stepper.Title>{t}</Stepper.Title>
                      </Stepper.Content>
                      <Stepper.Separator />
                    </Stepper.Step>
                  ))}
                </Stepper>
              </Empty>
            </Card>
          ) : (
            <Progress id={selected} onDetail={setDetail} />
          )}
        </div>
      </div>
      <OrderDetail id={detail} onClose={() => setDetail(null)} />
    </div>
  )
}

// Progress 是一笔测试单的进度：创建 → 付款确认 → 回调验签，每 3 秒刷新。
function Progress({ id, onDetail }: { id: number; onDetail: (id: number) => void }) {
  const qc = useQueryClient()
  const current = useQuery({
    queryKey: ['payment-test', id],
    queryFn: () => api.get<Detail>(`/web-api/admin/orders/${id}`),
    refetchInterval: 3000,
  })
  const receipt = useQuery({
    queryKey: ['test-receipt', id],
    queryFn: () =>
      api.get<{ received: boolean; receipt: { deliveries: number; last_at: number } }>(
        `/web-api/admin/payment-tests/${id}/receipt`,
      ),
    refetchInterval: 3000,
  })
  const close = useMutation({
    mutationFn: () => api.post(`/web-api/admin/orders/${id}/close`),
    onSuccess: async () => {
      toastOK('测试单已关闭')
      await qc.invalidateQueries({ queryKey: ['payment-test', id] })
      await qc.invalidateQueries({ queryKey: ['payment-tests'] })
    },
    onError: toastError,
  })
  const d = current.data?.order.id === id ? current.data : undefined
  if (current.isError) return <Notice status="danger">{current.error.message}</Notice>
  if (!d)
    return (
      <Card className="rounded-2xl">
        <Loading />
      </Card>
    )
  const o = d.order
  const paid = !!d.transaction
  const delivered = d.notification?.status === 'success' && !!receipt.data?.received
  const notifyDead = d.notification?.status === 'dead'
  const ended = !paid && o.state !== 'pending'
  const steps: { title: string; status: StepStatus }[] = [
    { title: '订单已创建', status: 'done' },
    {
      title: paid ? '已确认付款' : ended ? '没有付款' : '等待付款',
      status: paid ? 'done' : ended ? 'error' : 'current',
    },
    {
      title: delivered ? '回调验签通过' : notifyDead ? '回调失败' : '等待回调',
      status: delivered ? 'done' : notifyDead ? 'error' : paid ? 'current' : 'wait',
    },
  ]
  const active = steps.findIndex((s) => s.status !== 'done')
  return (
    <Panel
      title="测试进度"
      description="每 3 秒自动刷新"
      extra={
        <Button size="sm" variant="ghost" onPress={() => onDetail(o.id)}>
          查看流水与通知
        </Button>
      }
    >
      <div className="flex flex-col gap-6">
        <Stepper aria-label="测试进度" size="sm" currentStep={active < 0 ? steps.length : active}>
          {steps.map((s, i) => (
            <Stepper.Step key={i} style={s.status === 'error' ? dangerStep : undefined}>
              <Stepper.Indicator>
                {s.status === 'error' ? (
                  <Stepper.Icon>
                    <Xmark />
                  </Stepper.Icon>
                ) : undefined}
              </Stepper.Indicator>
              <Stepper.Content>
                <Stepper.Title>{s.title}</Stepper.Title>
              </Stepper.Content>
              <Stepper.Separator />
            </Stepper.Step>
          ))}
        </Stepper>

        {!paid && o.state === 'pending' && (
          <div className="flex flex-col items-center gap-3 rounded-2xl bg-accent-soft/60 px-4 py-6 text-center">
            <span className="text-sm text-muted">在收银台按这个金额付款</span>
            <span className="tb-num text-4xl font-semibold tracking-tight">¥{yuan(o.pay_amount)}</span>
            <span className="text-xs text-muted">{relative(o.expires_at)}过期</span>
            <div className="flex flex-wrap justify-center gap-2">
              <Button onPress={() => window.open(`/cashier/${o.trade_no}`, '_blank', 'noopener,noreferrer')}>
                <ArrowUpRightFromSquare />
                打开收银台
              </Button>
              <Confirm
                title="关闭这笔测试单？"
                content="关闭后付款人不能再付款。"
                okText="关闭"
                danger
                onConfirm={() => close.mutate()}
              >
                <Button variant="tertiary" isPending={close.isPending}>
                  关闭测试单
                </Button>
              </Confirm>
            </div>
          </div>
        )}
        {paid && !delivered && !notifyDead && (
          <Notice title="已确认付款，等待回调">
            实收 <span className="tb-num">{yuan(o.paid_amount)}</span> 元。本站正在接收并验证通知签名。
          </Notice>
        )}
        {notifyDead && (
          <Notice status="danger" title="回调没有送达">
            {d.notification?.last_error || '自动通知 8 次都失败了，到「通知」页查看原因。'}
          </Notice>
        )}
        {!notifyDead && d.notification?.last_error && !delivered && (
          <Notice status="warning">{d.notification.last_error}</Notice>
        )}
        {delivered && (
          <div className="flex items-center gap-3 rounded-xl bg-success-soft px-4 py-3 text-sm text-success-soft-foreground">
            <CircleCheck className="size-5 shrink-0" />
            <span>
              整条链路正常：已确认付款，回调验签通过（接收{' '}
              <span className="tb-num">{receipt.data?.receipt.deliveries}</span> 次）。
            </span>
          </div>
        )}
        {ended && (
          <Notice status="warning">这笔测试单已{o.state === 'expired' ? '过期' : '关闭'}，没有确认到付款。</Notice>
        )}

        <Desc
          data={[
            { key: '订单号', value: <Copyable text={o.trade_no} mono /> },
            { key: '状态', value: <StateTag state={o.state} /> },
            { key: '商户', value: o.merchant_name || `商户 ${o.merchant_id}` },
            { key: '渠道', value: o.channel_name || o.channel },
            { key: '回调签名', value: o.version === 2 ? 'V2（RSA）' : 'V1（MD5）' },
            { key: '订单金额', value: <span className="tb-num">{yuan(o.amount)} 元</span> },
            { key: '应付金额', value: <span className="tb-num font-semibold">{yuan(o.pay_amount)} 元</span> },
            paid && { key: '实收金额', value: <span className="tb-num">{yuan(o.paid_amount)} 元</span> },
            { key: '创建时间', value: <span className="tb-num">{time(o.created_at)}</span> },
          ]}
        />
        {receipt.isError && <Notice status="danger">{receipt.error.message}</Notice>}
      </div>
    </Panel>
  )
}
