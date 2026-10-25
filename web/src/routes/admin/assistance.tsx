import { useId, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Button, Card, Drawer, Form, Surface, Typography } from '@yreidev/ui'
import { ArrowUpRightFromSquare, CircleCheck, FaceRobot, Gear, PlugConnection } from '@gravity-ui/icons'
import { api } from '../../lib/api'
import { relative, time, yuan } from '../../lib/format'
import {
  CheckboxInput,
  DataTable,
  Desc,
  Empty,
  FilterTabs,
  Mono,
  ConflictNotice,
  Notice,
  NumberInput,
  PageHeader,
  PageSkeleton,
  Panel,
  Pill,
  Section,
  Stack,
  StatusDot,
  SwitchInput,
  TextAreaInput,
  TextInput,
  Events,
  cx,
  enter,
  toastError,
  toastOK,
  useRevision,
  type Tone,
} from '../../lib/ui'
import { OrderDetail } from '../../components/order-detail'

export const Route = createFileRoute('/admin/assistance')({ component: AssistancePage })

interface Config {
  enabled: boolean
  base_url: string
  model: string
  auto_recheck: boolean
  auto_notify: boolean
  auto_link: boolean
  interval_seconds: number
  max_attempts: number
}
interface Case {
  id: number
  order_id: number
  transaction_id: number
  source: string
  kind: string
  payment_ref: string
  claim_amount: number
  paid_at: number
  message: string
  contact: string
  status: string
  summary: string
  public_reply: string
  attempts: number
  created_at: number
  updated_at: number
}
interface Action {
  id: number
  action: string
  reason: string
  result: string
  created_at: number
}
interface Overview {
  config: Config
  has_api_key: boolean
  /** AI 设置的版本，保存时放进 If-Match */
  revision: number
  state: { last_run_at: number; last_ok_at: number; last_error: string; processed: number }
  items: Case[]
  total: number
  counts: Record<string, number>
}

type Status = 'manual' | 'pending' | 'waiting' | 'resolved' | 'all'

const statuses: Record<string, string> = {
  pending: '待核查',
  waiting: '自动处理中',
  manual: '待人工处理',
  resolved: '已处理',
}
const statusTone: Record<string, Tone> = {
  pending: 'accent',
  waiting: 'accent',
  manual: 'warning',
  resolved: 'success',
}
const sources: Record<string, string> = { payer: '付款人申诉', ledger: '流水异常', notification: '通知失败' }
const kinds: Record<string, string> = {
  not_received: '已付款未到账',
  wrong_amount: '金额付错',
  late: '迟付',
  duplicate: '重复付款',
  other: '其他问题',
  notification_failed: '通知失败',
  underpaid: '少付',
  overpaid: '多付',
  unmatched: '未匹配',
  ambiguous: '归属不明',
  conflict: '流水冲突',
  unlinked: '人工撤销',
  invalid: '流水解析不了',
}
const actionNames: Record<string, string> = {
  admin_associate: '人工关联申诉订单',
  find_order: '核查订单归属',
  admin_resolve: '人工处理',
  admin_retry: '重新核查',
  link_payment: '补认到账',
  link_late: '补认过期订单',
  check_merchant: '核对商户入账',
  recheck: '请求查账',
  retry_notification: '补发通知',
  verify: '状态确认',
  manual: '转人工',
  model_error: '模型请求失败',
  blocked: '安全校验未通过',
  limit: '达到尝试上限',
  paused: '自动处理已停用',
}
// 处理过程里的圆点颜色：失败、拦截的标红，转人工的标黄，补认和人工处理标绿。
const actionTone: Record<string, Tone> = {
  model_error: 'danger',
  blocked: 'danger',
  limit: 'warning',
  manual: 'warning',
  paused: 'warning',
  link_payment: 'success',
  link_late: 'success',
  admin_resolve: 'success',
}

const pageSize = 20

function AssistancePage() {
  const qc = useQueryClient()
  const [status, setStatus] = useState<Status>('manual')
  const [page, setPage] = useState(1)
  const [selected, setSelected] = useState<number | null>(null)
  const [orderID, setOrderID] = useState<number | null>(null)
  const [config, setConfig] = useState(false)
  const q = useQuery({
    queryKey: ['assistance', status, page],
    queryFn: () => api.get<Overview>(`/web-api/admin/assistance?status=${status === 'all' ? '' : status}&page=${page}`),
    placeholderData: keepPreviousData,
    refetchInterval: 10_000,
  })
  const refresh = async () => {
    await qc.invalidateQueries({ queryKey: ['assistance'] })
    await qc.invalidateQueries({ queryKey: ['assistance-case'] })
  }
  const run = useMutation({
    mutationFn: (action: 'run' | 'test') => api.post(`/web-api/admin/assistance/${action}`),
    onSuccess: async (_d, action) => {
      await refresh()
      toastOK(action === 'test' ? '模型连接和动作格式校验通过' : '巡检请求已提交')
    },
    onError: toastError,
  })
  const d = q.data
  if (q.isError && !d) return <Notice status="danger">{q.error.message}</Notice>
  if (!d) return <PageSkeleton rows={2} />
  const counts = d.counts ?? {}

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        description="付款人自助提交的申诉、对不上账的流水和送不到的通知都会进到这里。AI 先按授权自动核查，证据不足的转给你处理。"
        actions={
          <Button size="sm" variant="secondary" onPress={() => window.open('/help', '_blank', 'noopener,noreferrer')}>
            <ArrowUpRightFromSquare />
            付款人自助页
          </Button>
        }
      />
      <AIStatus
        d={d}
        running={run.isPending && run.variables === 'run'}
        onRun={() => run.mutate('run')}
        onConfig={() => setConfig(true)}
      />
      <FilterTabs<Status>
        ariaLabel="处理状态"
        value={status}
        onChange={(v) => {
          setStatus(v)
          setPage(1)
        }}
        options={[
          { value: 'manual', label: '待人工处理', count: counts.manual, tone: 'warning' },
          { value: 'pending', label: '待核查', count: counts.pending, tone: 'accent' },
          { value: 'waiting', label: '自动处理中', count: counts.waiting, tone: 'accent' },
          { value: 'resolved', label: '已处理' },
          { value: 'all', label: '全部' },
        ]}
      >
        {q.isError && <Notice status="danger">{q.error.message}</Notice>}
        {status === 'manual' && !d.items?.length ? (
          <Panel>
            <Empty
              icon={<CircleCheck className="text-success" />}
              title="没有等你处理的申诉"
              description="AI 处理不了的会出现在这里。"
            />
          </Panel>
        ) : (
          <DataTable
            label="申诉与异常"
            rows={d.items ?? []}
            rowKey={(c) => c.id}
            loading={q.isFetching && !q.data}
            minWidth={960}
            onRowClick={(c) => setSelected(c.id)}
            empty="没有记录"
            pagination={{ page, pageSize, total: d.total, onChange: setPage }}
            card={(c) => (
              <div className="flex flex-col gap-1.5">
                <div className="flex items-center justify-between gap-3">
                  <span className="truncate font-medium">{kinds[c.kind] || c.kind}</span>
                  <StatusDot tone={statusTone[c.status] ?? 'default'} pulse={c.status === 'waiting'}>
                    {statuses[c.status] || c.status}
                  </StatusDot>
                </div>
                <span className="text-xs text-muted">
                  #{c.id} · {sources[c.source] || c.source} · <span className="tb-num">{relative(c.updated_at)}</span>
                </span>
                <Typography.Paragraph size="sm" color="muted" className="line-clamp-2">
                  {c.summary || '等待核查'}
                </Typography.Paragraph>
              </div>
            )}
            columns={[
              {
                title: '问题',
                render: (c) => (
                  <Stack
                    main={<span className="font-medium">{kinds[c.kind] || c.kind}</span>}
                    sub={
                      <>
                        #{c.id} · {sources[c.source] || c.source}
                      </>
                    }
                  />
                ),
              },
              {
                title: '订单',
                nowrap: true,
                render: (c) =>
                  c.order_id > 0 ? (
                    <span className="text-sm">已关联</span>
                  ) : c.source === 'payer' ? (
                    <Pill tone="warning">待找回订单</Pill>
                  ) : (
                    <span className="text-sm text-muted">无关联订单</span>
                  ),
              },
              {
                title: '状态',
                nowrap: true,
                render: (c) => (
                  <Stack
                    main={
                      <StatusDot tone={statusTone[c.status] ?? 'default'} pulse={c.status === 'waiting'}>
                        {statuses[c.status] || c.status}
                      </StatusDot>
                    }
                    sub={c.attempts ? `自动尝试 ${c.attempts} 次` : undefined}
                  />
                ),
              },
              {
                title: '最近结果',
                render: (c) => (
                  <span className="line-clamp-2 max-w-80 text-sm" title={c.summary}>
                    {c.summary || <span className="text-muted">等待核查</span>}
                  </span>
                ),
              },
              { title: '更新时间', nowrap: true, render: (c) => <span className="tb-num">{time(c.updated_at)}</span> },
            ]}
          />
        )}
      </FilterTabs>
      <CaseSheet id={selected} onClose={() => setSelected(null)} onOrder={setOrderID} refresh={refresh} />
      <OrderDetail id={orderID} onClose={() => setOrderID(null)} />
      <ConfigSheet
        open={config}
        onClose={() => setConfig(false)}
        config={d.config}
        hasKey={d.has_api_key}
        revision={d.revision}
        saved={refresh}
        testing={run.isPending && run.variables === 'test'}
        onTest={() => run.mutate('test')}
      />
    </div>
  )
}

// AIStatus 是 AI 自动处理的一行状态：开没开、用哪个模型、上次巡检情况。
function AIStatus({
  d,
  running,
  onRun,
  onConfig,
}: {
  d: Overview
  running: boolean
  onRun: () => void
  onConfig: () => void
}) {
  const c = d.config
  const s = d.state
  const allowed = [c.auto_recheck && '重新查账', c.auto_notify && '补发通知', c.auto_link && '补认到账'].filter(Boolean)
  return (
    <Panel>
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center">
        <div className="flex min-w-0 flex-1 items-start gap-3">
          <span
            className={cx(
              'grid size-10 shrink-0 place-items-center rounded-xl [&>svg]:size-5',
              c.enabled ? 'bg-accent-soft text-accent-soft-foreground' : 'bg-default text-muted',
            )}
          >
            <FaceRobot />
          </span>
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
              <span className="font-semibold">AI 自动处理</span>
              <StatusDot tone={!c.enabled ? 'default' : s.last_error ? 'warning' : 'success'}>
                {!c.enabled ? '未启用' : s.last_error ? '上次巡检出错' : '运行中'}
              </StatusDot>
            </div>
            <Typography.Paragraph size="sm" color="muted" className="mt-0.5">
              {c.enabled
                ? `${c.model || '未填模型'} · 每 ${c.interval_seconds} 秒巡检 · 允许${allowed.length ? allowed.join('、') : '只读核查'}`
                : '未启用时付款人仍能自助查单、提交申诉，由你在这里处理。'}
            </Typography.Paragraph>
            {c.enabled && (
              <Typography.Paragraph size="xs" color="muted" className="tb-num mt-0.5">
                {s.last_ok_at
                  ? `${relative(s.last_ok_at)}巡检成功，处理了 ${s.processed || 0} 项（每轮最多 5 项）`
                  : '还没巡检过'}
              </Typography.Paragraph>
            )}
            {s.last_error && (
              <Typography.Paragraph size="xs" className="mt-1 text-warning">
                {s.last_error}
              </Typography.Paragraph>
            )}
          </div>
        </div>
        <div className="flex shrink-0 gap-2">
          <Button size="sm" variant="secondary" onPress={onConfig}>
            <Gear />
            设置
          </Button>
          <Button size="sm" variant="secondary" isPending={running} isDisabled={!c.enabled} onPress={onRun}>
            立即巡检
          </Button>
        </div>
      </div>
    </Panel>
  )
}

// CaseSheet 是一条处理记录的侧栏：付款人提交的内容、人工处理、找回订单和处理过程。
function CaseSheet({
  id,
  onClose,
  onOrder,
  refresh,
}: {
  id: number | null
  onClose: () => void
  onOrder: (id: number) => void
  refresh: () => Promise<void>
}) {
  const [note, setNote] = useState('')
  const [tradeNo, setTradeNo] = useState('')
  const detail = useQuery({
    queryKey: ['assistance-case', id],
    enabled: id !== null,
    queryFn: () => api.get<{ case: Case; actions: Action[] }>(`/web-api/admin/assistance/cases/${id}`),
    refetchInterval: 10_000,
  })
  const update = useMutation({
    mutationFn: (action: 'retry' | 'resolve' | 'associate') =>
      api.post(`/web-api/admin/assistance/cases/${id}`, {
        action,
        note,
        ...(action === 'associate' ? { trade_no: tradeNo.trim() } : {}),
      }),
    onSuccess: async () => {
      await refresh()
      setNote('')
      setTradeNo('')
      toastOK('处理记录已更新')
    },
    onError: toastError,
  })
  const close = () => {
    setNote('')
    setTradeNo('')
    onClose()
  }
  const c = detail.data?.case.id === id ? detail.data?.case : undefined
  const actions = detail.data?.actions ?? []
  const noted = note.trim().length >= 2
  const busy = (a: string) => update.isPending && update.variables === a
  return (
    <Drawer.Backdrop isOpen={id !== null} onOpenChange={(v) => !v && close()}>
      <Drawer.Content placement="right">
        <Drawer.Dialog className="w-[min(600px,100vw)] max-w-[100vw] p-0 sm:w-[min(600px,100vw)]">
          <Drawer.CloseTrigger />
          <Drawer.Header className="gap-2 border-b border-separator px-5 pt-5 pb-4 sm:px-6">
            <Typography.Paragraph size="sm" color="muted">
              处理记录 #{id}
              {c && ` · ${sources[c.source] || c.source}`}
            </Typography.Paragraph>
            <Drawer.Heading className="pr-10 text-lg font-semibold">
              {c ? kinds[c.kind] || c.kind : '加载中…'}
            </Drawer.Heading>
            {c && (
              <div className="flex flex-wrap items-center gap-3">
                <StatusDot tone={statusTone[c.status] ?? 'default'} pulse={c.status === 'waiting'}>
                  {statuses[c.status] || c.status}
                </StatusDot>
                <span className="tb-num text-xs text-muted">
                  {time(c.created_at)} 提交{c.attempts ? ` · 自动尝试 ${c.attempts} 次` : ''}
                </span>
              </div>
            )}
          </Drawer.Header>
          <Drawer.Body className="flex flex-col gap-7 px-5 py-5 sm:px-6">
            {detail.isError && <Notice status="danger">{detail.error.message}</Notice>}
            {c && (
              <>
                {c.summary && (
                  <Section title="AI 核查结论">
                    <Typography.Paragraph size="sm">{c.summary}</Typography.Paragraph>
                  </Section>
                )}
                {(c.payment_ref || c.message || c.contact) && (
                  <Section title="付款人提交的信息">
                    <Desc
                      data={[
                        c.payment_ref && { key: '交易号', value: <Mono>{c.payment_ref}</Mono> },
                        c.payment_ref && {
                          key: '申报实付',
                          value: <span className="tb-num">{yuan(c.claim_amount)} 元</span>,
                        },
                        c.payment_ref && { key: '付款时间', value: <span className="tb-num">{time(c.paid_at)}</span> },
                        c.message && {
                          key: '付款说明',
                          value: <span className="[overflow-wrap:anywhere] whitespace-pre-wrap">{c.message}</span>,
                        },
                        c.contact && { key: '联系方式', value: c.contact },
                      ]}
                    />
                  </Section>
                )}
                <Section
                  title="付款人现在看到的回复"
                  extra={
                    c.order_id > 0 && (
                      <Button size="sm" variant="ghost" onPress={() => onOrder(c.order_id)}>
                        查看关联订单
                      </Button>
                    )
                  }
                >
                  <Surface<'blockquote'>
                    variant="secondary"
                    className="rounded-xl border-l-2 border-accent px-3 py-2.5 text-sm [overflow-wrap:anywhere]"
                    render={(props) => <blockquote {...props} />}
                  >
                    {c.public_reply || '—'}
                  </Surface>
                </Section>
                {c.status !== 'resolved' && (
                  <Section title="人工处理">
                    <TextAreaInput
                      label="处理说明"
                      description={
                        c.source === 'ledger'
                          ? '标为已处理会同时把对应的流水异常标成已处理。'
                          : '标为已处理后，付款人在自助页会看到这段回复。至少 2 个字。'
                      }
                      placeholder="写清核对了什么、结论是什么"
                      value={note}
                      onChange={setNote}
                      maxLength={1000}
                      rows={3}
                    />
                    {c.source === 'payer' && c.order_id === 0 && (
                      <Card
                        variant="transparent"
                        className={cx('rounded-2xl border border-solid border-separator', enter)}
                      >
                        <div>
                          <Typography.Heading level={4} className="text-sm font-medium">
                            找回平台订单
                          </Typography.Heading>
                          <Typography.Paragraph size="xs" color="muted" className="mt-0.5">
                            核对支付宝交易号、金额、时间和原业务站后，填写确认的订单号。只关联申诉，不确认到账；核对依据写在上面的处理说明里。
                          </Typography.Paragraph>
                        </div>
                        <div className="flex flex-wrap items-start gap-2">
                          <TextInput
                            ariaLabel="确认关联的平台订单号"
                            className="w-full sm:w-60"
                            placeholder="19 位平台订单号"
                            inputMode="numeric"
                            maxLength={19}
                            mono
                            value={tradeNo}
                            onChange={setTradeNo}
                          />
                          <Button
                            variant="secondary"
                            isPending={busy('associate')}
                            isDisabled={!/^\d{19}$/.test(tradeNo.trim()) || !noted}
                            onPress={() => update.mutate('associate')}
                          >
                            关联申诉订单
                          </Button>
                        </div>
                      </Card>
                    )}
                    <Typography.Paragraph size="xs" color="muted">
                      标为已处理不会改到账状态；要人工补认时，到「流水与异常」核实并关联。
                    </Typography.Paragraph>
                  </Section>
                )}
                {actions.length > 0 && (
                  <Section title="处理过程">
                    <Events
                      items={actions.map((a) => ({
                        key: a.id,
                        tone: actionTone[a.action] ?? 'default',
                        title: actionNames[a.action] || a.action,
                        meta: time(a.created_at),
                        children: (
                          <>
                            {a.reason && <Typography.Paragraph size="sm">{a.reason}</Typography.Paragraph>}
                            {a.result && (
                              <Typography.Paragraph size="xs" color="muted" className="mt-0.5">
                                {a.result}
                              </Typography.Paragraph>
                            )}
                          </>
                        ),
                      }))}
                    />
                  </Section>
                )}
              </>
            )}
          </Drawer.Body>
          {c && c.status !== 'resolved' && (
            <Drawer.Footer className="flex-wrap justify-start gap-2 border-t border-separator px-5 py-4 sm:px-6">
              <Button isPending={busy('resolve')} isDisabled={!noted} onPress={() => update.mutate('resolve')}>
                回复并标为已处理
              </Button>
              <Button
                variant="secondary"
                isPending={busy('retry')}
                isDisabled={!noted}
                onPress={() => update.mutate('retry')}
              >
                重新自动核查
              </Button>
              {!noted && <span className="ms-auto text-xs text-muted">先写处理说明</span>}
            </Drawer.Footer>
          )}
        </Drawer.Dialog>
      </Drawer.Content>
    </Drawer.Backdrop>
  )
}

type ConfigValues = Config & { api_key: string; clear_key: boolean }

function ConfigSheet({
  open,
  onClose,
  config,
  hasKey,
  revision,
  saved,
  testing,
  onTest,
}: {
  open: boolean
  onClose: () => void
  config: Config
  hasKey: boolean
  revision: number
  saved: () => Promise<void>
  testing: boolean
  onTest: () => void
}) {
  return (
    <Drawer.Backdrop isOpen={open} onOpenChange={(v) => !v && onClose()}>
      <Drawer.Content placement="right">
        <Drawer.Dialog className="w-[min(560px,100vw)] max-w-[100vw] p-0 sm:w-[min(560px,100vw)]">
          <Drawer.CloseTrigger />
          <Drawer.Header className="gap-0.5 border-b border-separator px-5 pt-5 pb-4 sm:px-6">
            <Typography.Paragraph size="sm" color="muted">
              设置
            </Typography.Paragraph>
            <Drawer.Heading className="pr-10 text-lg font-semibold">AI 自动处理</Drawer.Heading>
          </Drawer.Header>
          {open && (
            <ConfigForm
              config={config}
              hasKey={hasKey}
              revision={revision}
              saved={saved}
              testing={testing}
              onTest={onTest}
            />
          )}
        </Drawer.Dialog>
      </Drawer.Content>
    </Drawer.Backdrop>
  )
}

function ConfigForm({
  config,
  hasKey,
  revision,
  saved,
  testing,
  onTest,
}: {
  config: Config
  hasKey: boolean
  revision: number
  saved: () => Promise<void>
  testing: boolean
  onTest: () => void
}) {
  const formId = useId()
  const [v, setV] = useState<ConfigValues>(() => ({ ...config, api_key: '', clear_key: false }))
  const set = <K extends keyof ConfigValues>(k: K, x: ConfigValues[K]) => setV((s) => ({ ...s, [k]: x }))
  const dirty =
    v.api_key !== '' || v.clear_key || (Object.keys(config) as (keyof Config)[]).some((k) => v[k] !== config[k])
  const rev = useRevision(revision, !dirty)
  const save = useMutation({
    mutationFn: (overwrite: boolean) =>
      rev.run(
        async (revision) =>
          (
            await api.put<{ revision: number }>(
              '/web-api/admin/assistance/config',
              {
                enabled: v.enabled,
                base_url: v.base_url,
                model: v.model,
                auto_recheck: v.auto_recheck,
                auto_notify: v.auto_notify,
                auto_link: v.auto_link,
                interval_seconds: Number(v.interval_seconds),
                max_attempts: Number(v.max_attempts),
                api_key: v.api_key,
                clear_key: v.clear_key,
              },
              { revision },
            )
          ).revision,
        overwrite,
      ),
    onSuccess: async (ok) => {
      if (!ok) return
      setV((s) => ({ ...s, api_key: '', clear_key: false }))
      await saved()
      toastOK('自动处理配置已保存')
    },
    onError: toastError,
  })
  // 载入最新：把表单换成服务器上的设置，再刷新列表让 config 和 revision 跟上。
  const reload = async () => {
    const l = await api.get<Overview>('/web-api/admin/assistance?page=1')
    setV({ ...l.config, api_key: '', clear_key: false })
    await saved()
  }
  return (
    <>
      <Drawer.Body className="px-5 py-5 sm:px-6">
        <Form
          id={formId}
          className="flex flex-col gap-7"
          onSubmit={(e) => {
            e.preventDefault()
            save.mutate(false)
          }}
        >
          <SwitchInput
            label="启用 AI 自动处理"
            description="按下面允许的操作，自动核查并处理异常订单和付款申诉。"
            value={v.enabled}
            onChange={(x) => set('enabled', x)}
          />
          <Section title="模型">
            <TextInput
              label="接口地址（Base URL）"
              placeholder="https://模型服务地址/v1"
              description="兼容 Chat Completions 的接口地址。"
              mono
              value={v.base_url}
              onChange={(x) => set('base_url', x)}
            />
            <TextInput
              label="模型名称"
              placeholder="模型服务提供的模型名称"
              mono
              value={v.model}
              onChange={(x) => set('model', x)}
            />
            <TextInput
              label="API Key"
              type="password"
              autoComplete="new-password"
              placeholder={hasKey ? '已安全保存，留空表示不改' : '模型接口的密钥'}
              value={v.api_key}
              onChange={(x) => set('api_key', x)}
            />
            {hasKey && (
              <CheckboxInput label="清除已保存的 API Key" value={v.clear_key} onChange={(x) => set('clear_key', x)} />
            )}
          </Section>
          <Section title="允许自动执行的操作">
            <CheckboxInput
              label="自动请求重新查账"
              description="向支付宝账单或第三方平台再查一次这笔付款，仍受查账间隔限制。"
              value={v.auto_recheck}
              onChange={(x) => set('auto_recheck', x)}
            />
            <CheckboxInput
              label="自动补发通知"
              description="已支付订单的通知失败时，自动重发给商户。"
              value={v.auto_notify}
              onChange={(x) => set('auto_notify', x)}
            />
            <CheckboxInput
              label="证据完整且唯一时自动补认到账"
              description="付款人提交的交易号要和查到的真实流水一致，金额、时间和归属都通过校验。重复付款、流水冲突、人工关闭或撤销关联的订单一律转人工。不会退款。"
              value={v.auto_link}
              onChange={(x) => set('auto_link', x)}
            />
          </Section>
          <Section title="节奏">
            <div className="grid gap-4 sm:grid-cols-2">
              <NumberInput
                label="巡检间隔（秒）"
                minValue={30}
                maxValue={3600}
                value={Number.isNaN(v.interval_seconds) ? undefined : v.interval_seconds}
                onChange={(n) => set('interval_seconds', n ?? NaN)}
              />
              <NumberInput
                label="每项最多自动尝试（次）"
                minValue={1}
                maxValue={10}
                value={Number.isNaN(v.max_attempts) ? undefined : v.max_attempts}
                onChange={(n) => set('max_attempts', n ?? NaN)}
              />
            </div>
          </Section>
          <Surface variant="secondary" className="rounded-xl px-3 py-2.5">
            <Typography.Paragraph size="xs" color="muted">
              会向模型发送必要的订单状态、金额、时间和付款说明；不发送支付密钥、联系方式和完整流水原文。请使用可信的模型服务。
            </Typography.Paragraph>
          </Surface>
          {rev.conflict && (
            <ConflictNotice what="AI 自动处理设置" onReload={reload} onOverwrite={() => save.mutateAsync(true)} />
          )}
        </Form>
      </Drawer.Body>
      <Drawer.Footer className="flex-wrap justify-start gap-2 border-t border-separator px-5 py-4 sm:px-6">
        <Button type="submit" form={formId} isPending={save.isPending}>
          保存
        </Button>
        <Button variant="secondary" isPending={testing} onPress={onTest}>
          <PlugConnection />
          测试已保存的连接
        </Button>
      </Drawer.Footer>
    </>
  )
}
