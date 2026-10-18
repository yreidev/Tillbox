import { useState } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Button, Card, Tooltip } from '@yreidev/ui'
import { ArrowRotateRight, Xmark } from '@gravity-ui/icons'
import { OrderDetail } from '../../components/order-detail'
import { api, overviewQuery, query } from '../../lib/api'
import { relative, time } from '../../lib/format'
import { useNow } from '../../lib/now'
import type { NotificationView, NotifyAttempt, Page } from '../../lib/types'
import { notifyText } from '../../lib/types'
import {
  DataTable,
  FilterTabs,
  Mono,
  Notice,
  OrderLink,
  PageHeader,
  Stack,
  StatusDot,
  cx,
  toastError,
  toastOK,
  type Tone,
} from '../../lib/ui'

type Status = NotificationView['status']

interface Search {
  status?: Status
  page?: number
}

export const Route = createFileRoute('/admin/notifications')({
  validateSearch: (s: Record<string, unknown>): Search => ({
    status: typeof s.status === 'string' && s.status in notifyText ? (s.status as Status) : undefined,
    page: typeof s.page === 'number' ? s.page : undefined,
  }),
  component: NotificationsPage,
})

const pageSize = 20

// schedule 和 internal/notify/worker.go 的 Schedule 一致：相对第一次通知的时间点。
const schedule = ['立即', '1 分钟', '3 分钟', '19 分钟', '55 分钟', '2 小时', '6 小时', '24 小时']

const statusTone: Record<Status, Tone> = { pending: 'accent', success: 'success', dead: 'danger' }

function NotificationsPage() {
  const search = Route.useSearch()
  const navigate = useNavigate({ from: Route.fullPath })
  const qc = useQueryClient()
  const [detail, setDetail] = useState<number | null>(null)
  const ov = useQuery(overviewQuery(7)).data
  const q = useQuery({
    queryKey: ['notifications', search],
    queryFn: () =>
      api.get<Page<NotificationView>>(
        `/web-api/admin/notifications${query({ status: search.status, page: search.page, page_size: pageSize })}`,
      ),
    placeholderData: keepPreviousData,
    refetchInterval: 10_000,
  })
  const resend = useMutation({
    mutationFn: (id: number) => api.post<{ attempt: NotifyAttempt }>(`/web-api/admin/notifications/${id}/resend`),
    onSuccess: (r) => {
      if (r.attempt.success) toastOK('通知已送达，商户确认收到')
      else
        toastError(
          new Error(`商户没有确认：${r.attempt.error || r.attempt.response || `HTTP ${r.attempt.http_status}`}`),
        )
      void qc.invalidateQueries()
    },
    onError: toastError,
  })
  const now = useNow()
  const resendButton = (n: NotificationView) => (
    <Button
      size="sm"
      variant={n.status === 'success' ? 'ghost' : 'secondary'}
      isPending={resend.isPending && resend.variables === n.order_id}
      onPress={() => resend.mutate(n.order_id)}
    >
      <ArrowRotateRight />
      重发
    </Button>
  )
  const statusLine = (n: NotificationView) =>
    n.status === 'pending'
      ? n.attempts === 0
        ? '马上发出第一次'
        : n.next_at
          ? `${relative(n.next_at, now)}发第 ${n.attempts + 1} 次`
          : undefined
      : n.status === 'dead'
        ? '自动重发已停止'
        : n.last_at
          ? `${relative(n.last_at, now)}送达`
          : undefined

  return (
    <div className="flex flex-col gap-4">
      <PageHeader description="付款确认后，系统会通知商户网站发货。商户返回 success 才算送达；没送达就按下面的计划自动重发。">
        <ScheduleCard />
      </PageHeader>
      <FilterTabs
        ariaLabel="通知状态"
        value={search.status ?? 'all'}
        onChange={(v) => void navigate({ search: { status: v === 'all' ? undefined : v } })}
        options={[
          { value: 'all', label: '全部' },
          { value: 'dead', label: '已放弃', count: ov?.dead_notifications, tone: 'danger' },
          { value: 'pending', label: '重试中', count: ov?.retrying_notifications, tone: 'accent' },
          { value: 'success', label: '已送达' },
        ]}
      >
        {search.status === 'dead' && !!q.data?.total && (
          <Notice status="danger" title="这些订单的商户还不知道已付款">
            先确认商户的通知地址能访问、验签通过并返回 success，再逐笔重发。重发成功后付款人才能在商户那边拿到货。
          </Notice>
        )}
        {q.isError && <Notice status="danger">{q.error.message}</Notice>}
        <DataTable
          label="通知列表"
          rows={q.data?.items ?? []}
          rowKey={(n) => n.order_id}
          loading={q.isFetching && !q.data}
          minWidth={980}
          empty={search.status === 'dead' ? '没有放弃的通知，商户都收到了' : '没有通知记录'}
          pagination={{
            page: search.page ?? 1,
            pageSize,
            total: q.data?.total ?? 0,
            onChange: (p) => void navigate({ search: (s) => ({ ...s, page: p > 1 ? p : undefined }) }),
          }}
          card={(n) => (
            <div className="flex flex-col gap-2">
              <div className="flex items-center justify-between gap-3">
                <OrderLink tradeNo={n.trade_no} onClick={() => setDetail(n.order_id)} />
                <StatusDot tone={statusTone[n.status]}>{notifyText[n.status]}</StatusDot>
              </div>
              <Attempts n={n} />
              {n.last_error && n.status !== 'success' && (
                <p className="line-clamp-2 text-xs break-all text-danger">{n.last_error}</p>
              )}
              <div className="flex items-center justify-between gap-3">
                <span className="min-w-0 truncate text-xs text-muted">{statusLine(n)}</span>
                {resendButton(n)}
              </div>
            </div>
          )}
          columns={[
            {
              title: '订单',
              render: (n) => (
                <Stack
                  main={<OrderLink tradeNo={n.trade_no} onClick={() => setDetail(n.order_id)} />}
                  sub={<span title="商户订单号">{n.out_trade_no}</span>}
                  className="max-w-48"
                />
              ),
            },
            {
              title: '状态',
              nowrap: true,
              render: (n) => (
                <Stack
                  main={<StatusDot tone={statusTone[n.status]}>{notifyText[n.status]}</StatusDot>}
                  sub={statusLine(n)}
                />
              ),
            },
            { title: '自动通知', nowrap: true, render: (n) => <Attempts n={n} /> },
            {
              title: '最近一次',
              render: (n) => (
                <Stack
                  className="max-w-60"
                  main={<span className="tb-num">{time(n.last_at) || '还没发'}</span>}
                  sub={
                    n.last_error && n.status !== 'success' ? (
                      <span className="text-danger" title={n.last_error}>
                        {n.last_error}
                      </span>
                    ) : undefined
                  }
                />
              ),
            },
            {
              title: '通知地址',
              render: (n) => (
                <span className="block max-w-44 truncate text-xs" title={n.notify_url}>
                  <Mono>{host(n.notify_url)}</Mono>
                </span>
              ),
            },
            { title: '', align: 'right', nowrap: true, render: resendButton },
          ]}
        />
      </FilterTabs>
      <OrderDetail id={detail} onClose={() => setDetail(null)} />
    </div>
  )
}

function host(url: string) {
  try {
    const u = new URL(url)
    return u.host + u.pathname
  } catch {
    return url
  }
}

// ScheduleCard 把 8 次自动通知的时间点画成一条线，最后一格是放弃。
function ScheduleCard() {
  return (
    <Card className="gap-3 rounded-2xl">
      <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <span className="text-sm font-medium">自动重发计划</span>
        <span className="text-xs text-muted">时间从第一次通知算起；8 次都没送达就放弃，需要你手动重发</span>
      </div>
      <ol className="-mx-1 flex overflow-x-auto px-1 pb-1">
        {schedule.map((s, i) => (
          <li
            key={s}
            className="relative flex min-w-16 flex-1 flex-col items-center gap-1.5 before:absolute before:top-3 before:right-1/2 before:h-px before:w-full before:bg-separator first:before:hidden"
          >
            <span className="tb-num relative grid size-6 place-items-center rounded-full bg-accent-soft text-xs font-medium text-accent-soft-foreground">
              {i + 1}
            </span>
            <span className="text-xs whitespace-nowrap text-muted">{s}</span>
          </li>
        ))}
        <li className="relative flex min-w-16 flex-1 flex-col items-center gap-1.5 before:absolute before:top-3 before:right-1/2 before:h-px before:w-full before:border-t before:border-dashed before:border-separator">
          <span className="relative grid size-6 place-items-center rounded-full bg-danger-soft text-danger-soft-foreground">
            <Xmark className="size-3.5" />
          </span>
          <span className="text-xs whitespace-nowrap text-muted">放弃</span>
        </li>
      </ol>
    </Card>
  )
}

// Attempts 用 8 小格表示自动通知用掉了几次；悬停看每次的时间点。
function Attempts({ n }: { n: NotificationView }) {
  const used = Math.min(n.attempts, schedule.length)
  const fill = n.status === 'success' ? 'bg-success' : n.status === 'dead' ? 'bg-danger' : 'bg-warning'
  return (
    <Tooltip delay={200} closeDelay={0}>
      <Tooltip.Trigger className="inline-flex items-center gap-2" tabIndex={-1} role="presentation">
        <span className="flex gap-0.5" aria-hidden>
          {schedule.map((s, i) => (
            <span
              key={s}
              className={cx(
                'h-2 w-2.5 rounded-sm',
                i < used ? fill : i === used && n.status === 'pending' ? 'bg-accent/40' : 'bg-default',
              )}
            />
          ))}
        </span>
        <span className="tb-num text-xs text-muted">
          {used}/{schedule.length}
        </span>
      </Tooltip.Trigger>
      <Tooltip.Content>
        已自动通知 {used} 次（共 {schedule.length} 次）
        {n.first_at > 0 && <span className="tb-num">，第一次在 {time(n.first_at)}</span>}
      </Tooltip.Content>
    </Tooltip>
  )
}
