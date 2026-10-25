import { useState } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { Button } from '@yreidev/ui'
import { ArrowDownToLine, FunnelXmark } from '@gravity-ui/icons'
import { OrderDetail } from '../../components/order-detail'
import { api, channelsQuery, overviewQuery, query } from '../../lib/api'
import { relative, time, yuan } from '../../lib/format'
import { useNow } from '../../lib/now'
import type { Merchant, OrderState, OrderView, Page } from '../../lib/types'
import { payTypeLabel, stateText } from '../../lib/types'
import {
  DataTable,
  DateRangeInput,
  FilterTabs,
  Money,
  Mono,
  Notice,
  PageHeader,
  Pill,
  SearchInput,
  SelectInput,
  Stack,
  StateTag,
  Toolbar,
} from '../../lib/ui'

interface Search {
  state?: string
  q?: string
  merchant_id?: number
  channel_id?: number
  from?: number
  to?: number
  page?: number
}

const num = (v: unknown) =>
  typeof v === 'number' ? v : typeof v === 'string' && /^\d+$/.test(v) ? Number(v) : undefined

export const Route = createFileRoute('/admin/orders')({
  validateSearch: (s: Record<string, unknown>): Search => ({
    state: typeof s.state === 'string' && s.state in stateText ? s.state : undefined,
    q: typeof s.q === 'string' && s.q ? s.q : undefined,
    merchant_id: num(s.merchant_id),
    channel_id: num(s.channel_id),
    from: num(s.from),
    to: num(s.to),
    page: num(s.page),
  }),
  component: OrdersPage,
})

const pageSize = 20
const tabs: (OrderState | 'all')[] = ['all', 'pending', 'paid', 'disputed', 'expired', 'closed']

function OrdersPage() {
  const search = Route.useSearch()
  const navigate = useNavigate({ from: Route.fullPath })
  const [kw, setKw] = useState(search.q ?? '')
  const [detail, setDetail] = useState<number | null>(null)
  const set = (patch: Partial<Search>) => void navigate({ search: (s) => ({ ...s, page: undefined, ...patch }) })

  const filter = {
    state: search.state,
    q: search.q,
    merchant_id: search.merchant_id,
    channel_id: search.channel_id,
    from: search.from,
    to: search.to,
  }
  const q = useQuery({
    queryKey: ['orders', search],
    queryFn: () =>
      api.get<Page<OrderView>>(`/web-api/admin/orders${query({ ...filter, page: search.page, page_size: pageSize })}`),
    placeholderData: keepPreviousData,
    refetchInterval: 15_000,
  })
  const merchants = useQuery({
    queryKey: ['merchants'],
    queryFn: () => api.get<{ items: { merchant: Merchant; orders: number }[] }>('/web-api/admin/merchants'),
  })
  const channels = useQuery(channelsQuery).data?.items ?? []
  const pending = useQuery(overviewQuery(7)).data?.pending_orders ?? 0
  const filtered = !!(search.q || search.merchant_id || search.channel_id || search.from)
  const now = useNow()

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        description="点一行看详情：付款进度、到账流水和商户通知都在里面。"
        actions={
          <Button
            size="sm"
            variant="secondary"
            onPress={() => window.open(`/web-api/admin/orders/export${query(filter)}`, '_blank')}
          >
            <ArrowDownToLine />
            导出 CSV
          </Button>
        }
      />
      <FilterTabs
        ariaLabel="订单状态"
        value={(search.state as OrderState | undefined) ?? 'all'}
        onChange={(v) => set({ state: v === 'all' ? undefined : v })}
        options={tabs.map((t) => ({
          value: t,
          label: t === 'all' ? '全部' : stateText[t],
          count: t === 'pending' ? pending : undefined,
          tone: 'accent',
        }))}
      >
        <Toolbar
          end={
            q.data && (
              <span className="text-sm text-muted">
                共 <span className="tb-num">{q.data.total}</span> 笔
              </span>
            )
          }
        >
          <SearchInput
            ariaLabel="搜索订单"
            className="w-full sm:w-72"
            placeholder="订单号、商户订单号、交易号或金额"
            value={kw}
            onChange={setKw}
            onSubmit={(v) => v !== (search.q ?? '') && set({ q: v || undefined })}
          />
          <SelectInput<number>
            ariaLabel="商户"
            className="w-[calc(50%-4px)] sm:w-44"
            placeholder="全部商户"
            clearable
            value={search.merchant_id}
            onChange={(v) => set({ merchant_id: v })}
            options={(merchants.data?.items ?? []).map(({ merchant: m }) => ({ value: m.id, label: m.name }))}
          />
          {channels.length > 1 && (
            <SelectInput<number>
              ariaLabel="支付渠道"
              className="w-[calc(50%-4px)] sm:w-44"
              placeholder="全部渠道"
              clearable
              value={search.channel_id}
              onChange={(v) => set({ channel_id: v })}
              options={channels.map((c) => ({ value: c.id, label: c.name }))}
            />
          )}
          <DateRangeInput
            ariaLabel="下单日期"
            className="w-full sm:w-auto"
            from={search.from}
            to={search.to}
            onChange={(from, to) => set({ from, to })}
          />
          {filtered && (
            <Button
              size="sm"
              variant="ghost"
              onPress={() => {
                setKw('')
                set({ q: undefined, merchant_id: undefined, channel_id: undefined, from: undefined, to: undefined })
              }}
            >
              <FunnelXmark />
              清除筛选
            </Button>
          )}
        </Toolbar>
        {q.isError && <Notice status="danger">{q.error.message}</Notice>}
        <DataTable
          label="订单列表"
          rows={q.data?.items ?? []}
          rowKey={(o) => o.id}
          loading={q.isFetching && !q.data}
          minWidth={980}
          onRowClick={(o) => setDetail(o.id)}
          empty={
            filtered || search.state
              ? '没有符合条件的订单'
              : '还没有订单。商户网站下单后会出现在这里，也可以先用“支付测试”走一遍。'
          }
          pagination={{
            page: search.page ?? 1,
            pageSize,
            total: q.data?.total ?? 0,
            onChange: (p) => void navigate({ search: (s) => ({ ...s, page: p > 1 ? p : undefined }) }),
          }}
          card={(o) => (
            <div className="flex flex-col gap-1">
              <div className="flex items-center justify-between gap-3">
                <span className="tb-mono truncate text-xs text-muted">{o.trade_no}</span>
                <StateTag state={o.state} />
              </div>
              <div className="flex items-baseline justify-between gap-3">
                <span className="flex min-w-0 items-center gap-1.5 font-medium">
                  {o.is_test && <Pill>测试</Pill>}
                  <span className="truncate">{o.name}</span>
                </span>
                <span className="tb-num shrink-0 font-semibold">¥{yuan(o.pay_amount)}</span>
              </div>
              <span className="truncate text-xs text-muted">
                {o.merchant_name || `商户 ${o.merchant_id}`} ·{' '}
                {o.channel ? o.channel_name || o.channel : '还没选支付方式'} · {time(o.created_at, false)}
              </span>
            </div>
          )}
          columns={[
            {
              title: '订单',
              render: (o) => (
                <Stack
                  className="max-w-64"
                  main={<Mono>{o.trade_no}</Mono>}
                  sub={
                    <span className="flex items-center gap-1.5">
                      {o.is_test && <Pill>测试</Pill>}
                      <span className="truncate" title={o.name}>
                        {o.name}
                      </span>
                    </span>
                  }
                />
              ),
            },
            {
              title: '商户',
              render: (o) => (
                <Stack
                  className="max-w-52"
                  main={o.merchant_name || `已删除（${o.merchant_id}）`}
                  sub={<span title={o.out_trade_no}>{o.out_trade_no}</span>}
                />
              ),
            },
            {
              title: '渠道',
              render: (o) => (
                <Stack
                  className="max-w-40"
                  main={o.channel ? o.channel_name || o.channel : '未定'}
                  sub={o.pay_type ? payTypeLabel(o.pay_type) : '还没选支付方式'}
                />
              ),
            },
            {
              title: '应付（元）',
              align: 'right',
              nowrap: true,
              render: (o) => (
                <Stack
                  main={
                    <span className="font-medium">
                      <Money cents={o.pay_amount} />
                    </span>
                  }
                  sub={
                    o.paid_amount && o.paid_amount !== o.pay_amount ? (
                      <span className="text-warning">实收 {yuan(o.paid_amount)}</span>
                    ) : o.amount !== o.pay_amount ? (
                      `订单 ${yuan(o.amount)}`
                    ) : undefined
                  }
                />
              ),
            },
            { title: '状态', nowrap: true, render: (o) => <StateTag state={o.state} /> },
            {
              title: '时间',
              nowrap: true,
              render: (o) => (
                <Stack
                  main={<span className="tb-num">{time(o.created_at, false)}</span>}
                  sub={
                    o.paid_at ? (
                      <span className="tb-num">支付于 {time(o.paid_at).slice(11)}</span>
                    ) : o.state === 'pending' && o.expires_at > now ? (
                      `${relative(o.expires_at, now)}过期`
                    ) : undefined
                  }
                />
              ),
            },
          ]}
        />
      </FilterTabs>
      <OrderDetail id={detail} onClose={() => setDetail(null)} />
    </div>
  )
}
