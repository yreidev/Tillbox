import { useMemo, useState } from 'react'
import { Link, createFileRoute } from '@tanstack/react-router'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Button, linkVariants } from '@yreidev/ui'
import { ArrowsRotateRight, ChevronRight, CircleCheck, CircleExclamation, TriangleExclamation } from '@gravity-ui/icons'
import { Chart, tooltipStyle, type ChartOption, type Palette } from '../../components/chart'
import type { MonitorState, TelegramView } from '../../components/telegram-settings'
import { api } from '../../lib/api'
import { duration, relative, time } from '../../lib/format'
import { channelHealth } from '../../lib/health'
import { useNow } from '../../lib/now'
import { channelTypeText, type ChannelView } from '../../lib/types'
import {
  DataTable,
  Desc,
  Notice,
  PageHeader,
  PageSkeleton,
  Panel,
  Pill,
  Segmented,
  Stack,
  StatusDot,
  cx,
  toastError,
  toastOK,
} from '../../lib/ui'

export const Route = createFileRoute('/admin/monitor')({ component: MonitorPage })

// 以下结构和 internal/admin/monitor.go、internal/monitor 一致。
type Level = 'critical' | 'warning'

interface Alert {
  id: number
  key: string
  level: Level
  title: string
  detail: string
  /** 后台处理这个问题的页面，可能带查询串 */
  link: string
  count: number
  started_at: number
  last_seen_at: number
  resolved_at: number
  /** 上次成功发出通知的时刻，0 表示还没通知到 */
  notified_at: number
}

type ChannelHealth = Pick<ChannelView, 'id' | 'key' | 'name' | 'type' | 'enabled' | 'problem' | 'ledger' | 'upstream'>

interface Monitor {
  alerts: Alert[]
  resolved: Alert[]
  state: MonitorState
  channels: ChannelHealth[]
  telegram: TelegramView
}

interface Bucket {
  at: number
  created: number
  paid: number
  notify_ok: number
  notify_fail: number
}

type Range = '24h' | '7d' | '30d'

const levelText: Record<Level, string> = { critical: '严重', warning: '提醒' }

// 后台每 30 秒检查一次（internal/monitor 的 Every）；超过 2 分钟没跑说明监控任务出了问题。
const staleAfter = 2 * 60_000

const monitorKey = ['monitor']

function MonitorPage() {
  const qc = useQueryClient()
  const q = useQuery({
    queryKey: monitorKey,
    queryFn: () => api.get<Monitor>('/web-api/admin/monitor'),
    refetchInterval: 15_000,
  })
  const check = useMutation({
    mutationFn: () => api.post<Monitor>('/web-api/admin/monitor/check'),
    onSuccess: (d) => {
      qc.setQueryData(monitorKey, d)
      void qc.invalidateQueries({ queryKey: ['overview'] })
      toastOK(d.alerts.length ? `检查完成，有 ${d.alerts.length} 条告警` : '检查完成，一切正常')
    },
    onError: toastError,
  })
  const now = useNow()
  const d = q.data
  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        description="每 30 秒自动检查渠道、查账、上游、商户通知、流水异常、AI 处理和归档，有问题列在这里并发 Telegram 通知。"
        actions={
          <Button size="sm" variant="secondary" isPending={check.isPending} onPress={() => check.mutate()}>
            <ArrowsRotateRight />
            立即检查
          </Button>
        }
      />
      {q.isError && <Notice status="danger">{q.error.message}</Notice>}
      {q.isPending && <PageSkeleton rows={3} />}
      {d && (
        <>
          {d.state.last_error && (
            <Notice status="danger" title="上次检查出错">
              {d.state.last_error}
            </Notice>
          )}
          {d.state.last_run_at > 0 && now - d.state.last_run_at > staleAfter && (
            <Notice status="warning" title="监控任务没有按时运行">
              上次检查是 {relative(d.state.last_run_at, now)}，下面的告警可能不是最新的。请看服务日志。
            </Notice>
          )}
          {d.telegram.configured && d.state.send_error_at > d.state.sent_at && (
            <Notice status="warning" title="Telegram 消息没发出去">
              {relative(d.state.send_error_at, now)}：{d.state.send_error}。下一轮检查会重发。
            </Notice>
          )}
          <AlertsPanel alerts={d.alerts} state={d.state} telegram={d.telegram} now={now} />
          <div className="grid items-start gap-4 lg:grid-cols-2">
            <ChannelsPanel channels={d.channels} now={now} />
            <NotifyPanel tg={d.telegram} state={d.state} now={now} />
          </div>
          <TrendsPanel />
          <ResolvedPanel alerts={d.resolved} />
        </>
      )}
    </div>
  )
}

// splitLink 把 “/admin/notifications?status=dead” 拆成路由和查询参数。
function splitLink(link: string) {
  const [to, qs] = link.split('?', 2)
  return { to, search: qs ? Object.fromEntries(new URLSearchParams(qs)) : undefined }
}

function LevelIcon({ level }: { level: Level }) {
  return (
    <span
      className={cx(
        'grid size-9 shrink-0 place-items-center rounded-xl [&>svg]:size-4',
        level === 'critical' ? 'bg-danger-soft text-danger' : 'bg-warning-soft text-warning',
      )}
    >
      {level === 'critical' ? <TriangleExclamation /> : <CircleExclamation />}
    </span>
  )
}

function AlertsPanel({
  alerts,
  state,
  telegram,
  now,
}: {
  alerts: Alert[]
  state: MonitorState
  telegram: TelegramView
  now: number
}) {
  const notifying = telegram.configured && telegram.chats.length > 0
  return (
    <Panel
      title="进行中的告警"
      description={
        state.last_run_at ? `上次检查 ${relative(state.last_run_at, now)}，严重的排在前面` : '监控任务还没运行过'
      }
    >
      {alerts.length === 0 ? (
        <div className="flex items-center gap-3 rounded-xl bg-success-soft px-4 py-3 text-sm text-success-soft-foreground">
          <CircleCheck className="size-5 shrink-0" />
          一切正常，没有需要处理的问题。
        </div>
      ) : (
        <ul className="flex flex-col divide-y divide-separator">
          {alerts.map((a) => {
            const link = a.link ? splitLink(a.link) : null
            return (
              <li key={a.id} className="flex items-start gap-3 py-3 first:pt-0 last:pb-0">
                <LevelIcon level={a.level} />
                <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                  <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                    <span className="text-sm font-medium">{a.title}</span>
                    <Pill tone={a.level === 'critical' ? 'danger' : 'warning'}>{levelText[a.level]}</Pill>
                  </div>
                  {a.detail && <p className="text-xs break-words text-muted">{a.detail}</p>}
                  <p className="text-xs text-muted">
                    <span className="tb-num">{time(a.started_at, false)}</span> 开始，已持续{' '}
                    {duration(now - a.started_at)}
                    {notifying && (a.notified_at ? ' · 已发 Telegram' : ' · Telegram 还没发出')}
                  </p>
                </div>
                {link && (
                  <Link
                    to={link.to}
                    search={link.search}
                    className={cx(linkVariants().base(), 'mt-2 inline-flex shrink-0 items-center gap-0.5 text-sm')}
                  >
                    去处理
                    <ChevronRight className="size-3.5" />
                  </Link>
                )}
              </li>
            )
          })}
        </ul>
      )}
    </Panel>
  )
}

function ChannelsPanel({ channels, now }: { channels: ChannelHealth[]; now: number }) {
  return (
    <Panel title="渠道运行情况" description="能否收款，经营码看查账，上游渠道看预下单、查单和回调">
      {channels.length === 0 ? (
        <p className="text-sm text-muted">
          还没有支付渠道，到{' '}
          <Link className={linkVariants().base()} to="/admin/channels">
            支付渠道
          </Link>{' '}
          页新建。
        </p>
      ) : (
        <ul className="flex flex-col divide-y divide-separator">
          {channels.map((c) => (
            <li key={c.id} className="flex flex-col gap-1 py-3 first:pt-0 last:pb-0">
              <div className="flex items-start justify-between gap-3">
                <Stack
                  className="min-w-0"
                  main={<span className="font-medium">{c.name}</span>}
                  sub={`${channelTypeText[c.type] ?? c.type} · 渠道 ${c.id}`}
                />
                <StatusDot tone={!c.enabled ? 'default' : c.problem ? 'danger' : 'success'}>
                  {!c.enabled ? '已停用' : c.problem ? '不能收款' : '能收款'}
                </StatusDot>
              </div>
              {c.enabled && c.problem && <p className="text-xs text-danger">{c.problem}</p>}
              {channelHealth(c, now).map((l, i) => (
                <p
                  key={i}
                  className={cx(
                    'text-xs',
                    l.tone === 'danger' ? 'text-danger' : l.tone === 'warning' ? 'text-warning' : 'text-muted',
                  )}
                >
                  {l.text}
                </p>
              ))}
            </li>
          ))}
        </ul>
      )}
    </Panel>
  )
}

// NotifyPanel 只显示 Telegram 通知的状态，设置在“设置 → 管理员通知”。
function NotifyPanel({ tg, state, now }: { tg: TelegramView; state: MonitorState; now: number }) {
  return (
    <Panel
      title="Telegram 通知"
      description="告警开始、笔数变多、恢复，以及一直没恢复时，发给绑定的人"
      extra={
        <Link
          to="/admin/settings"
          search={{ tab: 'notify' }}
          className={cx(linkVariants().base(), 'inline-flex items-center gap-0.5 text-sm')}
        >
          {tg.configured ? '设置' : '去设置'}
          <ChevronRight className="size-3.5" />
        </Link>
      }
    >
      {!tg.configured ? (
        <p className="text-sm text-muted">
          还没设置机器人，告警只显示在这个页面。到“设置 → 管理员通知”填上 Telegram
          机器人并绑定自己的账号，有问题时手机上马上能收到；收款到账、付款申诉也可以一起推送。
        </p>
      ) : (
        <Desc
          data={[
            { key: '机器人', value: `@${tg.bot_username}` },
            {
              key: '接收人',
              value: tg.chats.length ? (
                `${tg.chats.length} 个`
              ) : (
                <span className="text-warning">还没有，告警发不出去</span>
              ),
            },
            {
              key: '一直没恢复时',
              value: tg.remind_hours ? `每 ${tg.remind_hours} 小时再提醒一次` : '不再提醒',
            },
            {
              key: '上次发出消息',
              value: <span className="tb-num">{state.sent_at ? relative(state.sent_at, now) : '还没发过'}</span>,
            },
          ]}
        />
      )}
    </Panel>
  )
}

const rangeOptions: { value: Range; label: string }[] = [
  { value: '24h', label: '24 小时' },
  { value: '7d', label: '7 天' },
  { value: '30d', label: '30 天' },
]

// 趋势按北京时间分格（和后端一致），不看浏览器所在时区。
function cstLabel(ms: number, hourly: boolean) {
  const t = new Date(ms + 8 * 3600_000)
  const pad = (n: number) => String(n).padStart(2, '0')
  return hourly ? `${pad(t.getUTCHours())}:00` : `${pad(t.getUTCMonth() + 1)}-${pad(t.getUTCDate())}`
}

// axes 是两张趋势图共用的坐标轴样式。
function axes(p: Palette, labels: string[]): Pick<ChartOption, 'grid' | 'xAxis' | 'legend'> {
  return {
    grid: { left: 4, right: 4, top: 36, bottom: 4 },
    legend: {
      top: 0,
      left: 0,
      icon: 'roundRect',
      itemWidth: 10,
      itemHeight: 10,
      textStyle: { color: p.muted, fontFamily: p.font, fontSize: 12 },
    },
    xAxis: {
      type: 'category',
      data: labels,
      axisTick: { show: false },
      axisLine: { lineStyle: { color: p.separator } },
      axisLabel: { color: p.muted, fontFamily: p.font, fontSize: 11 },
    },
  }
}

function countAxis(p: Palette) {
  return {
    type: 'value' as const,
    minInterval: 1,
    splitNumber: 3,
    axisLabel: { color: p.muted, fontFamily: p.font, fontSize: 11 },
    splitLine: { lineStyle: { color: p.separator, type: 'dashed' as const } },
  }
}

const times = (v: unknown) => `${String(v)} 笔`

// orderChart 是订单趋势：新建和已支付的笔数（左轴），支付成功率（右轴）。
function orderChart(buckets: Bucket[], labels: string[]) {
  return (p: Palette): ChartOption => ({
    ...axes(p, labels),
    tooltip: tooltipStyle(p),
    yAxis: [
      countAxis(p),
      {
        type: 'value',
        min: 0,
        max: 100,
        interval: 50,
        axisLabel: { color: p.muted, fontFamily: p.font, fontSize: 11, formatter: '{value}%' },
        splitLine: { show: false },
      },
    ],
    series: [
      {
        name: '新建',
        type: 'bar',
        barMaxWidth: 16,
        itemStyle: { color: p.muted, opacity: 0.35, borderRadius: [3, 3, 0, 0] },
        tooltip: { valueFormatter: times },
        data: buckets.map((b) => b.created),
      },
      {
        name: '已支付',
        type: 'bar',
        barMaxWidth: 16,
        itemStyle: { color: p.success, borderRadius: [3, 3, 0, 0] },
        tooltip: { valueFormatter: times },
        data: buckets.map((b) => b.paid),
      },
      {
        name: '支付成功率',
        type: 'line',
        yAxisIndex: 1,
        symbol: 'circle',
        symbolSize: 5,
        itemStyle: { color: p.accent },
        lineStyle: { width: 2 },
        tooltip: { valueFormatter: (v) => (v === null || v === undefined || v === '-' ? '—' : `${String(v)}%`) },
        // 没有新建订单的格子不算成功率，画成断开
        data: buckets.map((b) => (b.created ? Math.round((b.paid / b.created) * 100) : '-')),
      },
    ],
  })
}

// notifyChart 是通知商户的趋势：送达和失败的次数叠在一起。
function notifyChart(buckets: Bucket[], labels: string[]) {
  return (p: Palette): ChartOption => ({
    ...axes(p, labels),
    tooltip: tooltipStyle(p),
    yAxis: countAxis(p),
    series: [
      {
        name: '送达',
        type: 'bar',
        stack: 'notify',
        barMaxWidth: 16,
        itemStyle: { color: p.success },
        tooltip: { valueFormatter: (v) => `${String(v)} 次` },
        data: buckets.map((b) => b.notify_ok),
      },
      {
        name: '失败',
        type: 'bar',
        stack: 'notify',
        barMaxWidth: 16,
        itemStyle: { color: p.danger, borderRadius: [3, 3, 0, 0] },
        tooltip: { valueFormatter: (v) => `${String(v)} 次` },
        data: buckets.map((b) => b.notify_fail),
      },
    ],
  })
}

function TrendsPanel() {
  const [range, setRange] = useState<Range>('24h')
  const q = useQuery({
    queryKey: ['monitor', 'trends', range],
    // 结果带上自己的范围：切换范围时先显示旧数据，横轴要按旧数据的范围标
    queryFn: async () => ({
      range,
      ...(await api.get<{ buckets: Bucket[] }>(`/web-api/admin/monitor/trends?range=${range}`)),
    }),
    refetchInterval: 60_000,
    placeholderData: keepPreviousData,
  })
  // 页面每 5 秒刷新相对时间，图表只在数据变了时重画
  const { buckets, orders, notify } = useMemo(() => {
    const buckets = q.data?.buckets ?? []
    const labels = buckets.map((b) => cstLabel(b.at, q.data?.range === '24h'))
    return { buckets, orders: orderChart(buckets, labels), notify: notifyChart(buckets, labels) }
  }, [q.data])
  const created = buckets.reduce((s, b) => s + b.created, 0)
  const paid = buckets.reduce((s, b) => s + b.paid, 0)
  const ok = buckets.reduce((s, b) => s + b.notify_ok, 0)
  const fail = buckets.reduce((s, b) => s + b.notify_fail, 0)

  return (
    <Panel
      title="趋势"
      description="不含测试订单，北京时间分格"
      extra={<Segmented ariaLabel="时间范围" value={range} onChange={setRange} options={rangeOptions} />}
    >
      {q.isError && <Notice status="danger">{q.error.message}</Notice>}
      <div className="grid gap-6 lg:grid-cols-2">
        <section className="flex min-w-0 flex-col gap-1">
          <h3 className="text-sm font-semibold">订单</h3>
          <p className="text-xs text-muted">
            新建 <span className="tb-num">{created}</span> 笔，已支付 <span className="tb-num">{paid}</span> 笔
            {created > 0 && (
              <>
                ，成功率 <span className="tb-num">{Math.round((paid / created) * 100)}%</span>
              </>
            )}
            。按下单时间归格，最近一格里可能还有等待付款的订单。
          </p>
          <Chart build={orders} label={`订单趋势：新建 ${created} 笔，已支付 ${paid} 笔`} className="h-64" />
        </section>
        <section className="flex min-w-0 flex-col gap-1">
          <h3 className="text-sm font-semibold">通知商户</h3>
          <p className="text-xs text-muted">
            送达 <span className="tb-num">{ok}</span> 次，失败 <span className="tb-num">{fail}</span>{' '}
            次。按发送时间归格，含自动重发和手动重发。
          </p>
          <Chart build={notify} label={`通知商户趋势：送达 ${ok} 次，失败 ${fail} 次`} className="h-64" />
        </section>
      </div>
    </Panel>
  )
}

function ResolvedPanel({ alerts }: { alerts: Alert[] }) {
  return (
    <div className="flex flex-col gap-3">
      <div className="mt-2 flex flex-col gap-1">
        <h2 className="text-base font-semibold">最近恢复的告警</h2>
        <p className="text-sm text-muted">最近 30 条。恢复的告警保留 180 天。</p>
      </div>
      <DataTable
        label="最近恢复的告警"
        rows={alerts}
        rowKey={(a) => a.id}
        minWidth={720}
        empty="还没有恢复的告警"
        card={(a) => (
          <div className="flex items-start gap-3">
            <Pill tone={a.level === 'critical' ? 'danger' : 'warning'}>{levelText[a.level]}</Pill>
            <Stack
              className="min-w-0"
              main={a.title}
              sub={
                <>
                  <span className="tb-num">{time(a.resolved_at, false)}</span> 恢复，持续{' '}
                  {duration(a.resolved_at - a.started_at)}
                </>
              }
            />
          </div>
        )}
        columns={[
          {
            title: '级别',
            nowrap: true,
            render: (a) => <Pill tone={a.level === 'critical' ? 'danger' : 'warning'}>{levelText[a.level]}</Pill>,
          },
          {
            title: '告警',
            render: (a) => (
              <Stack
                className="max-w-md"
                main={a.title}
                sub={a.detail && <span className="break-words">{a.detail}</span>}
              />
            ),
          },
          { title: '开始', nowrap: true, render: (a) => <span className="tb-num">{time(a.started_at, false)}</span> },
          { title: '恢复', nowrap: true, render: (a) => <span className="tb-num">{time(a.resolved_at, false)}</span> },
          { title: '持续', nowrap: true, align: 'right', render: (a) => duration(a.resolved_at - a.started_at) },
        ]}
      />
    </div>
  )
}
