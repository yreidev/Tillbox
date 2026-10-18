import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Button, Form } from '@yreidev/ui'
import { ArrowDownToLine } from '@gravity-ui/icons'
import { api } from '../../lib/api'
import { relative, time } from '../../lib/format'
import { useNow } from '../../lib/now'
import {
  DataTable,
  Desc,
  Mono,
  Notice,
  NumberInput,
  PageHeader,
  PageSkeleton,
  Panel,
  SettingsRow,
  Stack,
  StatusDot,
  SwitchInput,
  toastError,
  toastOK,
} from '../../lib/ui'

export const Route = createFileRoute('/admin/operations')({ component: OperationsPage })

interface Retention {
  enabled: boolean
  raw_days: number
  attempt_days: number
  audit_days: number
}
interface Operations {
  retention: Retention
  state: { last_run_at: number; last_ok_at: number; last_error: string; archived: number }
  archives: { id: number; filename: string; records: number; sha256: string; created_at: number }[]
}

function OperationsPage() {
  const qc = useQueryClient()
  const q = useQuery({
    queryKey: ['operations'],
    queryFn: () => api.get<Operations>('/web-api/admin/operations'),
    refetchInterval: 15_000,
  })
  const refresh = () => qc.invalidateQueries({ queryKey: ['operations'] })
  const archive = useMutation({
    mutationFn: () => api.post('/web-api/admin/operations/archive'),
    onSuccess: async () => {
      await refresh()
      toastOK('归档检查完成')
    },
    onError: toastError,
  })
  const d = q.data
  const now = useNow()
  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        description="过了保留期的原始记录归档下线，归档批次可以下载。系统运行情况在监控页。每 15 秒刷新。"
        actions={
          d && (
            <Button
              size="sm"
              variant="secondary"
              isPending={archive.isPending}
              isDisabled={!d.retention.enabled}
              onPress={() => archive.mutate()}
            >
              立即执行归档
            </Button>
          )
        }
      />
      {q.isError && <Notice status="danger">{q.error.message}</Notice>}
      {q.isPending && <PageSkeleton rows={2} />}
      {d && (
        <>
          <div className="grid items-start gap-4 lg:grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)]">
            <RetentionForm key={JSON.stringify(d.retention)} initial={d.retention} onSaved={refresh} />
            <Panel title="归档任务">
              <div className="flex flex-col gap-4">
                <StatusDot
                  tone={
                    !d.retention.enabled
                      ? 'default'
                      : d.state.last_error
                        ? 'warning'
                        : d.state.last_ok_at
                          ? 'success'
                          : 'default'
                  }
                >
                  {!d.retention.enabled
                    ? '自动归档已关闭'
                    : d.state.last_error
                      ? '上次运行出错'
                      : d.state.last_ok_at
                        ? `正常 · ${relative(d.state.last_ok_at, now)}成功`
                        : '还没运行过'}
                </StatusDot>
                <Desc
                  data={[
                    {
                      key: '上次运行',
                      value: <span className="tb-num">{time(d.state.last_run_at) || '尚未执行'}</span>,
                    },
                    {
                      key: '上次成功',
                      value: <span className="tb-num">{time(d.state.last_ok_at) || '尚未执行'}</span>,
                    },
                    { key: '上次归档', value: <span className="tb-num">{d.state.archived} 条</span> },
                  ]}
                />
                {d.state.last_error && <Notice status="warning">{d.state.last_error}</Notice>}
                <p className="text-xs text-muted">
                  订单、金额、流水编号和关联关系长期保留。只归档过了保留期且已完结的原始响应与通知历史，每笔订单至少保留最后一次通知；有待处理异常的记录继续在线保留。归档先落盘、校验并加密，再清理在线副本。
                </p>
              </div>
            </Panel>
          </div>
          <div className="mt-2 flex flex-col gap-1">
            <h2 className="text-base font-semibold">归档批次</h2>
            <p className="text-sm text-muted">
              最近 50 批。下载时会校验并解密为 JSONL.GZ，可能含账务信息，请妥善保管；服务器上的加密归档要配套 master.key
              才能恢复。
            </p>
          </div>
          <DataTable
            label="归档批次"
            rows={d.archives ?? []}
            rowKey={(a) => a.id}
            minWidth={650}
            empty="还没有归档批次"
            card={(a) => (
              <div className="flex items-center justify-between gap-3">
                <Stack
                  className="min-w-0"
                  main={<Mono>{a.filename}</Mono>}
                  sub={
                    <>
                      <span className="tb-num">{a.records}</span> 条 ·{' '}
                      <span className="tb-num">{time(a.created_at, false)}</span>
                    </>
                  }
                />
                <Download id={a.id} />
              </div>
            )}
            columns={[
              {
                title: '归档文件',
                render: (a) => (
                  <Stack
                    className="max-w-96"
                    main={<Mono>{a.filename}</Mono>}
                    sub={
                      <span className="tb-mono" title={a.sha256}>
                        SHA-256 {a.sha256.slice(0, 16)}…
                      </span>
                    }
                  />
                ),
              },
              {
                title: '记录数',
                align: 'right',
                nowrap: true,
                render: (a) => <span className="tb-num">{a.records}</span>,
              },
              { title: '创建时间', nowrap: true, render: (a) => <span className="tb-num">{time(a.created_at)}</span> },
              { title: '', align: 'right', nowrap: true, render: (a) => <Download id={a.id} /> },
            ]}
          />
        </>
      )}
    </div>
  )
}

function Download({ id }: { id: number }) {
  return (
    <Button
      size="sm"
      variant="ghost"
      onPress={() => window.open(`/web-api/admin/operations/archives/${id}`, '_blank', 'noopener,noreferrer')}
    >
      <ArrowDownToLine />
      下载
    </Button>
  )
}

// 范围和 internal/settings/operations.go 一致。
const retentionFields: {
  field: 'raw_days' | 'attempt_days' | 'audit_days'
  label: string
  help: string
  min: number
  def: number
}[] = [
  {
    field: 'raw_days',
    label: '流水原始响应',
    help: '支付宝和上游返回的原始数据。流水本身（金额、编号、关联）不受影响。',
    min: 30,
    def: 90,
  },
  {
    field: 'attempt_days',
    label: '通知尝试记录',
    help: '每次通知商户的请求和响应；每笔订单至少保留最后一次。',
    min: 30,
    def: 90,
  },
  { field: 'audit_days', label: '审计日志', help: '后台和 AI 自动处理的操作记录。', min: 90, def: 365 },
]

function RetentionForm({ initial, onSaved }: { initial: Retention; onSaved: () => Promise<void> }) {
  const [v, setV] = useState(initial)
  const [busy, setBusy] = useState(false)
  const dirty = JSON.stringify(v) !== JSON.stringify(initial)
  return (
    <Panel title="在线保留期" description="超过保留期的记录会归档后从数据库移除。">
      <Form
        onSubmit={async (e) => {
          e.preventDefault()
          setBusy(true)
          try {
            await api.put('/web-api/admin/operations/retention', v)
            await onSaved()
            toastOK('保留期已保存')
          } catch (err) {
            toastError(err)
          } finally {
            setBusy(false)
          }
        }}
      >
        <div className="divide-y divide-separator">
          <SettingsRow label="自动归档" description="每天检查一次。关闭后记录一直留在数据库里。">
            <SwitchInput
              label={v.enabled ? '已开启' : '已关闭'}
              value={v.enabled}
              onChange={(enabled) => setV((s) => ({ ...s, enabled }))}
            />
          </SettingsRow>
          {retentionFields.map((f) => (
            <SettingsRow
              key={f.field}
              label={f.label}
              description={
                <>
                  {f.help}
                  <span className="mt-1 block text-xs">
                    默认 <span className="tb-num">{f.def}</span> 天，可设 <span className="tb-num">{f.min}–3650</span>{' '}
                    天
                  </span>
                </>
              }
            >
              <div className="flex items-center gap-3">
                <NumberInput
                  ariaLabel={f.label}
                  className="w-40"
                  isRequired
                  isDisabled={!v.enabled}
                  minValue={f.min}
                  maxValue={3650}
                  value={Number.isNaN(v[f.field]) ? undefined : v[f.field]}
                  onChange={(n) => setV((s) => ({ ...s, [f.field]: n ?? NaN }))}
                />
                <span className="text-sm text-muted">天</span>
              </div>
            </SettingsRow>
          ))}
        </div>
        <div className="mt-5 flex justify-end gap-2">
          <Button size="sm" variant="tertiary" isDisabled={!dirty} onPress={() => setV(initial)}>
            撤销修改
          </Button>
          <Button size="sm" type="submit" isDisabled={!dirty} isPending={busy}>
            保存
          </Button>
        </div>
      </Form>
    </Panel>
  )
}
