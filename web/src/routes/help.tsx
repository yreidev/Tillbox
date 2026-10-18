import { Fragment, useEffect, useEffectEvent, useRef, useState, type ReactNode } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery, type UseQueryResult } from '@tanstack/react-query'
import { Button, Form, ItemCard, ItemCardGroup, Separator, Spinner, Stepper } from '@yreidev/ui'
import {
  ArrowLeft,
  ArrowRotateRight,
  CircleCheckFill,
  CircleMinus,
  Clock,
  TriangleExclamation,
} from '@gravity-ui/icons'
import { Logo, siteName } from '../components/brand'
import { api } from '../lib/api'
import { time } from '../lib/format'
import {
  clearPayments,
  rememberPayment,
  rememberReceipt,
  savedReceipts,
  usePaymentHistory,
} from '../lib/payment-history'
import { Copyable, Notice, Panel, Events, Pill, cx, linkClass, toastError, type Tone } from '../lib/ui-base'
import { CheckboxInput, DateTimeInput, SelectInput, TextAreaInput, TextInput } from '../lib/ui-form'

// 付款自助页：付款人自己查订单、提交付款问题、查看处理进度。三步对应页面上的三块，
// 宽屏时进度固定在右侧，手机上从上到下排。

export const Route = createFileRoute('/help')({
  validateSearch: (s: Record<string, unknown>) => ({ trade_no: typeof s.trade_no === 'string' ? s.trade_no : '' }),
  component: HelpPage,
})

interface Lookup {
  trade_no: string
  state: string
  pay_amount: string
  paid_at: number
}
interface Progress {
  status: string
  reply: string
  updated_at: number
  order_state: string
  trade_no: string
}
interface Claim {
  kind: string
  payment_ref: string
  amount: string
  paid_at: number | undefined
  message: string
  contact: string
}

const states: Record<string, string> = {
  pending: '等待付款确认',
  paid: '已确认到账',
  expired: '订单已过期',
  closed: '订单已关闭',
  disputed: '订单待人工核查',
}
const stateTone: Record<string, Tone> = {
  pending: 'accent',
  paid: 'success',
  expired: 'default',
  closed: 'default',
  disputed: 'warning',
}
const statusNames: Record<string, string> = {
  pending: '已提交',
  waiting: '正在核查',
  manual: '人工核查中',
  resolved: '已处理',
}
const statusTone: Record<string, Tone> = {
  pending: 'accent',
  waiting: 'accent',
  manual: 'warning',
  resolved: 'success',
}
const kinds = [
  { value: 'not_received', label: '已付款未到账' },
  { value: 'wrong_amount', label: '付款金额不正确' },
  { value: 'late', label: '订单结束后付款' },
  { value: 'duplicate', label: '重复付款' },
  { value: 'other', label: '其他付款问题' },
]
// short 是手机宽度下的标题，三格并排时完整标题会折行。
const steps = [
  { id: 'help-order', title: '查询订单', short: '查订单', text: '看付款是否已到账' },
  { id: 'help-claim', title: '提交付款问题', short: '提交问题', text: '附上支付宝交易号' },
  { id: 'help-progress', title: '查看处理进度', short: '查进度', text: '凭查询凭证随时查' },
]

const scrollTo = (id: string) => document.getElementById(id)?.scrollIntoView({ behavior: 'smooth', block: 'start' })

// 校验规则和后端一致：订单号 19 位数字；交易号见 internal/assistance/claims.go 的 referencePattern；
// 金额 0.01–1000000 元；付款时间在最近 30 天内。
const checkTradeNo = (v: string) =>
  !v.trim() ? '请填写平台订单号' : /^\d{19}$/.test(v.trim()) ? null : '平台订单号是 19 位数字'
const checkRef = (v: string) =>
  !v.trim() ? '请填写支付宝交易号' : /^[A-Za-z0-9_-]{8,80}$/.test(v.trim()) ? null : '交易号是 8–80 位字母或数字'
const checkAmount = (v: string) => {
  const s = v.trim()
  if (!s) return '请填写实际付款金额'
  if (!/^\d+(\.\d{1,2})?$/.test(s)) return '金额最多两位小数'
  return Number(s) > 0 && Number(s) <= 1_000_000 ? null : '金额要在 0.01–1000000 元之间'
}
// 和后端一致：最近 30 天内，最多比现在晚 5 分钟（容许手机时间不准）。
const checkPaidAt = (ms: number) =>
  ms < Date.now() - 30 * 24 * 3600_000
    ? '付款时间要在最近 30 天内'
    : ms > Date.now() + 5 * 60_000
      ? '付款时间不能晚于现在'
      : null
const checkReceipt = (v: string) =>
  !v.trim() ? '请粘贴查询凭证' : /^[a-f0-9]{64}$/.test(v.trim()) ? null : '凭证是 64 位小写十六进制字符'

function HelpPage() {
  const search = Route.useSearch()
  const fromCashier = /^\d{19}$/.test(search.trade_no) ? search.trade_no : ''
  const [tradeNo, setTradeNo] = useState(search.trade_no)
  const [unknownOrder, setUnknownOrder] = useState(false)
  const history = usePaymentHistory()
  const [lookup, setLookup] = useState<Lookup | null>(null)
  const [looking, setLooking] = useState(false)
  const [lookupError, setLookupError] = useState('')
  const [receipts, setReceipts] = useState(savedReceipts)
  const [receipt, setReceipt] = useState(() => receipts[0]?.receipt || '')
  const progress = useQuery({
    queryKey: ['support-progress', receipt],
    enabled: /^[a-f0-9]{64}$/.test(receipt),
    retry: false,
    queryFn: () => api.post<Progress>('/web-api/self-service/progress', { receipt }),
    refetchInterval: (q) => (['pending', 'waiting'].includes(q.state.data?.status || '') ? 15_000 : false),
  })

  const editTradeNo = (v: string) => {
    setTradeNo(v)
    setLookup(null)
  }
  const remember = (token: string) => {
    setReceipt(token)
    if (rememberReceipt(token)) setReceipts(savedReceipts()) // 存不下时凭证仍可复制
  }
  const lookupOrder = async (no: string) => {
    setLooking(true)
    setLookupError('')
    setLookup(null)
    try {
      const result = await api.post<Lookup>('/web-api/self-service/lookup', { trade_no: no.trim() })
      setLookup(result)
      rememberPayment({
        trade_no: result.trade_no,
        pay_amount: result.pay_amount,
        state: result.state,
        created_at: history.find((p) => p.trade_no === result.trade_no)?.created_at || 0,
      })
    } catch (e) {
      setLookupError((e as Error).message)
    } finally {
      setLooking(false)
    }
  }
  const pick = (no: string) => {
    setTradeNo(no)
    setUnknownOrder(false)
    scrollTo('help-order')
    void lookupOrder(no)
  }

  // 从收银台点进来时带着订单号，直接查一次。
  const autoLooked = useRef(false)
  const autoLookup = useEffectEvent((no: string) => void lookupOrder(no))
  useEffect(() => {
    if (!fromCashier || autoLooked.current) return
    autoLooked.current = true
    autoLookup(fromCashier)
  }, [fromCashier])

  const lookupState = lookup && (progress.data?.trade_no === lookup.trade_no ? progress.data.order_state : lookup.state)
  const step = receipt ? 2 : lookup ? 1 : 0

  return (
    <div className="min-h-dvh">
      <header className="border-b border-separator bg-surface/80 backdrop-blur">
        <div className="mx-auto flex h-14 max-w-6xl items-center justify-between gap-3 px-4 sm:px-6">
          <a
            href="/help"
            className="flex min-w-0 items-center gap-2.5 rounded-lg outline-none focus-visible:ring-2 focus-visible:ring-focus"
          >
            <Logo size={28} />
            <span className="font-semibold">{siteName()}</span>
            <span className="h-4 w-px bg-separator" aria-hidden />
            <span className="truncate text-sm text-muted">付款支持</span>
          </a>
          {fromCashier && (
            <a href={`/cashier/${fromCashier}`} className={cx(linkClass, 'shrink-0 gap-1 text-sm')}>
              <ArrowLeft className="size-3.5" />
              返回收银台
            </a>
          )}
        </div>
      </header>
      <main className="mx-auto max-w-6xl px-4 pt-6 pb-12 sm:px-6 sm:pt-10">
        <div className="max-w-2xl">
          <h1 className="text-2xl font-semibold tracking-tight sm:text-3xl">付款自助服务</h1>
          <p className="mt-2 text-muted">先查订单，再核对付款。提交问题后，随时查看处理进度。</p>
        </div>
        <nav className="mt-6">
          <Stepper aria-label="办理步骤" currentStep={step} onStepChange={(i) => scrollTo(steps[i].id)}>
            {steps.map((s) => (
              <Stepper.Step key={s.id}>
                <Stepper.Indicator />
                <Stepper.Content>
                  <Stepper.Title>
                    <span className="sm:hidden">{s.short}</span>
                    <span className="max-sm:hidden">{s.title}</span>
                  </Stepper.Title>
                  <Stepper.Description className="max-sm:hidden">{s.text}</Stepper.Description>
                </Stepper.Content>
                <Stepper.Separator />
              </Stepper.Step>
            ))}
          </Stepper>
        </nav>
        <div className="mt-6 grid items-start gap-5 lg:grid-cols-[minmax(0,1fr)_400px]">
          <div className="flex min-w-0 flex-col gap-5">
            <section id="help-order" className="scroll-mt-4">
              <Panel title="查询订单" description="用收银台上的平台订单号查询这笔付款的状态。">
                <Form
                  className="flex flex-col gap-3 sm:flex-row sm:items-start"
                  onSubmit={(e) => {
                    e.preventDefault()
                    void lookupOrder(tradeNo)
                  }}
                >
                  <TextInput
                    label="平台订单号"
                    className="sm:flex-1"
                    placeholder="收银台上的 19 位订单号"
                    inputMode="numeric"
                    maxLength={19}
                    mono
                    value={tradeNo}
                    onChange={editTradeNo}
                    validate={checkTradeNo}
                  />
                  <Button type="submit" className="sm:mt-6" isPending={looking}>
                    查询支付状态
                  </Button>
                </Form>
                {lookupError && (
                  <Notice status="danger" className="mt-4">
                    {lookupError}
                  </Notice>
                )}
                {lookup && lookupState && (
                  <LookupResult
                    lookup={lookup}
                    state={lookupState}
                    onClaim={() => {
                      setUnknownOrder(false)
                      scrollTo('help-claim')
                    }}
                  />
                )}
                <div className="mt-6 border-t border-separator pt-5">
                  <div className="flex items-center justify-between gap-3">
                    <h3 className="text-sm font-medium">最近支付</h3>
                    {history.length > 0 && (
                      <Button
                        size="sm"
                        variant="ghost"
                        className="text-muted"
                        onPress={() => {
                          if (!clearPayments()) toastError(new Error('无法清除记录，请在浏览器设置中清除本站数据'))
                        }}
                      >
                        清除记录
                      </Button>
                    )}
                  </div>
                  <p className="mt-1 text-xs leading-relaxed text-muted">
                    仅保存在当前浏览器，最多 20 笔、保留 30
                    天。换浏览器、无痕模式或清除网站数据后无法找回。状态以查询结果为准。
                  </p>
                  {history.length === 0 ? (
                    <p className="mt-3 rounded-xl bg-surface-secondary px-4 py-3 text-sm text-muted">
                      暂无记录。可输入订单号查询，或在下方勾选“不知道平台订单号”提交付款问题。
                    </p>
                  ) : (
                    <ItemCardGroup variant="outline" className="mt-3">
                      {history.map((p, i) => (
                        <Fragment key={p.trade_no}>
                          {i > 0 && <Separator />}
                          <ItemCard className="px-3 py-2.5 sm:px-4">
                            <ItemCard.Content>
                              <ItemCard.Title className="tb-mono">{p.trade_no}</ItemCard.Title>
                              <ItemCard.Description>
                                应付 <span className="tb-num">¥{p.pay_amount}</span> · {states[p.state]}（上次记录）
                              </ItemCard.Description>
                            </ItemCard.Content>
                            <ItemCard.Action>
                              <Button
                                size="sm"
                                variant="secondary"
                                isDisabled={looking}
                                aria-label={`查询订单 ${p.trade_no}`}
                                onPress={() => pick(p.trade_no)}
                              >
                                查询 / 申诉
                              </Button>
                            </ItemCard.Action>
                          </ItemCard>
                        </Fragment>
                      ))}
                    </ItemCardGroup>
                  )}
                </div>
              </Panel>
            </section>
            <section id="help-claim" className="scroll-mt-4">
              <ClaimForm
                tradeNo={tradeNo}
                onTradeNo={editTradeNo}
                unknownOrder={unknownOrder}
                onUnknownOrder={setUnknownOrder}
                onSubmitted={(token) => {
                  remember(token)
                  scrollTo('help-progress')
                }}
              />
            </section>
          </div>
          <section id="help-progress" className="scroll-mt-4 lg:sticky lg:top-4">
            <Panel
              title="申诉处理进度"
              description={!receipt ? '提交付款问题后这里会自动显示进度，也可以粘贴已保存的查询凭证。' : undefined}
            >
              <div className="flex flex-col gap-5">
                {receipt && <ProgressView receipt={receipt} progress={progress} busy={looking} onPick={pick} />}
                {receipts.length > 1 && (
                  <div className={cx(receipt && 'border-t border-separator pt-5')}>
                    <p className="text-xs text-muted">本机保存的查询凭证（最近 10 张，仅保存在当前浏览器）</p>
                    <ItemCardGroup variant="outline" className="mt-2">
                      {receipts.map((r, i) => (
                        <Fragment key={r.receipt}>
                          {i > 0 && <Separator />}
                          <ItemCard className="px-3 py-2">
                            <ItemCard.Content>
                              <ItemCard.Title className="tb-mono">
                                {r.receipt.slice(0, 8)}…{r.receipt.slice(-6)}
                              </ItemCard.Title>
                              <ItemCard.Description>
                                {r.saved_at ? `保存于 ${time(r.saved_at)}` : '较早保存'}
                              </ItemCard.Description>
                            </ItemCard.Content>
                            <ItemCard.Action>
                              <Button
                                size="sm"
                                variant="secondary"
                                isDisabled={r.receipt === receipt}
                                onPress={() => setReceipt(r.receipt)}
                              >
                                {r.receipt === receipt ? '正在查看' : '查看进度'}
                              </Button>
                            </ItemCard.Action>
                          </ItemCard>
                        </Fragment>
                      ))}
                    </ItemCardGroup>
                  </div>
                )}
                <ReceiptForm onSubmit={remember} divided={!!receipt || receipts.length > 1} />
              </div>
            </Panel>
          </section>
        </div>
        <p className="mt-10 text-center text-xs text-muted">
          到账以真实支付流水为准。涉及退款及业务站余额的问题，由商户核实处理。
        </p>
      </main>
    </div>
  )
}

const resultIcon: Record<string, { icon: ReactNode; className: string }> = {
  paid: { icon: <CircleCheckFill />, className: 'bg-success-soft text-success' },
  pending: { icon: <Clock />, className: 'bg-accent-soft text-accent' },
  disputed: { icon: <TriangleExclamation />, className: 'bg-warning-soft text-warning' },
}

function LookupResult({ lookup, state, onClaim }: { lookup: Lookup; state: string; onClaim: () => void }) {
  const look = resultIcon[state] ?? { icon: <CircleMinus />, className: 'bg-default text-muted' }
  return (
    <div role="status" className="mt-4 rounded-2xl border border-separator p-4">
      <div className="flex items-start gap-3">
        <span className={cx('grid size-10 shrink-0 place-items-center rounded-full [&>svg]:size-5', look.className)}>
          {look.icon}
        </span>
        <div className="min-w-0 flex-1">
          <p className="font-semibold">{states[state] || state}</p>
          <p className="mt-0.5 text-xs text-muted">
            订单 <span className="tb-mono">{lookup.trade_no}</span>
          </p>
        </div>
      </div>
      <dl className="mt-4 grid grid-cols-2 gap-3 rounded-xl bg-surface-secondary px-4 py-3 text-sm">
        <div>
          <dt className="text-xs text-muted">应付金额</dt>
          <dd className="tb-num mt-0.5 font-medium">¥{lookup.pay_amount}</dd>
        </div>
        <div>
          <dt className="text-xs text-muted">到账时间</dt>
          <dd className="tb-num mt-0.5 font-medium">{lookup.paid_at > 0 ? time(lookup.paid_at, false) : '—'}</dd>
        </div>
      </dl>
      <div className="mt-4 flex flex-wrap items-center justify-between gap-3">
        <a href={`/cashier/${lookup.trade_no}`} className={cx(linkClass, 'text-sm')}>
          查看收银台
        </a>
        <Button size="sm" variant="secondary" onPress={onClaim}>
          这笔付款有问题
        </Button>
      </div>
    </div>
  )
}

function ClaimForm({
  tradeNo,
  onTradeNo,
  unknownOrder,
  onUnknownOrder,
  onSubmitted,
}: {
  tradeNo: string
  onTradeNo: (v: string) => void
  unknownOrder: boolean
  onUnknownOrder: (v: boolean) => void
  onSubmitted: (receipt: string) => void
}) {
  const [opened] = useState(() => Date.now())
  const [v, setV] = useState<Claim>(() => ({
    kind: 'not_received',
    payment_ref: '',
    amount: '',
    paid_at: Math.floor(opened / 60_000) * 60_000,
    message: '',
    contact: '',
  }))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const set = <K extends keyof Claim>(k: K, x: Claim[K]) => setV((s) => ({ ...s, [k]: x }))

  return (
    <Panel
      title="提交付款问题"
      description="我们会核对真实流水；无法自动确认的问题会转人工处理。请保留支付宝账单备查。"
    >
      <Form
        className="flex flex-col gap-4"
        onSubmit={async (e) => {
          e.preventDefault()
          setBusy(true)
          setError('')
          try {
            const result = await api.post<{ receipt: string }>('/web-api/self-service/claims', {
              trade_no: unknownOrder ? '' : tradeNo.trim(),
              kind: v.kind,
              payment_ref: v.payment_ref.trim(),
              amount: Number(v.amount.trim()).toFixed(2),
              paid_at: v.paid_at ?? 0,
              message: v.message,
              contact: v.contact,
            })
            onSubmitted(result.receipt)
          } catch (err) {
            setError((err as Error).message)
          } finally {
            setBusy(false)
          }
        }}
      >
        <div className="flex flex-col gap-3">
          <CheckboxInput label="不知道平台订单号" value={unknownOrder} onChange={onUnknownOrder} />
          {unknownOrder ? (
            <Notice status="warning">
              填写支付宝账单中的交易号、实际金额和付款时间。证据核对后关联订单；无法确认归属时转人工，请补充原业务站名称等线索。支付宝交易号与平台订单号不同。
            </Notice>
          ) : (
            <TextInput
              label="问题订单号"
              isRequired
              inputMode="numeric"
              maxLength={19}
              mono
              value={tradeNo}
              onChange={onTradeNo}
              validate={checkTradeNo}
            />
          )}
        </div>
        <SelectInput<string> label="问题类型" value={v.kind} onChange={(k) => k && set('kind', k)} options={kinds} />
        <TextInput
          label="支付宝交易号"
          isRequired
          placeholder="在支付宝账单详情中复制交易号"
          maxLength={80}
          mono
          autoComplete="off"
          value={v.payment_ref}
          onChange={(x) => set('payment_ref', x)}
          validate={checkRef}
        />
        <div className="grid gap-4 sm:grid-cols-2">
          <TextInput
            label="实际付款金额（元）"
            isRequired
            inputMode="decimal"
            placeholder="0.00"
            value={v.amount}
            onChange={(x) => set('amount', x)}
            validate={checkAmount}
          />
          <DateTimeInput
            label="付款时间"
            description="最近 30 天内"
            isRequired
            value={v.paid_at}
            onChange={(x) => set('paid_at', x)}
            minValue={opened - 30 * 24 * 3600_000}
            validate={checkPaidAt}
          />
        </div>
        <TextAreaInput
          label="补充说明（可选）"
          rows={3}
          maxLength={1000}
          placeholder="描述遇到的问题；请勿填写密码、密钥或其他敏感信息"
          value={v.message}
          onChange={(x) => set('message', x)}
        />
        <TextInput
          label="联系方式（可选）"
          maxLength={100}
          placeholder="仅供处理人员联系你"
          value={v.contact}
          onChange={(x) => set('contact', x)}
        />
        <p className="text-xs leading-relaxed text-muted">
          付款说明及必要的金额、时间可能由本站配置的 AI 辅助核查。联系方式仅用于人工处理。
        </p>
        {error && <Notice status="danger">{error}</Notice>}
        <div>
          <Button type="submit" isPending={busy}>
            提交并获取查询凭证
          </Button>
        </div>
      </Form>
    </Panel>
  )
}

function ProgressView({
  receipt,
  progress,
  busy,
  onPick,
}: {
  receipt: string
  progress: UseQueryResult<Progress>
  busy: boolean
  onPick: (tradeNo: string) => void
}) {
  const d = progress.data
  const s = d?.status ?? ''
  const known = s in statusNames
  return (
    <div className="flex flex-col gap-4">
      <div className="rounded-xl bg-surface-secondary px-3 py-2.5">
        <p className="text-xs text-muted">查询凭证</p>
        <div className="mt-1 text-sm">
          <Copyable text={receipt} mono />
        </div>
        <p className="mt-1.5 text-xs text-muted">请保存这个查询凭证，换设备后可用它查询。请勿分享给他人。</p>
      </div>
      {progress.isPending && (
        <div role="status" className="flex items-center gap-2 text-sm text-muted">
          <Spinner size="sm" />
          正在查询…
        </div>
      )}
      {progress.isError && <Notice status="danger">{progress.error.message}</Notice>}
      {d && (
        <div role="status" className="flex flex-col gap-4">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <Pill tone={statusTone[s] ?? 'default'}>{statusNames[s] || s}</Pill>
            <span className="text-xs text-muted">
              更新于 <span className="tb-num">{time(d.updated_at)}</span>
            </span>
          </div>
          {known && (
            <Events
              items={[
                { key: 'submitted', title: '已提交', step: 'done' },
                {
                  key: 'check',
                  title:
                    s === 'manual'
                      ? '人工核查中'
                      : s === 'waiting'
                        ? '正在核查'
                        : s === 'resolved'
                          ? '核查完成'
                          : '等待核查',
                  step: s === 'resolved' ? 'done' : 'current',
                },
                { key: 'resolved', title: '已处理', step: s === 'resolved' ? 'done' : 'wait' },
              ]}
            />
          )}
          <div className="text-sm">
            {d.trade_no ? (
              <span className="flex flex-wrap items-center gap-x-2 gap-y-1 text-muted">
                订单 <span className="tb-mono text-foreground">{d.trade_no}</span>
                <Pill tone={stateTone[d.order_state] ?? 'default'}>{states[d.order_state] || d.order_state}</Pill>
              </span>
            ) : (
              <span className="text-muted">正在核对付款凭证，尚未确认平台订单。</span>
            )}
          </div>
          {d.reply && (
            <blockquote className="rounded-xl border-l-4 border-accent bg-accent-soft/50 px-4 py-3 text-sm leading-relaxed whitespace-pre-wrap">
              {d.reply}
            </blockquote>
          )}
          <div className="flex flex-wrap gap-2">
            <Button
              size="sm"
              variant="secondary"
              isPending={progress.isFetching}
              onPress={() => void progress.refetch()}
            >
              <ArrowRotateRight />
              刷新进度
            </Button>
            {d.trade_no && (
              <Button size="sm" variant="tertiary" isDisabled={busy} onPress={() => onPick(d.trade_no)}>
                查询关联订单
              </Button>
            )}
          </div>
        </div>
      )}
    </div>
  )
}

function ReceiptForm({ onSubmit, divided }: { onSubmit: (receipt: string) => void; divided: boolean }) {
  const [value, setValue] = useState('')
  return (
    <Form
      className={cx('flex flex-col gap-3', divided && 'border-t border-separator pt-5')}
      onSubmit={(e) => {
        e.preventDefault()
        onSubmit(value.trim())
        setValue('')
      }}
    >
      <TextInput
        label="已有查询凭证"
        placeholder="粘贴提交时保存的凭证"
        autoComplete="off"
        mono
        value={value}
        onChange={setValue}
        validate={checkReceipt}
      />
      <div>
        <Button type="submit" variant="secondary">
          查询申诉进度
        </Button>
      </div>
    </Form>
  )
}
