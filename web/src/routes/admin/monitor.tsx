import { Fragment, useMemo, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  BarChart,
  Button,
  ChartTooltip,
  ComposedChart,
  ItemCard,
  ItemCardGroup,
  ListView,
  Segment,
  Separator,
  Typography,
  type ChartTooltipPayloadItem,
} from '@yreidev/ui'
import { ArrowsRotateRight, ChevronRight, CircleExclamation, TriangleExclamation } from '@gravity-ui/icons'
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
  RouterLink,
  Stack,
  StatusDot,
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
        description="每 30 秒自动检查渠道、查账、第三方平台、商户通知、流水异常、AI 处理和归档，有问题列在这里并发 Telegram 通知。"
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

function LevelIcon({ level }: { level: Level }) {
  // 图标的颜色直接写在 svg 上：ListView 给条目里的 svg 统一设了灰色
  return level === 'critical' ? (
    <span className="grid size-9 shrink-0 place-items-center rounded-xl bg-danger-soft">
      <TriangleExclamation className="text-danger" />
    </span>
  ) : (
    <span className="grid size-9 shrink-0 place-items-center rounded-xl bg-warning-soft">
      <CircleExclamation className="text-warning" />
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
        <Notice status="success">一切正常，没有需要处理的问题。</Notice>
      ) : (
        <ListView aria-label="进行中的告警" variant="secondary" items={alerts} className="-mx-2">
          {(a) => (
            <ListView.Item id={a.id} href={a.link || undefined} textValue={a.title}>
              <ListView.ItemContent className="items-start">
                <LevelIcon level={a.level} />
                <div className="flex min-w-0 flex-col gap-0.5">
                  <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                    <ListView.Title>{a.title}</ListView.Title>
                    <Pill tone={a.level === 'critical' ? 'danger' : 'warning'}>{levelText[a.level]}</Pill>
                  </div>
                  {a.detail && (
                    <ListView.Description className="break-words whitespace-normal">{a.detail}</ListView.Description>
                  )}
                  <ListView.Description className="whitespace-normal">
                    <span className="tb-num">{time(a.started_at, false)}</span> 开始，已持续{' '}
                    {duration(now - a.started_at)}
                    {notifying && (a.notified_at ? ' · 已发 Telegram' : ' · Telegram 还没发出')}
                  </ListView.Description>
                </div>
              </ListView.ItemContent>
              {a.link && (
                <ListView.ItemAction className="flex items-center gap-1 text-sm text-accent">
                  <span className="max-sm:hidden">去处理</span>
                  <ChevronRight className="size-3.5" />
                </ListView.ItemAction>
              )}
            </ListView.Item>
          )}
        </ListView>
      )}
    </Panel>
  )
}

function ChannelsPanel({ channels, now }: { channels: ChannelHealth[]; now: number }) {
  return (
    <Panel title="渠道运行情况" description="能否收款，经营码看查账，第三方易支付渠道看预下单、查单和回调">
      {channels.length === 0 ? (
        <Typography.Paragraph size="sm" color="muted">
          还没有支付渠道，到 <RouterLink to="/admin/channels">支付渠道</RouterLink> 页新建。
        </Typography.Paragraph>
      ) : (
        <ItemCardGroup variant="transparent">
          {channels.map((c, i) => (
            <Fragment key={c.id}>
              {i > 0 && <Separator />}
              <ItemCard variant="transparent" className="items-start px-0">
                <ItemCard.Content className="gap-1">
                  <ItemCard.Title>{c.name}</ItemCard.Title>
                  <ItemCard.Description className="mt-0">
                    {channelTypeText[c.type] ?? c.type} · 渠道 {c.id}
                  </ItemCard.Description>
                  {c.enabled && c.problem && (
                    <Typography.Paragraph size="xs" className="text-danger">
                      {c.problem}
                    </Typography.Paragraph>
                  )}
                  {channelHealth(c, now).map((l, i) => (
                    <Typography.Paragraph
                      key={i}
                      size="xs"
                      className={
                        l.tone === 'danger' ? 'text-danger' : l.tone === 'warning' ? 'text-warning' : 'text-muted'
                      }
                    >
                      {l.text}
                    </Typography.Paragraph>
                  ))}
                </ItemCard.Content>
                <ItemCard.Action>
                  <StatusDot tone={!c.enabled ? 'default' : c.problem ? 'danger' : 'success'}>
                    {!c.enabled ? '已停用' : c.problem ? '不能收款' : '能收款'}
                  </StatusDot>
                </ItemCard.Action>
              </ItemCard>
            </Fragment>
          ))}
        </ItemCardGroup>
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
        <RouterLink to="/admin/settings" search={{ tab: 'notify' }} className="gap-0.5 text-sm">
          {tg.configured ? '设置' : '去设置'}
          <ChevronRight className="size-3.5" />
        </RouterLink>
      }
    >
      {!tg.configured ? (
        <Typography.Paragraph size="sm" color="muted">
          还没设置机器人，告警只显示在这个页面。到“设置 → 管理员通知”填上 Telegram
          机器人并绑定自己的账号，有问题时手机上马上能收到；收款到账、付款申诉也可以一起推送。
        </Typography.Paragraph>
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

// Legend 是趋势图的图例，照组件库示例：色点加名称。
function Legend({ items }: { items: { label: string; color: string }[] }) {
  return (
    <div className="flex flex-wrap items-center gap-3" aria-hidden>
      {items.map((it) => (
        <span key={it.label} className="flex items-center gap-1.5 text-xs text-muted">
          <span className="size-2.5 rounded-full" style={{ backgroundColor: it.color }} />
          {it.label}
        </span>
      ))}
    </div>
  )
}

// TrendTooltip 是趋势图的悬停提示：笔数、次数带单位，成功率显示百分比，没有新建订单的格子显示“—”。
function TrendTooltip({
  active,
  payload,
  label,
  unit,
}: {
  active?: boolean
  payload?: ChartTooltipPayloadItem[]
  label?: string | number
  unit: string
}) {
  if (!active || !payload?.length) return null
  return (
    <ChartTooltip indicator="line">
      <ChartTooltip.Header>{label}</ChartTooltip.Header>
      {payload.map((it) => (
        <ChartTooltip.Item key={String(it.dataKey)}>
          <ChartTooltip.Indicator color={it.stroke ?? it.color ?? it.fill} />
          <ChartTooltip.Label>{it.name}</ChartTooltip.Label>
          <ChartTooltip.Value className="tb-num">
            {it.dataKey === 'rate'
              ? it.value === null || it.value === undefined
                ? '—'
                : `${String(it.value)}%`
              : `${String(it.value ?? 0)} ${unit}`}
          </ChartTooltip.Value>
        </ChartTooltip.Item>
      ))}
    </ChartTooltip>
  )
}

const orderLegend = [
  { label: '新建', color: 'color-mix(in oklab, var(--muted) 35%, transparent)' },
  { label: '已支付', color: 'var(--success)' },
  { label: '支付成功率', color: 'var(--accent)' },
]

const notifyLegend = [
  { label: '送达', color: 'var(--success)' },
  { label: '失败', color: 'var(--danger)' },
]

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
  // 页面每 5 秒刷新相对时间，图表只在数据变了时重画；数据没变时 rows 不变，进场动画不重播
  const { buckets, rows } = useMemo(() => {
    const buckets = q.data?.buckets ?? []
    const hourly = q.data?.range === '24h'
    const rows = buckets.map((b) => ({
      label: cstLabel(b.at, hourly),
      created: b.created,
      paid: b.paid,
      // 没有新建订单的格子不算成功率，画成断开
      rate: b.created ? Math.round((b.paid / b.created) * 100) : null,
      ok: b.notify_ok,
      fail: b.notify_fail,
    }))
    return { buckets, rows }
  }, [q.data])
  // 笔数是整数：最大值很小时少画几条刻度，免得 1 笔也画到 4
  const countTicks = (max: number) => Math.min(5, Math.max(1, max) + 1)
  const orderTicks = countTicks(Math.max(0, ...rows.map((r) => Math.max(r.created, r.paid))))
  const notifyTicks = countTicks(Math.max(0, ...rows.map((r) => r.ok + r.fail)))
  const created = buckets.reduce((s, b) => s + b.created, 0)
  const paid = buckets.reduce((s, b) => s + b.paid, 0)
  const ok = buckets.reduce((s, b) => s + b.notify_ok, 0)
  const fail = buckets.reduce((s, b) => s + b.notify_fail, 0)

  return (
    <Panel
      title="趋势"
      description="不含测试订单，北京时间分格"
      extra={
        <Segment aria-label="时间范围" size="sm" selectedKey={range} onSelectionChange={(k) => setRange(k as Range)}>
          {rangeOptions.map((o) => (
            <Segment.Item key={o.value} id={o.value}>
              {o.label}
            </Segment.Item>
          ))}
        </Segment>
      }
    >
      {q.isError && <Notice status="danger">{q.error.message}</Notice>}
      <div className="grid gap-6 lg:grid-cols-2">
        <section className="flex min-w-0 flex-col gap-1">
          <Typography.Heading level={3} className="text-sm">
            订单
          </Typography.Heading>
          <Typography.Paragraph size="xs" color="muted">
            新建 <span className="tb-num">{created}</span> 笔，已支付 <span className="tb-num">{paid}</span> 笔
            {created > 0 && (
              <>
                ，成功率 <span className="tb-num">{Math.round((paid / created) * 100)}%</span>
              </>
            )}
            。按下单时间归格，最近一格里可能还有等待付款的订单。
          </Typography.Paragraph>
          <Legend items={orderLegend} />
          <ComposedChart
            data={rows}
            height={256}
            role="img"
            aria-label={`订单趋势：新建 ${created} 笔，已支付 ${paid} 笔`}
          >
            <ComposedChart.Grid vertical={false} strokeDasharray="3 3" />
            <ComposedChart.XAxis dataKey="label" minTickGap={8} />
            <ComposedChart.YAxis yAxisId="count" allowDecimals={false} tickCount={orderTicks} width={32} />
            <ComposedChart.YAxis
              yAxisId="rate"
              orientation="right"
              domain={[0, 100]}
              ticks={[0, 50, 100]}
              tickFormatter={(v: number) => `${v}%`}
              width={40}
            />
            <ComposedChart.Bar
              yAxisId="count"
              dataKey="created"
              name="新建"
              fill="var(--muted)"
              fillOpacity={0.35}
              maxBarSize={16}
              radius={[3, 3, 0, 0]}
            />
            <ComposedChart.Bar
              yAxisId="count"
              dataKey="paid"
              name="已支付"
              fill="var(--success)"
              maxBarSize={16}
              radius={[3, 3, 0, 0]}
            />
            <ComposedChart.Line
              yAxisId="rate"
              dataKey="rate"
              name="支付成功率"
              type="linear"
              stroke="var(--accent)"
              strokeWidth={2}
              dot={{ r: 2.5 }}
            />
            <ComposedChart.Tooltip cursor={{ fill: 'var(--default)' }} content={<TrendTooltip unit="笔" />} />
          </ComposedChart>
        </section>
        <section className="flex min-w-0 flex-col gap-1">
          <Typography.Heading level={3} className="text-sm">
            通知商户
          </Typography.Heading>
          <Typography.Paragraph size="xs" color="muted">
            送达 <span className="tb-num">{ok}</span> 次，失败 <span className="tb-num">{fail}</span>{' '}
            次。按发送时间归格，含自动重发和手动重发。
          </Typography.Paragraph>
          <Legend items={notifyLegend} />
          <BarChart data={rows} height={256} role="img" aria-label={`通知商户趋势：送达 ${ok} 次，失败 ${fail} 次`}>
            <BarChart.Grid vertical={false} strokeDasharray="3 3" />
            <BarChart.XAxis dataKey="label" minTickGap={8} />
            <BarChart.YAxis allowDecimals={false} tickCount={notifyTicks} width={32} />
            <BarChart.Bar dataKey="ok" name="送达" stackId="notify" fill="var(--success)" maxBarSize={16} />
            <BarChart.Bar
              dataKey="fail"
              name="失败"
              stackId="notify"
              fill="var(--danger)"
              maxBarSize={16}
              radius={[3, 3, 0, 0]}
            />
            <BarChart.Tooltip cursor={{ fill: 'var(--default)' }} content={<TrendTooltip unit="次" />} />
          </BarChart>
        </section>
      </div>
    </Panel>
  )
}

function ResolvedPanel({ alerts }: { alerts: Alert[] }) {
  return (
    <div className="flex flex-col gap-3">
      <div className="mt-2 flex flex-col gap-1">
        <Typography.Heading level={2} className="text-base">
          最近恢复的告警
        </Typography.Heading>
        <Typography.Paragraph size="sm" color="muted">
          最近 30 条。恢复的告警保留 180 天。
        </Typography.Paragraph>
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
