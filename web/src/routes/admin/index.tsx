import { useState, type ComponentType, type ReactNode, type SVGProps } from 'react'
import { Link, createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { linkVariants, Tooltip } from '@yreidev/ui'
import {
  Bell,
  ChevronRight,
  CircleCheck,
  Clock,
  CreditCard,
  FaceRobot,
  Receipt,
  TriangleExclamation,
  Wallet,
} from '@gravity-ui/icons'
import { LedgerRow } from '../../components/ledger-status'
import { casesCountQuery, channelsQuery, overviewQuery } from '../../lib/api'
import { yuan } from '../../lib/format'
import type { Day, Overview } from '../../lib/types'
import { Kpi, Notice, PageHeader, PageSkeleton, Panel, Segmented, cx, linkClass, type Tone } from '../../lib/ui'

export const Route = createFileRoute('/admin/')({
  component: OverviewPage,
})

type Icon = ComponentType<SVGProps<SVGSVGElement>>

interface Todo {
  key: string
  tone: Tone
  icon: Icon
  title: ReactNode
  detail: ReactNode
  to: string
  search?: Record<string, string>
  action: string
}

function OverviewPage() {
  const [days, setDays] = useState<'7' | '30'>('7')
  const q = useQuery({ ...overviewQuery(Number(days)), refetchInterval: 15_000 })
  const cases = useQuery({ ...casesCountQuery, refetchInterval: 30_000 })
  const channels = useQuery(channelsQuery).data?.items ?? []
  const o = q.data
  const manual = cases.data?.counts?.manual ?? 0

  if (q.isError) return <Notice status="danger">{q.error.message}</Notice>
  if (!o) return <PageSkeleton />

  const total = { count: o.days.reduce((s, d) => s + d.count, 0), amount: o.days.reduce((s, d) => s + d.amount, 0) }
  const todo: Todo[] = []
  if (o.open_anomalies > 0)
    todo.push({
      key: 'anomalies',
      tone: 'warning',
      icon: TriangleExclamation,
      title: `${o.open_anomalies} 笔到账待确认归属`,
      detail: '金额对不上、找不到订单或重复付款的流水，要人工关联订单或标为已处理。',
      to: '/admin/ledger',
      action: '去处理',
    })
  if (o.dead_notifications > 0)
    todo.push({
      key: 'notify',
      tone: 'danger',
      icon: Bell,
      title: `${o.dead_notifications} 笔订单没能通知到商户`,
      detail: '自动通知 8 次都失败了，商户那边可能还没给付款人发货。修好商户接口后手动重发。',
      to: '/admin/notifications',
      search: { status: 'dead' },
      action: '查看',
    })
  if (manual > 0)
    todo.push({
      key: 'cases',
      tone: 'warning',
      icon: FaceRobot,
      title: `${manual} 个付款问题等人工处理`,
      detail: 'AI 核查后无法自动确认，需要你看一下证据再决定。',
      to: '/admin/assistance',
      action: '去处理',
    })
  for (const c of channels.filter((c) => c.enabled && c.problem))
    todo.push({
      key: `ch${c.id}`,
      tone: 'danger',
      icon: CreditCard,
      title: `渠道「${c.name}」现在不能收款`,
      detail: c.problem,
      to: '/admin/channels',
      action: '去修复',
    })

  const top = o.days.reduce<Day | null>((m, d) => (!m || d.amount > m.amount ? d : m), null)
  return (
    <div className="flex flex-col gap-4">
      <PageHeader description="今天收了多少，有什么要处理的。数据每 15 秒自动刷新。" />
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <Kpi
          label="今日收款"
          icon={<Wallet />}
          tone="accent"
          value={yuan(o.today.amount)}
          unit="元"
          foot={`${o.today.count} 笔已支付订单`}
        />
        <Kpi
          label={`近 ${days} 天收款`}
          icon={<Receipt />}
          value={yuan(total.amount)}
          unit="元"
          foot={`${total.count} 笔 · 日均 ${yuan(Math.round(total.amount / o.days.length))} 元`}
        />
        <Kpi
          label="等待付款"
          icon={<Clock />}
          value={o.pending_orders}
          unit="笔"
          foot={
            <Link className={linkClass} to="/admin/orders" search={{ state: 'pending' }}>
              查看待支付订单
            </Link>
          }
        />
        <Kpi
          label="通知重试中"
          icon={<Bell />}
          value={o.retrying_notifications}
          unit="笔"
          foot="商户暂时没确认，会按计划自动重发"
        />
      </div>

      <Panel title="待处理" description={todo.length ? '按处理优先级排列，处理完会自动消失。' : undefined}>
        {todo.length === 0 ? (
          <div className="flex items-center gap-3 rounded-xl bg-success-soft px-4 py-3 text-sm text-success-soft-foreground">
            <CircleCheck className="size-5 shrink-0" />
            一切正常：没有待确认的到账、失败的通知或等人工的申诉。
          </div>
        ) : (
          <ul className="-mx-2 flex flex-col">
            {todo.map((t) => (
              <li key={t.key}>
                <Link
                  to={t.to}
                  search={t.search}
                  className="group flex items-center gap-3 rounded-xl px-2 py-2.5 outline-none hover:bg-default focus-visible:ring-2 focus-visible:ring-focus"
                >
                  <span
                    className={cx(
                      'grid size-9 shrink-0 place-items-center rounded-xl [&>svg]:size-4',
                      t.tone === 'danger' ? 'bg-danger-soft text-danger' : 'bg-warning-soft text-warning',
                    )}
                  >
                    <t.icon />
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="block text-sm font-medium">{t.title}</span>
                    <span className="block text-xs text-muted">{t.detail}</span>
                  </span>
                  <span
                    className={cx(linkVariants().base(), 'hidden shrink-0 items-center gap-0.5 text-sm sm:inline-flex')}
                  >
                    {t.action}
                    <ChevronRight className="size-3.5" />
                  </span>
                  <ChevronRight className="size-4 shrink-0 text-muted sm:hidden" />
                </Link>
              </li>
            ))}
          </ul>
        )}
      </Panel>

      <div className="grid items-start gap-4 lg:grid-cols-[minmax(0,1.7fr)_minmax(0,1fr)]">
        <Panel
          title="收款趋势"
          description="按订单金额统计，北京时间分日"
          extra={
            <Segmented
              ariaLabel="统计天数"
              value={days}
              onChange={setDays}
              options={[
                { value: '7', label: '7 天' },
                { value: '30', label: '30 天' },
              ]}
            />
          }
        >
          <TrendChart days={o.days} />
          {top && top.amount > 0 && (
            <p className="mt-3 text-xs text-muted">
              最高的一天是 <span className="tb-num">{top.date}</span>，<span className="tb-num">{top.count}</span> 笔，
              <span className="tb-num">{yuan(top.amount)}</span> 元。
            </p>
          )}
        </Panel>
        <Panel title="查账" description="支付宝经营码按账单确认到账">
          {o.ledgers.length > 0 ? (
            <div className="flex flex-col divide-y divide-separator">
              {o.ledgers.map((l) => (
                <LedgerRow key={l.source} status={l} />
              ))}
            </div>
          ) : (
            <p className="text-sm text-muted">
              没有启用的经营码渠道。上游易支付渠道的查单和回调情况在{' '}
              <Link className={linkClass} to="/admin/channels">
                支付渠道
              </Link>{' '}
              页查看。
            </p>
          )}
        </Panel>
      </div>

      <Panel title="按商户" description={`近 ${days} 天各商户的已支付金额`}>
        <MerchantShare merchants={o.merchants} />
      </Panel>
    </div>
  )
}

// niceMax 把纵轴最大值取整到 1、2、5 开头的数，刻度好读。
function niceMax(v: number) {
  if (v <= 0) return 100
  const p = 10 ** Math.floor(Math.log10(v))
  const n = v / p
  return (n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10) * p
}

// TrendChart 是每日收款的柱状图：纵轴三条刻度线，悬停看当天笔数和金额。
function TrendChart({ days }: { days: Day[] }) {
  const max = niceMax(Math.max(...days.map((d) => d.amount)))
  const dense = days.length > 10
  const ticks = [max, max / 2, 0]
  return (
    <div className="flex gap-2" role="img" aria-label={`每日收款金额，共 ${days.length} 天`}>
      <div className="flex h-48 flex-col justify-between pb-5 text-right">
        {ticks.map((t) => (
          <span
            key={t}
            className="tb-num -translate-y-1/2 text-[11px] leading-none text-muted first:translate-y-0 last:translate-y-0"
          >
            {t >= 1_000_000 ? `${yuan(t / 10_000).replace(/\.00$/, '')}万` : yuan(t).replace(/\.00$/, '')}
          </span>
        ))}
      </div>
      <div className="relative min-w-0 flex-1">
        <div className="pointer-events-none absolute inset-x-0 top-0 bottom-5 flex flex-col justify-between">
          {ticks.map((t) => (
            <span
              key={t}
              className={cx('block h-px', t === 0 ? 'bg-separator' : 'border-t border-dashed border-separator')}
            />
          ))}
        </div>
        <div className={cx('relative flex h-48 items-end', dense ? 'gap-1' : 'gap-3')}>
          {days.map((d) => (
            <Tooltip key={d.date} delay={0} closeDelay={0}>
              <Tooltip.Trigger
                className="group flex h-full min-w-0 flex-1 flex-col items-center justify-end gap-1.5"
                tabIndex={-1}
                role="presentation"
              >
                <div className="flex w-full flex-1 items-end justify-center pb-px">
                  <div
                    className={cx(
                      'w-full max-w-9 rounded-t-md transition-colors',
                      d.amount ? 'bg-accent/85 group-hover:bg-accent' : 'bg-default',
                    )}
                    style={{ height: d.amount ? `${Math.max(2, (d.amount / max) * 100)}%` : 2 }}
                  />
                </div>
                <span className="tb-num h-3.5 text-[11px] leading-none text-muted">
                  {dense ? (Number(d.date.slice(8)) % 5 === 0 ? d.date.slice(8) : '') : d.date.slice(5)}
                </span>
              </Tooltip.Trigger>
              <Tooltip.Content>
                <span className="tb-num">
                  {d.date}：{d.count} 笔，{yuan(d.amount)} 元
                </span>
              </Tooltip.Content>
            </Tooltip>
          ))}
        </div>
      </div>
    </div>
  )
}

// MerchantShare 用横条显示各商户收款占比，金额从高到低。
function MerchantShare({ merchants }: { merchants: Overview['merchants'] }) {
  if (merchants.length === 0) return <p className="text-sm text-muted">这段时间还没有收款。</p>
  const sorted = [...merchants].sort((a, b) => b.amount - a.amount)
  const sum = sorted.reduce((s, m) => s + m.amount, 0) || 1
  return (
    <ul className="flex flex-col gap-3.5">
      {sorted.map((m) => {
        const pct = (m.amount / sum) * 100
        return (
          <li key={m.merchant_id} className="flex flex-col gap-1.5">
            <div className="flex items-baseline justify-between gap-3 text-sm">
              <span className="min-w-0 truncate">
                {m.name || '已删除的商户'} <span className="text-xs text-muted">#{m.merchant_id}</span>
              </span>
              <span className="tb-num shrink-0">
                {yuan(m.amount)} 元<span className="ml-2 text-xs text-muted">{m.count} 笔</span>
              </span>
            </div>
            <div className="h-1.5 overflow-hidden rounded-full bg-default" aria-hidden>
              <div className="h-full rounded-full bg-accent" style={{ width: `${Math.max(pct, 1)}%` }} />
            </div>
          </li>
        )
      })}
    </ul>
  )
}
