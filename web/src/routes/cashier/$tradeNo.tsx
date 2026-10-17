import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Button, Card, Spinner } from '@heroui/react'
import {
  ArrowUpRightFromSquare,
  Check,
  ChevronRight,
  CircleCheckFill,
  CircleExclamation,
  CircleMinus,
  CircleQuestion,
  Clock,
  Copy,
  ShieldCheck,
} from '@gravity-ui/icons'
import { encode } from 'uqr'
import { Logo } from '../../components/brand'
import { ApiError, api } from '../../lib/api'
import { time } from '../../lib/format'
import { rememberPayment, usePaymentHistory } from '../../lib/payment-history'
import { Notice, copyText, cx } from '../../lib/ui-base'

// 收银台：付款人看到的页面，手机优先。一张窄卡片：顶部是订单和倒计时，中间是应付金额和付款方式，
// 最下面是唯一的主操作（扫码后点“我已付款”，或跳去付款），卡片底部是付款自助入口。

interface Guide {
  qrcode: string
  payee_name: string
  app: string
  manual_amount: boolean
  /** 上游渠道：“去付款”按钮的链接（上游付款页或唤起 App 的链接） */
  pay_url?: string
  /** 上游渠道：要先向本站取上游收银台的跳转地址 */
  submit?: boolean
}

interface PayTypeOption {
  type: string
  label: string
  /** 这笔订单不能用这种方式的原因（超出的单笔限额），能用时没有 */
  unavailable?: string
}

interface CashierOrder {
  trade_no: string
  name: string
  amount: string
  pay_amount: string
  state: 'pending' | 'paid' | 'expired' | 'closed' | 'disputed'
  created_at: number
  expires_at: number
  paid_at: number
  server_time: number
  site_name: string
  has_return: boolean
  guide?: Guide
  /** 上游渠道的订单才有：可选的支付方式和当前选定的方式 */
  pay_types?: PayTypeOption[]
  pay_type?: string
}

export const Route = createFileRoute('/cashier/$tradeNo')({
  component: Cashier,
})

// qrPNG 把内容画成二维码 PNG（手机上长按可以保存到相册）。
function qrPNG(text: string): string {
  const qr = encode(text, { ecc: 'M', border: 2 })
  const scale = 8
  const canvas = document.createElement('canvas')
  canvas.width = canvas.height = qr.size * scale
  const ctx = canvas.getContext('2d')
  if (!ctx) return ''
  ctx.fillStyle = '#fff'
  ctx.fillRect(0, 0, canvas.width, canvas.height)
  ctx.fillStyle = '#000'
  qr.data.forEach((row, y) =>
    row.forEach((on, x) => {
      if (on) ctx.fillRect(x * scale, y * scale, scale, scale)
    }),
  )
  return canvas.toDataURL('image/png')
}

// appName 把支付方式名换成付款人要打开的 App 名。
function appName(app: string) {
  return ({ 微信支付: '微信', QQ钱包: 'QQ', 京东支付: '京东' } as Record<string, string>)[app] ?? (app || '支付宝')
}

function countdown(left: number) {
  return `${String(Math.floor(left / 60)).padStart(2, '0')}:${String(left % 60).padStart(2, '0')}`
}

// useClock 返回本机时间，每秒走一次，走到 until（本机时间）为止。
function useClock(until: number) {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (Date.now() >= until) return
    const t = setInterval(() => {
      const n = Date.now()
      setNow(n)
      if (n >= until) clearInterval(t)
    }, 1000)
    return () => clearInterval(t)
  }, [until])
  return now
}

// 到期前最后几秒付的款要等下一轮查账才能确认。到期（或到期后点"我已付款"）之后继续确认这么久，
// 免得付款人看到"已过期"又付一次。
const confirmWindow = 3 * 60_000

const isMobile =
  typeof navigator !== 'undefined' && /Android|iPhone|iPad|iPod|Mobile|HarmonyOS/i.test(navigator.userAgent)
// 在支付宝 App 里打开的收银台：付款人用支付宝扫了商户页面上的二维码进来。
const inAlipay = typeof navigator !== 'undefined' && /AlipayClient/i.test(navigator.userAgent)

function Cashier() {
  const { tradeNo } = Route.useParams()
  const qc = useQueryClient()
  const [claimedAt, setClaimedAt] = useState(0)
  const [choosing, setChoosing] = useState(false)
  const claimed = claimedAt > 0
  const confirmUntil = (o?: CashierOrder) =>
    o?.state === 'expired' ? Math.max(o.expires_at, claimedAt) + confirmWindow : 0
  const q = useQuery({
    queryKey: ['cashier', tradeNo],
    queryFn: () => api.get<CashierOrder>(`/web-api/cashier/${tradeNo}`),
    refetchInterval: (query) => {
      const d = query.state.data
      if (d?.state === 'pending') return claimed ? 2_000 : 3_000
      if (d && Date.now() + d.server_time - query.state.dataUpdatedAt < confirmUntil(d)) return claimed ? 3_000 : 5_000
      return false
    },
    refetchIntervalInBackground: true,
    staleTime: 0,
  })
  const o = q.data
  // 本机时钟比服务器慢多少：服务器时间减去收到这份数据时的本机时间
  const offset = o ? o.server_time - q.dataUpdatedAt : 0
  const pending = o?.state === 'pending'
  // 待付款时一直走（倒计时）；过期后走到确认窗口结束
  const now = useClock(pending ? Infinity : confirmUntil(o) - offset) + offset
  const confirming = now < confirmUntil(o)
  const left = o ? Math.max(0, Math.floor((o.expires_at - now) / 1000)) : 0

  // 倒计时走完时再查一次，拿到准确状态。
  const refetch = q.refetch
  useEffect(() => {
    if (pending && left === 0) void refetch()
  }, [pending, left, refetch])

  useEffect(() => {
    if (o?.site_name) document.title = `收银台 - ${o.site_name}`
  }, [o?.site_name])

  // 打开过的订单记在本机，付款自助页里能直接选；本机记录里有它才提示“已保存”。
  const history = usePaymentHistory()
  const historySaved = !!o && history.some((p) => p.trade_no === o.trade_no)
  const visitNo = o?.trade_no ?? ''
  const visitAmount = o?.pay_amount ?? ''
  const visitState = o?.state ?? ''
  const visitCreated = o?.created_at ?? 0
  useEffect(() => {
    if (visitNo)
      rememberPayment({ trade_no: visitNo, pay_amount: visitAmount, state: visitState, created_at: visitCreated })
  }, [visitNo, visitAmount, visitState, visitCreated])

  const paid = useMutation({
    mutationFn: () => api.post(`/web-api/cashier/${tradeNo}/paid`),
    onSuccess: () => setClaimedAt(Date.now() + offset),
  })
  // 上游渠道：选定支付方式后向上游预下单，拿到二维码或付款链接。
  const choose = useMutation({
    mutationFn: (type: string) =>
      api.post<{ guide: Guide; pay_type: string }>(`/web-api/cashier/${tradeNo}/pay`, { type }),
    onSuccess: (r) => {
      qc.setQueryData<CashierOrder>(['cashier', tradeNo], (old) =>
        old ? { ...old, guide: r.guide, pay_type: r.pay_type } : old,
      )
      setChoosing(false)
    },
    onError: (e) => {
      // 409 是订单状态变了；422 是金额超出这种方式的单笔限额，刷新后这种方式会标成不可用
      if (e instanceof ApiError && (e.status === 409 || e.status === 422)) void q.refetch()
    },
  })

  if (q.isPending) {
    return (
      <Shell>
        <div role="status" className="flex min-h-72 flex-col items-center justify-center gap-3 text-sm text-muted">
          <Spinner size="lg" />
          正在加载订单…
        </div>
      </Shell>
    )
  }
  if (q.isError || !o) {
    const notFound = q.error instanceof ApiError && q.error.status === 404
    return (
      <Shell>
        <Outcome
          icon={<CircleExclamation />}
          tone="danger"
          title={notFound ? '订单不存在' : '加载失败'}
          text={notFound ? '请返回商户网站重新下单。' : q.error?.message}
        >
          {!notFound && (
            <Button fullWidth size="lg" onPress={() => void q.refetch()}>
              重试
            </Button>
          )}
        </Outcome>
      </Shell>
    )
  }

  return (
    <Shell site={o.site_name}>
      <header className="flex items-start gap-3 border-b border-separator px-5 pt-5 pb-4 sm:px-6">
        <div className="min-w-0 flex-1">
          <p className="truncate font-medium" title={o.name}>
            {o.name}
          </p>
          <p className="mt-0.5 text-xs text-muted">
            订单号 <span className="tb-mono">{o.trade_no}</span>
          </p>
        </div>
        {pending && <Timer left={left} />}
      </header>
      {o.state === 'paid' ? (
        <Paid order={o} />
      ) : o.state === 'pending' && o.pay_types && (!o.guide || choosing) ? (
        <Choose
          order={o}
          busy={choose.isPending ? choose.variables : undefined}
          error={choose.isError ? choose.error.message : ''}
          onChoose={(t) => choose.mutate(t)}
          onCancel={o.guide ? () => setChoosing(false) : undefined}
        />
      ) : o.state === 'pending' && o.guide ? (
        <Pay
          order={o}
          guide={o.guide}
          claimed={claimed}
          onPaid={() => paid.mutate()}
          busy={paid.isPending}
          onSwitch={o.pay_types && o.pay_types.length > 1 ? () => setChoosing(true) : undefined}
        />
      ) : confirming ? (
        <Confirming claimed={claimed} onPaid={() => paid.mutate()} busy={paid.isPending} />
      ) : (
        <Outcome
          icon={<CircleMinus />}
          tone="default"
          title={o.state === 'closed' ? '订单已关闭' : o.state === 'disputed' ? '订单待人工处理' : '订单已过期'}
          text={
            o.state === 'disputed'
              ? '这笔订单的到账需要人工核对，请联系商户。'
              : '如果你已经付了款，请不要重复付款，保留付款账单，通过下方“付款问题与自助查询”提交申诉；没有付款的话请返回商户网站重新下单。'
          }
        />
      )}
      {paid.isError && (
        <div className="px-5 pb-5 sm:px-6">
          <Notice status="danger">{paid.error.message}，请稍后重试。</Notice>
        </div>
      )}
      <a
        href={`/help?trade_no=${o.trade_no}`}
        className="flex items-center gap-3 border-t border-separator bg-surface-secondary/50 px-5 py-4 transition-colors outline-none hover:bg-surface-secondary focus-visible:bg-surface-secondary sm:px-6"
      >
        <span className="grid size-9 shrink-0 place-items-center rounded-xl bg-default text-muted">
          <CircleQuestion className="size-4" />
        </span>
        <span className="min-w-0 flex-1">
          <span className="block text-sm font-medium">付款问题与自助查询</span>
          <span className="mt-0.5 block text-xs text-muted">
            {historySaved
              ? '订单已保存在此浏览器，关闭后可到付款自助页查看最近支付。'
              : '请保存订单号或收藏此页，供后续查询。'}
          </span>
        </span>
        <ChevronRight className="size-4 shrink-0 text-muted" />
      </a>
    </Shell>
  )
}

function Shell({ site, children }: { site?: string; children: ReactNode }) {
  return (
    <div className="min-h-dvh px-4 pt-5 pb-8 sm:pt-12">
      <main className="mx-auto flex w-full max-w-[440px] flex-col gap-4">
        <div className="flex min-w-0 items-center gap-2.5 px-1">
          <Logo size={28} />
          <span className="truncate text-base font-semibold">{site || '收银台'}</span>
        </div>
        <Card className="gap-0 overflow-hidden p-0">{children}</Card>
        <p className="flex items-start justify-center gap-1.5 px-3 text-xs leading-relaxed text-muted">
          <ShieldCheck className="mt-0.5 size-3.5 shrink-0" />
          <span>确认收款方与应付金额后再付款。请勿向陌生人转账，遇到问题可使用付款自助服务。</span>
        </p>
      </main>
    </div>
  )
}

function Timer({ left }: { left: number }) {
  const urgent = left <= 60
  return (
    <span
      role="timer"
      aria-label={`剩余支付时间 ${countdown(left)}`}
      className={cx(
        'inline-flex shrink-0 items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium',
        urgent ? 'bg-danger-soft text-danger-soft-foreground' : 'bg-default text-muted',
      )}
    >
      <Clock className="size-3.5" />
      剩余 <span className="tb-num">{countdown(left)}</span>
    </span>
  )
}

// Amount 是卡片中央的大号金额。
function Amount({ label, value, children }: { label: string; value: string; children?: ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-1 text-center">
      <span className="text-sm text-muted">{label}</span>
      <div className="flex items-baseline gap-1 font-semibold tracking-tight">
        <span className="text-2xl">¥</span>
        <span className="tb-num text-[2.75rem] leading-tight">{value}</span>
      </div>
      {children}
    </div>
  )
}

function Waiting({ text }: { text: string }) {
  return (
    <div
      role="status"
      className="flex items-center justify-center gap-2.5 rounded-2xl bg-accent-soft px-4 py-3 text-sm text-accent-soft-foreground"
    >
      <Spinner size="sm" color="current" />
      {text}
    </div>
  )
}

const outcomeTone = {
  success: 'bg-success-soft text-success',
  danger: 'bg-danger-soft text-danger',
  warning: 'bg-warning-soft text-warning',
  default: 'bg-default text-muted',
}

// Outcome 是已支付、已过期、加载失败这类结果页：大图标、标题、一段说明，下面是按钮。
function Outcome({
  icon,
  tone,
  title,
  text,
  children,
}: {
  icon: ReactNode
  tone: keyof typeof outcomeTone
  title: string
  text?: ReactNode
  children?: ReactNode
}) {
  return (
    <div className="flex flex-col items-center px-5 py-9 text-center sm:px-6">
      <span className={cx('grid size-16 place-items-center rounded-full [&>svg]:size-8', outcomeTone[tone])}>
        {icon}
      </span>
      <h1 className="mt-4 text-xl font-semibold">{title}</h1>
      {text && <p className="mt-2 max-w-80 text-sm leading-relaxed text-muted">{text}</p>}
      {children && <div className="mt-6 flex w-full flex-col items-center gap-2">{children}</div>}
    </div>
  )
}

// Choose 是上游渠道订单选择支付方式的页面。
function Choose({
  order,
  busy,
  error,
  onChoose,
  onCancel,
}: {
  order: CashierOrder
  busy?: string
  error: string
  onChoose: (type: string) => void
  onCancel?: () => void
}) {
  return (
    <div className="flex flex-col gap-6 px-5 py-6 sm:px-6">
      <Amount label="应付金额" value={order.pay_amount} />
      <div className="flex flex-col gap-2">
        <p className="text-sm font-medium">选择支付方式</p>
        {(order.pay_types ?? []).map((t) => {
          const current = t.type === order.pay_type
          return (
            <Button
              key={t.type}
              fullWidth
              size="lg"
              variant={current ? 'secondary' : 'outline'}
              className="justify-between"
              isPending={busy === t.type}
              isDisabled={!!t.unavailable || (!!busy && busy !== t.type)}
              onPress={() => onChoose(t.type)}
            >
              <span className="flex-1 text-left">{busy === t.type ? '正在获取付款信息…' : t.label}</span>
              {t.unavailable ? (
                <span className="text-sm font-normal text-muted">{t.unavailable}</span>
              ) : current ? (
                <Check className="text-accent" />
              ) : (
                <ChevronRight className="text-muted" />
              )}
            </Button>
          )
        })}
      </div>
      {error && <Notice status="danger">{error}</Notice>}
      {onCancel && (
        <Button fullWidth variant="ghost" onPress={onCancel} isDisabled={!!busy}>
          返回
        </Button>
      )}
    </div>
  )
}

// openURL 打开付款链接：电脑上开新窗口，手机上在当前页跳转（唤起 App）。
function openURL(url: string, newTab: boolean) {
  if (newTab) window.open(url, '_blank', 'noopener,noreferrer')
  else window.location.assign(url)
}

function Pay({
  order,
  guide,
  claimed,
  onPaid,
  busy,
  onSwitch,
}: {
  order: CashierOrder
  guide: Guide
  claimed: boolean
  onPaid: () => void
  busy: boolean
  onSwitch?: () => void
}) {
  const png = useMemo(() => (guide.qrcode ? qrPNG(guide.qrcode) : ''), [guide.qrcode])
  const [copied, setCopied] = useState(false)
  const [going, setGoing] = useState(false)
  const [goError, setGoError] = useState('')
  const differs = order.pay_amount !== order.amount
  const upstream = !!order.pay_types
  const alipayQR = /^https:\/\/qr\.alipay\.com\//i.test(guide.qrcode)
  // 手机上扫不了自己屏幕上的码：二维码内容是网址时给个直接打开的按钮，作为主操作。
  const openQR = isMobile && (alipayQR || (upstream && /^https?:\/\//i.test(guide.qrcode))) ? guide.qrcode : ''
  const app = appName(guide.app)
  // 已经在支付宝里了，再让人扫码就绕回去了：不显示二维码，只给付款按钮。
  const inApp = !!openQR && inAlipay && app === '支付宝'
  const goButton = !!(guide.pay_url || guide.submit)

  useEffect(() => {
    if (!copied) return
    const t = setTimeout(() => setCopied(false), 2000)
    return () => clearTimeout(t)
  }, [copied])

  // goSubmit 向本站取上游收银台的跳转地址再跳过去。电脑上先开好新窗口，免得被拦截弹窗。
  const goSubmit = async () => {
    setGoing(true)
    setGoError('')
    const w = isMobile ? null : window.open('about:blank', '_blank')
    try {
      const r = await api.post<{ url: string }>(`/web-api/cashier/${order.trade_no}/go`)
      if (w) {
        w.opener = null
        w.location.href = r.url
      } else {
        window.location.assign(r.url)
      }
    } catch (e) {
      w?.close()
      setGoError((e as Error).message)
    } finally {
      setGoing(false)
    }
  }
  const newTab = !isMobile && !!guide.pay_url && /^https?:\/\//i.test(guide.pay_url)

  return (
    <div className="flex flex-col gap-5 px-5 py-6 sm:px-6">
      <Amount label="请付款" value={order.pay_amount}>
        {!upstream && (
          <Button
            size="sm"
            variant="tertiary"
            className="mt-1"
            aria-label="复制应付金额"
            onPress={async () => setCopied(await copyText(order.pay_amount, true))}
          >
            {copied ? <Check /> : <Copy />}
            {copied ? '已复制' : '复制金额'}
          </Button>
        )}
      </Amount>
      {guide.manual_amount && (
        <Notice status="warning">
          付款时请<b>手动输入 {order.pay_amount} 元</b>，金额要一分不差
          {differs && <>（订单金额 {order.amount} 元，尾数用来识别你的订单）</>}，否则无法自动到账。
        </Notice>
      )}
      {png && !inApp && (
        <figure className="flex flex-col items-center gap-3">
          <div className="rounded-2xl border border-separator bg-white p-2 shadow-surface">
            <img src={png} alt="付款二维码" width={220} height={220} className="block size-[220px]" />
          </div>
          <figcaption className="flex flex-col items-center gap-0.5 text-center text-sm">
            <span>
              打开<b className="font-semibold">{app}</b>扫一扫
            </span>
            {guide.payee_name && (
              <span className="text-muted">
                收款方：<span className="font-medium text-foreground">{guide.payee_name}</span>
              </span>
            )}
          </figcaption>
        </figure>
      )}
      {inApp && guide.payee_name && (
        <p className="text-center text-sm text-muted">
          收款方：<span className="font-medium text-foreground">{guide.payee_name}</span>
        </p>
      )}
      {(openQR || (png && isMobile)) && (
        <div className="flex flex-col gap-2">
          {openQR && (
            <Button fullWidth size="lg" onPress={() => openURL(openQR, false)}>
              {inApp ? '立即付款' : alipayQR ? '打开支付宝付款' : '在本机打开付款页'}
            </Button>
          )}
          {inApp ? (
            <p className="text-center text-xs text-muted">付款完成后回到这个页面，等待到账确认。</p>
          ) : (
            png &&
            isMobile && (
              <p className="text-center text-xs text-muted">
                也可以长按二维码保存到相册，在{app}“扫一扫”里从相册选择。
              </p>
            )
          )}
        </div>
      )}
      {goButton && (
        <div className="flex flex-col gap-2">
          {guide.pay_url && (
            <Button fullWidth size="lg" onPress={() => openURL(guide.pay_url!, newTab)}>
              去{app}付款
              {newTab && <ArrowUpRightFromSquare />}
            </Button>
          )}
          {guide.submit && (
            <Button fullWidth size="lg" isPending={going} onPress={() => void goSubmit()}>
              {going ? '正在跳转…' : `去${app}付款`}
              {!isMobile && <ArrowUpRightFromSquare />}
            </Button>
          )}
          {goError && <Notice status="danger">{goError}</Notice>}
          <p className="text-center text-xs text-muted">付款完成后回到这个页面，等待到账确认。</p>
        </div>
      )}
      <div className="flex flex-col gap-2 border-t border-separator pt-5">
        {claimed ? (
          <Waiting text="正在确认到账，通常 10–30 秒，请不要关闭页面" />
        ) : (
          <Button
            fullWidth
            size="lg"
            variant={goButton || openQR ? 'secondary' : 'primary'}
            onPress={onPaid}
            isPending={busy}
          >
            我已付款
          </Button>
        )}
        {onSwitch && !claimed && (
          <Button fullWidth variant="ghost" onPress={onSwitch}>
            换一种支付方式
          </Button>
        )}
      </div>
    </div>
  )
}

// Confirming 是订单刚到期、到期前的付款可能还没查到时的页面。
function Confirming({ claimed, onPaid, busy }: { claimed: boolean; onPaid: () => void; busy: boolean }) {
  return (
    <Outcome
      icon={<Clock />}
      tone="warning"
      title="订单已到期，正在确认付款"
      text="如果你在到期前已经付款，到账确认可能还要一两分钟，请不要重复付款。"
    >
      {claimed ? (
        <div className="w-full">
          <Waiting text="正在确认到账，请不要关闭页面" />
        </div>
      ) : (
        <Button fullWidth size="lg" onPress={onPaid} isPending={busy}>
          我已付款
        </Button>
      )}
    </Outcome>
  )
}

function Paid({ order }: { order: CashierOrder }) {
  const [left, setLeft] = useState(3)
  const [url, setURL] = useState('')
  useEffect(() => {
    if (!order.has_return) return
    let alive = true
    api
      .get<{ url: string }>(`/web-api/cashier/${order.trade_no}/return`)
      .then((r) => alive && setURL(r.url))
      .catch(() => undefined)
    return () => {
      alive = false
    }
  }, [order.has_return, order.trade_no])
  useEffect(() => {
    if (!url) return
    if (left <= 0) {
      window.location.href = url
      return
    }
    const t = setTimeout(() => setLeft((n) => n - 1), 1000)
    return () => clearTimeout(t)
  }, [url, left])

  return (
    <Outcome
      icon={<CircleCheckFill />}
      tone="success"
      title="支付成功"
      text={
        <>
          已收到 <span className="tb-num font-medium text-foreground">{order.pay_amount}</span> 元。
          {!url && <>可以关闭本页面了。</>}
        </>
      }
    >
      {order.paid_at > 0 && (
        <p className="-mt-3 mb-2 text-xs text-muted">
          到账时间 <span className="tb-num">{time(order.paid_at)}</span>
        </p>
      )}
      {url && (
        <>
          <Button fullWidth size="lg" onPress={() => window.location.assign(url)}>
            返回商户
          </Button>
          <p className="text-xs text-muted" aria-live="polite">
            {left > 0 ? `${left} 秒后自动返回` : '正在返回…'}
          </p>
        </>
      )}
    </Outcome>
  )
}
