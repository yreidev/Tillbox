import { useState, type ComponentType, type ReactNode, type SVGProps } from 'react'
import { Link, createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { BarChart, KPI, Label, ListView, Meter, Segment } from '@yreidev/ui'
import {
  Bell,
  ChevronRight,
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
import { Notice, PageHeader, PageSkeleton, Panel, linkClass, type Tone } from '../../lib/ui'

export const Route = createFileRoute('/admin/')({
  component: OverviewPage,
})

type Icon = ComponentType<SVGProps<SVGSVGElement>>

interface Todo {
  key: string
  tone: Tone
  icon: Icon
  title: string
  detail: ReactNode
  href: string
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
      href: '/admin/ledger',
      action: '去处理',
    })
  if (o.dead_notifications > 0)
    todo.push({
      key: 'notify',
      tone: 'danger',
      icon: Bell,
      title: `${o.dead_notifications} 笔订单没能通知到商户`,
      detail: '自动通知 8 次都失败了，商户那边可能还没给付款人发货。修好商户接口后手动重发。',
      href: '/admin/notifications?status=dead',
      action: '查看',
    })
  if (manual > 0)
    todo.push({
      key: 'cases',
      tone: 'warning',
      icon: FaceRobot,
      title: `${manual} 个付款问题等人工处理`,
      detail: 'AI 核查后无法自动确认，需要你看一下证据再决定。',
      href: '/admin/assistance',
      action: '去处理',
    })
  for (const c of channels.filter((c) => c.enabled && c.problem))
    todo.push({
      key: `ch${c.id}`,
      tone: 'danger',
      icon: CreditCard,
      title: `渠道「${c.name}」现在不能收款`,
      detail: c.problem,
      href: '/admin/channels',
      action: '去修复',
    })

  const top = o.days.reduce<Day | null>((m, d) => (!m || d.amount > m.amount ? d : m), null)
  return (
    <div className="flex flex-col gap-4">
      <PageHeader description="今天收了多少，有什么要处理的。数据每 15 秒自动刷新。" />
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <Kpi label="今日收款" icon={<Wallet />} status="success" cents={o.today.amount}>
          {o.today.count} 笔已支付订单
        </Kpi>
        <Kpi label={`近 ${days} 天收款`} icon={<Receipt />} cents={total.amount}>
          {total.count} 笔 · 日均 {yuan(Math.round(total.amount / o.days.length))} 元
        </Kpi>
        <Kpi label="等待付款" icon={<Clock />} count={o.pending_orders}>
          <Link className={linkClass} to="/admin/orders" search={{ state: 'pending' }}>
            查看待支付订单
          </Link>
        </Kpi>
        <Kpi label="通知重试中" icon={<Bell />} count={o.retrying_notifications}>
          商户暂时没确认，会按计划自动重发
        </Kpi>
      </div>

      <Panel title="待处理" description={todo.length ? '按处理优先级排列，处理完会自动消失。' : undefined}>
        {todo.length === 0 ? (
          <Notice status="success">一切正常：没有待确认的到账、失败的通知或等人工的申诉。</Notice>
        ) : (
          <ListView aria-label="待处理" variant="secondary" items={todo} className="-mx-2">
            {(t) => (
              <ListView.Item id={t.key} href={t.href} textValue={`${t.title}，${t.action}`}>
                <ListView.ItemContent>
                  <t.icon className={t.tone === 'danger' ? 'text-danger' : 'text-warning'} />
                  <div className="flex min-w-0 flex-col">
                    <ListView.Title>{t.title}</ListView.Title>
                    <ListView.Description className="whitespace-normal">{t.detail}</ListView.Description>
                  </div>
                </ListView.ItemContent>
                <ListView.ItemAction className="flex items-center gap-1 text-sm text-accent">
                  <span className="max-sm:hidden">{t.action}</span>
                  <ChevronRight className="size-3.5" />
                </ListView.ItemAction>
              </ListView.Item>
            )}
          </ListView>
        )}
      </Panel>

      <div className="grid items-start gap-4 lg:grid-cols-[minmax(0,1.7fr)_minmax(0,1fr)]">
        <Panel
          title="收款趋势"
          description="按订单金额统计，北京时间分日"
          extra={
            <Segment
              aria-label="统计天数"
              size="sm"
              selectedKey={days}
              onSelectionChange={(k) => setDays(k === '30' ? '30' : '7')}
            >
              <Segment.Item id="7">7 天</Segment.Item>
              <Segment.Item id="30">30 天</Segment.Item>
            </Segment>
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

// Kpi 是概览页顶部的一张指标卡：金额（cents，分）或笔数（count），底部一行补充说明。
function Kpi({
  label,
  icon,
  status,
  cents,
  count,
  children,
}: {
  label: ReactNode
  icon: ReactNode
  status?: 'success' | 'warning' | 'danger'
  cents?: number
  count?: number
  children: ReactNode
}) {
  const money = cents !== undefined
  return (
    <KPI className="min-w-0">
      <KPI.Header>
        <KPI.Icon status={status}>{icon}</KPI.Icon>
        <KPI.Title>{label}</KPI.Title>
      </KPI.Header>
      <KPI.Content>
        <KPI.Value
          className="tb-num"
          value={money ? cents / 100 : (count ?? 0)}
          minimumFractionDigits={money ? 2 : 0}
          maximumFractionDigits={money ? 2 : 0}
        >
          {(formatted) => (
            <>
              {formatted}
              <span className="ms-1 text-sm font-normal text-muted">{money ? '元' : '笔'}</span>
            </>
          )}
        </KPI.Value>
      </KPI.Content>
      <KPI.Footer className="text-xs text-muted">{children}</KPI.Footer>
    </KPI>
  )
}

// shortYuan 是纵轴刻度：去掉 .00，百万以上用“万”。
function shortYuan(cents: number) {
  return cents >= 1_000_000 ? `${yuan(cents / 10_000).replace(/\.00$/, '')}万` : yuan(cents).replace(/\.00$/, '')
}

// TrendChart 是每日收款的柱状图，悬停看当天笔数和金额。
function TrendChart({ days }: { days: Day[] }) {
  const data = days.map((d) => ({ date: d.date, label: d.date.slice(5), amount: d.amount, count: d.count }))
  return (
    <div role="img" aria-label={`每日收款金额，共 ${days.length} 天`}>
      <BarChart data={data} height={200}>
        <BarChart.Grid vertical={false} strokeDasharray="3 3" />
        <BarChart.XAxis dataKey="label" tickMargin={8} minTickGap={8} />
        <BarChart.YAxis width={48} tickFormatter={(v: number) => shortYuan(v)} />
        <BarChart.Bar
          dataKey="amount"
          name="收款"
          fill="var(--accent)"
          maxBarSize={36}
          radius={[6, 6, 0, 0]}
          isAnimationActive={false}
        />
        <BarChart.Tooltip
          cursor={{ fill: 'var(--default)' }}
          content={
            <BarChart.TooltipContent
              labelFormatter={(label) => {
                const d = data.find((x) => x.label === label)
                return d ? `${d.date} · ${d.count} 笔` : label
              }}
              valueFormatter={(v) => `${yuan(Number(v))} 元`}
            />
          }
        />
      </BarChart>
    </div>
  )
}

// MerchantShare 用横条显示各商户收款占比，金额从高到低。
function MerchantShare({ merchants }: { merchants: Overview['merchants'] }) {
  if (merchants.length === 0) return <p className="text-sm text-muted">这段时间还没有收款。</p>
  const sorted = [...merchants].sort((a, b) => b.amount - a.amount)
  const sum = sorted.reduce((s, m) => s + m.amount, 0) || 1
  return (
    <div className="flex flex-col gap-3.5">
      {sorted.map((m) => (
        <Meter
          key={m.merchant_id}
          size="sm"
          value={(m.amount / sum) * 100}
          valueLabel={`${yuan(m.amount)} 元 · ${m.count} 笔`}
          className="w-full"
        >
          <Label className="min-w-0 truncate font-normal">
            {m.name || '已删除的商户'} <span className="text-xs text-muted">#{m.merchant_id}</span>
          </Label>
          <Meter.Output className="tb-num" />
          <Meter.Track>
            <Meter.Fill />
          </Meter.Track>
        </Meter>
      ))}
    </div>
  )
}
