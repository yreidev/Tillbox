import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Button, Tooltip } from '@heroui/react'
import { ArrowRotateRight } from '@gravity-ui/icons'
import { api } from '../lib/api'
import { relative, time } from '../lib/format'
import { useNow } from '../lib/now'
import { ledgerErrorText, type LedgerStatus } from '../lib/types'
import { StatusDot, cx, toastError, toastOK, type Tone } from '../lib/ui'

// ledgerHealth 把一个经营码渠道的查账状态归成一句话和一个颜色。
export function ledgerHealth(s: LedgerStatus, now = Date.now()): { tone: Tone; text: string; pulse?: boolean } {
  if (!s.enabled) return { tone: 'default', text: '已停用' }
  if (s.failures > 0) return { tone: 'danger', text: '查账失败' }
  if (s.last_error) return { tone: 'warning', text: '暂不查账' }
  if (s.running) return { tone: 'accent', text: '查询中', pulse: true }
  if (s.active) return { tone: 'success', text: '有待付订单，加快查询', pulse: true }
  if (s.backfill_error) return { tone: 'warning', text: '历史补扫出错' }
  return { tone: 'success', text: s.last_ok_at ? `正常 · ${relative(s.last_ok_at, now)}查过` : '正常' }
}

/**
 * LedgerRow 是一个经营码渠道的查账状态：一行状态，一行进度，出错时再加一行原因。
 * 概览页和流水页的“查账”卡片都用它；默认按竖排列表留上下间距，排成多栏时用 className 去掉。
 */
export function LedgerRow({ status: s, className }: { status: LedgerStatus; className?: string }) {
  const qc = useQueryClient()
  const poll = useMutation({
    mutationFn: () => api.post('/web-api/admin/ledger/poll', { channel_id: s.channel_id }),
    onSuccess: () => {
      toastOK('已安排立即查账')
      setTimeout(() => void qc.invalidateQueries(), 1500)
    },
    onError: toastError,
  })
  const now = useNow()
  const health = ledgerHealth(s, now)
  const backoff = s.backoff_until > now
  const facts = [
    s.last_end ? `已查到 ${time(s.last_end, false)}` : '还没查过',
    s.enabled && s.next_at ? `下一轮 ${relative(s.next_at, now)}` : '',
    s.backlog_segments > 0 ? `历史补扫剩 ${s.backlog_segments} 段` : '',
  ].filter(Boolean)
  return (
    <div className={cx('flex items-start gap-3 py-3 first:pt-0 last:pb-0', className)}>
      <div className="min-w-0 flex-1">
        <div className="truncate text-sm font-medium">{s.name}</div>
        <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1">
          <StatusDot tone={health.tone} pulse={health.pulse}>
            <span className="text-xs">{health.text}</span>
          </StatusDot>
          <span className="tb-num text-xs text-muted">{facts.join(' · ')}</span>
        </div>
        {s.enabled && s.last_error && (
          <p className={s.failures > 0 ? 'mt-1.5 text-xs text-danger' : 'mt-1.5 text-xs text-warning'}>
            {ledgerErrorText(s)}
            {backoff && `。${relative(s.backoff_until, now)}重试`}
          </p>
        )}
        {s.backfill_error && (
          <p className="mt-1.5 text-xs text-warning">
            历史补扫连续失败 {s.backfill_failures} 次：{s.backfill_error}
            {s.backfill_retry_at > now && `。${relative(s.backfill_retry_at, now)}重试`}
          </p>
        )}
      </div>
      <Tooltip delay={300}>
        <Button
          isIconOnly
          size="sm"
          variant="tertiary"
          aria-label={`立即查账：${s.name}`}
          isDisabled={!s.enabled}
          isPending={poll.isPending}
          onPress={() => poll.mutate()}
        >
          <ArrowRotateRight />
        </Button>
        <Tooltip.Content>立即查账</Tooltip.Content>
      </Tooltip>
    </div>
  )
}
