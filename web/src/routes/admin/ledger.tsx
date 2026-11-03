import { Fragment, useState } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { keepPreviousData, useQuery, useQueryClient } from '@tanstack/react-query'
import { Button, ItemCard, ItemCardGroup, Radio, RadioGroup, Separator, Surface, Typography } from '@yreidev/ui'
import { CircleCheck, Link as LinkIcon } from '@gravity-ui/icons'
import { LedgerRow } from '../../components/ledger-status'
import { OrderDetail } from '../../components/order-detail'
import { api, channelsQuery, overviewQuery, query } from '../../lib/api'
import { time, yuan } from '../../lib/format'
import type { AnomalyView, LedgerStatus, OrderView, Page, Transaction } from '../../lib/types'
import { anomalyText, invalidTxn, stateText, txnStateText } from '../../lib/types'
import {
  DataTable,
  Empty,
  FilterTabs,
  Loading,
  Mono,
  NoteModal,
  Notice,
  OrderLink,
  PageHeader,
  Pager,
  Panel,
  Pill,
  SearchInput,
  SelectInput,
  Stack,
  Toolbar,
  toastOK,
  useFieldVariant,
} from '../../lib/ui'

type View = 'open' | 'resolved' | 'transactions'

interface Search {
  view?: 'resolved' | 'transactions'
  kind?: string
  state?: string
  direction?: string
  channel_id?: number
  q?: string
  page?: number
}

export const Route = createFileRoute('/admin/ledger')({
  validateSearch: (s: Record<string, unknown>): Search => {
    const str = (k: string) => (typeof s[k] === 'string' && s[k] ? s[k] : undefined)
    return {
      view: s.view === 'resolved' || s.view === 'transactions' ? s.view : undefined,
      kind: str('kind'),
      state: str('state'),
      direction: str('direction'),
      channel_id: typeof s.channel_id === 'number' ? s.channel_id : undefined,
      q: str('q'),
      page: typeof s.page === 'number' ? s.page : undefined,
    }
  },
  component: LedgerPage,
})

const pageSize = 20

function LedgerPage() {
  const search = Route.useSearch()
  const navigate = useNavigate({ from: Route.fullPath })
  const status = useQuery({
    queryKey: ['ledger-status'],
    queryFn: () => api.get<{ items: LedgerStatus[] }>('/web-api/admin/ledger/status'),
    refetchInterval: 10_000,
  })
  const open = useQuery(overviewQuery(7)).data?.open_anomalies ?? 0
  const view: View = search.view ?? 'open'
  const ledgers = status.data?.items ?? []
  return (
    <div className="flex flex-col gap-4">
      <PageHeader description="到账流水会自动和订单对账；金额对不上、找不到订单或重复付款的，进入异常等你确认。" />
      {ledgers.length > 0 && (
        <Panel title="查账" description="支付宝经营码的账单查询进度">
          <div className="grid gap-x-8 max-lg:divide-y max-lg:divide-separator lg:grid-cols-2 lg:gap-y-5">
            {ledgers.map((l) => (
              <LedgerRow key={l.source} status={l} className="lg:py-0" />
            ))}
          </div>
        </Panel>
      )}
      <FilterTabs<View>
        ariaLabel="流水与异常"
        value={view}
        onChange={(v) => void navigate({ search: { view: v === 'open' ? undefined : v } })}
        options={[
          { value: 'open', label: '待处理异常', count: open, tone: 'warning' },
          { value: 'resolved', label: '已处理异常' },
          { value: 'transactions', label: '全部流水' },
        ]}
      >
        {view === 'transactions' ? (
          <Transactions search={search} />
        ) : (
          <Anomalies search={search} resolved={view === 'resolved'} />
        )}
      </FilterTabs>
    </div>
  )
}

interface Linking {
  txn: Transaction
  /** 异常里指出的订单，打开时默认选中 */
  suggest?: string
}

// LinkModal 把一笔收入流水关联到订单（订单会变成已支付并通知商户）。可选的订单由后端按这笔流水列出，也能按订单号或金额搜。
function LinkModal({ linking, onClose }: { linking: Linking | null; onClose: () => void }) {
  const qc = useQueryClient()
  const [kw, setKw] = useState('')
  const [q, setQ] = useState('')
  const [picked, setPicked] = useState('')
  const txn = linking?.txn
  const list = useQuery({
    queryKey: ['link-candidates', txn?.id, q],
    queryFn: () => api.get<{ items: OrderView[] }>(`/web-api/admin/transactions/${txn!.id}/candidates${query({ q })}`),
    enabled: !!txn,
    // 同一笔流水换搜索词时保留上次的结果，换了流水不能先显示上一笔的候选
    placeholderData: (prev, prevQuery) => (prevQuery?.queryKey[1] === txn?.id ? prev : undefined),
  })
  const items = list.data?.items ?? []
  // 只认列表里看得到的订单：搜了别的、选过的那单不在列表里时算没选
  const shown = (v?: string) => !!v && items.some((o) => o.trade_no === v)
  const value = picked ? (shown(picked) ? picked : '') : shown(linking?.suggest) ? linking!.suggest! : ''
  const close = () => {
    setKw('')
    setQ('')
    setPicked('')
    onClose()
  }
  return (
    <NoteModal
      title="把这笔到账关联到订单"
      visible={!!txn}
      onCancel={close}
      okText="关联并标为已支付"
      description="关联后订单标记为已支付，实收按这笔流水算，并开始通知商户。"
      onOK={async (note) => {
        if (!value) throw new Error('先选一个订单')
        await api.post(`/web-api/admin/transactions/${txn!.id}/link`, { trade_no: value, note })
        toastOK('已关联，订单已标记为已支付')
        close()
        void qc.invalidateQueries()
      }}
    >
      {txn && <TxnBox txn={txn} />}
      <SearchInput
        ariaLabel="搜索订单"
        placeholder="平台订单号、商户订单号或金额"
        value={kw}
        onChange={setKw}
        onSubmit={setQ}
      />
      {list.isError ? (
        <Notice status="danger">{list.error.message}</Notice>
      ) : list.isPending ? (
        <Loading />
      ) : items.length === 0 ? (
        <Typography.Paragraph size="sm" color="muted">
          {q
            ? '没有找到同一渠道、还没支付的订单。'
            : '没有付款时间对得上的未支付订单（同一渠道、到账前创建、结束不超过一天），可以按订单号或金额搜。'}
        </Typography.Paragraph>
      ) : (
        <div className="flex flex-col gap-2">
          <Typography.Paragraph size="xs" color="muted">
            {q ? '搜到的订单' : '付款时间对得上的订单'}：只列同一渠道、还没支付的，最多 20 个
          </Typography.Paragraph>
          <div className="max-h-80 overflow-y-auto">
            <OrderPicker
              items={items}
              value={value}
              onChange={setPicked}
              note={(o) =>
                o.trade_no === linking?.suggest ? '异常里指出的订单' : o.pay_amount === txn?.amount ? '金额相同' : ''
              }
            />
          </div>
        </div>
      )}
    </NoteModal>
  )
}

// OrderPicker 是可关联订单的单选列表，整行都能点（说明文字放在 Radio.Content 里，不用 RadioInput 的说明）。
function OrderPicker({
  items,
  value,
  onChange,
  note,
}: {
  items: OrderView[]
  value: string
  onChange: (tradeNo: string) => void
  note: (o: OrderView) => string
}) {
  const variant = useFieldVariant()
  return (
    <RadioGroup variant={variant} aria-label="选择订单" value={value} onChange={onChange}>
      {items.map((o) => (
        <Radio key={o.trade_no} value={o.trade_no}>
          <Radio.Content className="items-start">
            <Radio.Control className="mt-0.5">
              <Radio.Indicator />
            </Radio.Control>
            <span className="flex min-w-0 flex-col gap-0.5">
              <span>
                {o.merchant_name || '已删除的商户'} · {o.name} · 应付{' '}
                <span className="tb-num">¥{yuan(o.pay_amount)}</span>
                {note(o) && <span className="text-accent"> · {note(o)}</span>}
              </span>
              <span className="text-xs font-normal break-all text-muted">
                <Mono>{o.trade_no}</Mono> · 商户订单号 <Mono>{o.out_trade_no}</Mono> ·{' '}
                <span className="tb-num">{time(o.created_at)}</span> 创建 · {stateText[o.state]}
              </span>
            </span>
          </Radio.Content>
        </Radio>
      ))}
    </RadioGroup>
  )
}

// TxnBox 是弹窗里一笔流水的摘要。
function TxnBox({ txn }: { txn: Transaction }) {
  return (
    <Surface variant="secondary" className="flex items-center justify-between gap-4 rounded-xl px-4 py-3">
      <div className="min-w-0 text-sm">
        <div className="truncate">{txn.other_account || '付款方未知'}</div>
        <div className="tb-num text-xs text-muted">{time(txn.occurred_at)}</div>
        <div className="truncate text-xs text-muted">
          流水 <Mono>{txn.external_id}</Mono>
        </div>
      </div>
      <span className="tb-num shrink-0 text-xl font-semibold">¥{yuan(txn.amount)}</span>
    </Surface>
  )
}

function Anomalies({ search, resolved }: { search: Search; resolved: boolean }) {
  const navigate = useNavigate({ from: Route.fullPath })
  const qc = useQueryClient()
  const [linking, setLinking] = useState<Linking | null>(null)
  const [resolving, setResolving] = useState<AnomalyView | null>(null)
  const [detail, setDetail] = useState<number | null>(null)
  const channels = useQuery(channelsQuery).data?.items ?? []
  const channelName = (key: string) => channels.find((c) => c.key === key)?.name ?? key
  const q = useQuery({
    queryKey: ['anomalies', resolved, search.kind, search.page],
    queryFn: () =>
      api.get<Page<AnomalyView> & { open: number }>(
        `/web-api/admin/anomalies${query({ status: resolved ? 'resolved' : 'open', kind: search.kind, page: search.page, page_size: pageSize })}`,
      ),
    placeholderData: keepPreviousData,
    refetchInterval: 15_000,
  })
  const items = q.data?.items ?? []
  return (
    <>
      <Toolbar
        end={
          q.data && (
            <span className="text-sm text-muted">
              共 <span className="tb-num">{q.data.total}</span> 条
            </span>
          )
        }
      >
        <SelectInput
          ariaLabel="异常种类"
          className="w-full sm:w-44"
          placeholder="全部种类"
          clearable
          value={search.kind}
          onChange={(v) => void navigate({ search: (s) => ({ ...s, kind: v, page: undefined }) })}
          options={Object.entries(anomalyText).map(([value, label]) => ({ value, label }))}
        />
      </Toolbar>
      {q.isError && <Notice status="danger">{q.error.message}</Notice>}
      <ItemCardGroup>
        {q.isPending ? (
          <Loading />
        ) : items.length === 0 ? (
          resolved || search.kind ? (
            <Empty description="没有记录" />
          ) : (
            <Empty
              icon={<CircleCheck className="text-success" />}
              title="没有待处理的异常"
              description="所有到账都已经和订单对上了。"
            />
          )
        ) : (
          items.map((a, i) => {
            const t = a.transaction
            const o = a.order
            const channel = o?.channel || t?.channel
            const canLink = !resolved && !!t && t.direction === 'in' && t.order_id === 0 && !invalidTxn(t)
            return (
              <Fragment key={a.id}>
                {i > 0 && <Separator />}
                <ItemCard className="flex-col items-stretch sm:flex-row sm:items-start">
                  <ItemCard.Content className="gap-1.5">
                    <div className="flex flex-wrap items-center gap-2">
                      <Pill tone={resolved ? 'default' : 'warning'}>{anomalyText[a.kind]}</Pill>
                      <span className="tb-num text-xs text-muted">{time(a.created_at)}</span>
                      {o && (
                        <span className="text-xs text-muted">
                          订单 <OrderLink tradeNo={o.trade_no} onClick={() => setDetail(a.order_id)} />
                        </span>
                      )}
                      {channel && channels.length > 1 && (
                        <span className="text-xs text-muted">{channelName(channel)}</span>
                      )}
                    </div>
                    {o && (
                      <Typography.Paragraph size="xs" color="muted">
                        <span className="font-medium text-foreground">{o.merchant || '已删除的商户'}</span> · {o.name} ·
                        商户订单号 <Mono>{o.out_trade_no}</Mono> · 应付{' '}
                        <span className="tb-num font-medium text-foreground">¥{yuan(o.pay_amount)}</span>
                        {o.pay_amount !== o.amount && <span className="tb-num">（原价 ¥{yuan(o.amount)}）</span>}
                      </Typography.Paragraph>
                    )}
                    <Typography.Paragraph size="sm">{a.detail}</Typography.Paragraph>
                    {t && (
                      <Typography.Paragraph size="xs" color="muted">
                        <span className="tb-num font-medium text-foreground">¥{yuan(t.amount)}</span> ·{' '}
                        {t.other_account || '付款方未知'} · <span className="tb-num">{time(t.occurred_at)}</span> · 流水{' '}
                        <Mono>{t.external_id}</Mono>
                      </Typography.Paragraph>
                    )}
                    {resolved && (
                      <Surface variant="secondary" className="mt-0.5 rounded-lg px-3 py-2">
                        <Typography.Paragraph size="xs" color="muted">
                          {a.resolved_by} 于 <span className="tb-num">{time(a.resolved_at)}</span> 处理：{a.note}
                        </Typography.Paragraph>
                      </Surface>
                    )}
                  </ItemCard.Content>
                  {!resolved && (
                    <ItemCard.Action className="ms-0 flex gap-2 ps-0 sm:ms-auto sm:ps-2">
                      {canLink && (
                        <Button size="sm" onPress={() => setLinking({ txn: t, suggest: o?.trade_no })}>
                          <LinkIcon />
                          关联订单
                        </Button>
                      )}
                      <Button size="sm" variant={canLink ? 'tertiary' : 'secondary'} onPress={() => setResolving(a)}>
                        标为已处理
                      </Button>
                    </ItemCard.Action>
                  )}
                </ItemCard>
              </Fragment>
            )
          })
        )}
      </ItemCardGroup>
      {q.data && q.data.total > pageSize && (
        <Pager
          page={search.page ?? 1}
          pageSize={pageSize}
          total={q.data.total}
          onChange={(p) => void navigate({ search: (s) => ({ ...s, page: p > 1 ? p : undefined }) })}
        />
      )}
      <LinkModal linking={linking} onClose={() => setLinking(null)} />
      <NoteModal
        title="标为已处理"
        visible={resolving !== null}
        onCancel={() => setResolving(null)}
        description="不改变订单和流水，只记下你是怎么处理的。例如：已原路退款、已联系付款人、确认不是本系统的收款。"
        onOK={async (note) => {
          await api.post(`/web-api/admin/anomalies/${resolving!.id}/resolve`, { note })
          toastOK('已标为已处理')
          setResolving(null)
          void qc.invalidateQueries()
        }}
      />
      <OrderDetail id={detail} onClose={() => setDetail(null)} />
    </>
  )
}

function Transactions({ search }: { search: Search }) {
  const navigate = useNavigate({ from: Route.fullPath })
  const [kw, setKw] = useState(search.q ?? '')
  const [linking, setLinking] = useState<Linking | null>(null)
  const [detail, setDetail] = useState<number | null>(null)
  const channels = useQuery(channelsQuery).data?.items ?? []
  const channelName = (key: string) => channels.find((c) => c.key === key)?.name ?? key
  const q = useQuery({
    queryKey: ['transactions', search],
    queryFn: () =>
      api.get<Page<Transaction> & { trade_nos: Record<string, string> }>(
        `/web-api/admin/transactions${query({ state: search.state, direction: search.direction, channel_id: search.channel_id, q: search.q, page: search.page, page_size: pageSize })}`,
      ),
    placeholderData: keepPreviousData,
    refetchInterval: 15_000,
  })
  const set = (patch: Partial<Search>) =>
    void navigate({ search: (s) => ({ ...s, view: 'transactions' as const, page: undefined, ...patch }) })
  const order = (t: Transaction) =>
    t.order_id ? (
      <OrderLink tradeNo={q.data?.trade_nos[t.order_id] ?? t.order_id} onClick={() => setDetail(t.order_id)} />
    ) : invalidTxn(t) ? (
      <span className="text-muted">解析不了</span>
    ) : t.direction === 'in' ? (
      <Button size="sm" variant="secondary" onPress={() => setLinking({ txn: t })}>
        关联订单
      </Button>
    ) : (
      <span className="text-muted">—</span>
    )
  const amount = (t: Transaction) => (
    <span className={t.direction === 'in' ? 'tb-num font-medium text-success' : 'tb-num text-muted'}>
      {t.direction === 'in' ? '+' : '-'}
      {yuan(t.amount)}
    </span>
  )
  const stateTone = (t: Transaction) =>
    t.state === 'matched' ? 'success' : t.state === 'anomaly' ? 'warning' : 'default'
  return (
    <>
      <Toolbar
        end={
          q.data && (
            <span className="text-sm text-muted">
              共 <span className="tb-num">{q.data.total}</span> 条
            </span>
          )
        }
      >
        <SearchInput
          ariaLabel="搜索流水"
          className="w-full sm:w-72"
          placeholder="流水号、支付宝订单号、付款方或金额"
          value={kw}
          onChange={setKw}
          onSubmit={(v) => v !== (search.q ?? '') && set({ q: v || undefined })}
        />
        <SelectInput
          ariaLabel="对账状态"
          className="w-[calc(50%-4px)] sm:w-32"
          placeholder="全部状态"
          clearable
          value={search.state}
          onChange={(v) => set({ state: v })}
          options={Object.entries(txnStateText).map(([value, label]) => ({ value, label }))}
        />
        <SelectInput
          ariaLabel="收支方向"
          className="w-[calc(50%-4px)] sm:w-32"
          placeholder="收入和支出"
          clearable
          value={search.direction}
          onChange={(v) => set({ direction: v })}
          options={[
            { value: 'in', label: '只看收入' },
            { value: 'out', label: '只看支出' },
          ]}
        />
        {channels.length > 1 && (
          <SelectInput<number>
            ariaLabel="支付渠道"
            className="w-full sm:w-44"
            placeholder="全部渠道"
            clearable
            value={search.channel_id}
            onChange={(v) => set({ channel_id: v })}
            options={channels.map((c) => ({ value: c.id, label: c.name }))}
          />
        )}
      </Toolbar>
      {q.isError && <Notice status="danger">{q.error.message}</Notice>}
      <DataTable
        label="流水列表"
        rows={q.data?.items ?? []}
        rowKey={(t) => t.id}
        loading={q.isFetching && !q.data}
        minWidth={940}
        empty="没有符合条件的流水"
        pagination={{
          page: search.page ?? 1,
          pageSize,
          total: q.data?.total ?? 0,
          onChange: (p) => void navigate({ search: (s) => ({ ...s, page: p > 1 ? p : undefined }) }),
        }}
        card={(t) => (
          <div className="flex flex-col gap-1.5">
            <div className="flex items-center justify-between gap-3">
              <span className="min-w-0 truncate text-sm">{t.other_account || '付款方未知'}</span>
              <span className="text-base">{amount(t)}</span>
            </div>
            <div className="flex items-center justify-between gap-3 text-xs text-muted">
              <span className="tb-num">{time(t.occurred_at)}</span>
              <Pill tone={stateTone(t)}>{txnStateText[t.state]}</Pill>
            </div>
            <div className="flex items-center justify-between gap-3 text-xs">
              <span className="min-w-0 truncate text-muted">{channelName(t.channel)}</span>
              {order(t)}
            </div>
          </div>
        )}
        columns={[
          { title: '到账时间', nowrap: true, render: (t) => <span className="tb-num">{time(t.occurred_at)}</span> },
          { title: '金额（元）', align: 'right', nowrap: true, render: amount },
          {
            title: '付款方 / 对方',
            render: (t) => (
              <Stack className="max-w-52" main={t.other_account || '—'} sub={t.memo || t.biz_desc || undefined} />
            ),
          },
          {
            title: '渠道',
            render: (t) => (
              <Stack
                className="max-w-40"
                main={channelName(t.channel)}
                sub={t.kind === 'upstream' ? '第三方平台确认' : t.kind || undefined}
              />
            ),
          },
          { title: '流水号', render: (t) => <Mono>{t.external_id}</Mono> },
          { title: '对账', nowrap: true, render: (t) => <Pill tone={stateTone(t)}>{txnStateText[t.state]}</Pill> },
          { title: '订单', nowrap: true, render: order },
        ]}
      />
      <LinkModal linking={linking} onClose={() => setLinking(null)} />
      <OrderDetail id={detail} onClose={() => setDetail(null)} />
    </>
  )
}
