import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Button, Form, Typography } from '@yreidev/ui'
import { ArrowDownToLine } from '@gravity-ui/icons'
import { api } from '../../lib/api'
import { bytes, relative, time } from '../../lib/format'
import { useNow } from '../../lib/now'
import {
  Code,
  ConflictNotice,
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
  useRevision,
  type Tone,
} from '../../lib/ui'

export const Route = createFileRoute('/admin/operations')({ component: OperationsPage })

interface Retention {
  enabled: boolean
  raw_days: number
  attempt_days: number
  audit_days: number
}
interface BackupConfig {
  enabled: boolean
  keep: number
}
interface Backup {
  /** 数据库是 SQLite 才有内置备份 */
  supported: boolean
  config: BackupConfig
  revision: number
  state: { last_run_at: number; last_ok_at: number; last_error: string; last_file: string }
  files: { name: string; size: number; created_at: number }[]
  /** 服务器上的备份目录 */
  dir: string
}
interface Operations {
  retention: Retention
  retention_revision: number
  state: { last_run_at: number; last_ok_at: number; last_error: string; archived: number }
  archives: { id: number; filename: string; records: number; sha256: string; created_at: number }[]
  backup: Backup
}

const operationsQuery = {
  queryKey: ['operations'],
  queryFn: () => api.get<Operations>('/web-api/admin/operations'),
}

function OperationsPage() {
  const qc = useQueryClient()
  const q = useQuery({ ...operationsQuery, refetchInterval: 15_000 })
  const refresh = () => qc.invalidateQueries({ queryKey: ['operations'] })
  const d = q.data
  return (
    <div className="flex flex-col gap-4">
      <PageHeader description="打开自动备份后数据库每天备份一份；过了保留期的原始记录归档下线，归档批次可以下载。系统运行情况在监控页。每 15 秒刷新。" />
      {q.isError && <Notice status="danger">{q.error.message}</Notice>}
      {q.isPending && <PageSkeleton rows={3} />}
      {d && (
        <>
          <Typography.Heading level={2} className="text-base">
            自动备份
          </Typography.Heading>
          {d.backup.supported ? (
            <BackupSection b={d.backup} onSaved={refresh} />
          ) : (
            <Notice status="accent" title="当前用的是 PostgreSQL">
              内置备份只支持 SQLite。请用 <Code>pg_dump</Code> 或云数据库自带的备份，并另外保存主密钥。
            </Notice>
          )}
          <Typography.Heading level={2} className="mt-4 text-base">
            归档
          </Typography.Heading>
          <ArchiveSection d={d} onSaved={refresh} />
        </>
      )}
    </div>
  )
}

function BackupSection({ b, onSaved }: { b: Backup; onSaved: () => Promise<void> }) {
  const now = useNow()
  const run = useMutation({
    mutationFn: () => api.post('/web-api/admin/operations/backup'),
    onSuccess: async () => {
      await onSaved()
      toastOK('已备份')
    },
    onError: async (err) => {
      toastError(err)
      await onSaved()
    },
  })
  const s = b.state
  const status: { tone: Tone; text: string } = !b.config.enabled
    ? { tone: 'default', text: '自动备份已关闭' }
    : s.last_error
      ? { tone: 'warning', text: '上次备份失败' }
      : s.last_ok_at
        ? { tone: 'success', text: `正常 · ${relative(s.last_ok_at, now)}成功` }
        : { tone: 'default', text: '还没备份过' }
  return (
    <>
      <div className="grid items-start gap-4 lg:grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)]">
        <BackupForm b={b} onSaved={onSaved} />
        <Panel
          title="备份任务"
          extra={
            <Button size="sm" variant="secondary" isPending={run.isPending} onPress={() => run.mutate()}>
              立即备份
            </Button>
          }
        >
          <div className="flex flex-col gap-4">
            <StatusDot tone={status.tone}>{status.text}</StatusDot>
            <Desc
              data={[
                { key: '上次运行', value: <span className="tb-num">{time(s.last_run_at) || '尚未执行'}</span> },
                { key: '上次成功', value: <span className="tb-num">{time(s.last_ok_at) || '尚未执行'}</span> },
                { key: '备份目录', value: <Mono>{b.dir}</Mono> },
              ]}
            />
            {s.last_error && <Notice status="warning">{s.last_error}</Notice>}
            <Typography.Paragraph size="xs" color="muted">
              升级前先点“立即备份”，或在服务器上用正在运行的版本执行 <Code>tillbox backup</Code>。备份文件只在服务器上，
              请定期复制到别的机器；磁盘坏了，同一台机器上的备份也会一起丢。
            </Typography.Paragraph>
          </div>
        </Panel>
      </div>
      <div className="mt-2 flex flex-col gap-1">
        <Typography.Heading level={3} className="text-sm">
          备份文件
        </Typography.Heading>
        <Typography.Paragraph size="sm" color="muted">
          新的在前。恢复：停掉 Tillbox，把要用的备份文件复制成数据目录里的 <Code>tillbox.db</Code>（同时删掉{' '}
          <Code>tillbox.db-wal</Code> 和 <Code>tillbox.db-shm</Code>
          ），再启动。备份里的密钥用主密钥加密，恢复时要用同一个主密钥（
          <Code>master.key</Code> 或 <Code>TILLBOX_MASTER_KEY</Code>），主密钥请另外保存。
        </Typography.Paragraph>
      </div>
      <DataTable
        label="备份文件"
        rows={b.files}
        rowKey={(f) => f.name}
        minWidth={520}
        empty="还没有备份"
        card={(f) => (
          <Stack
            main={<Mono>{f.name}</Mono>}
            sub={
              <>
                <span className="tb-num">{bytes(f.size)}</span> ·{' '}
                <span className="tb-num">{time(f.created_at, false)}</span>
              </>
            }
          />
        )}
        columns={[
          { title: '文件', render: (f) => <Mono>{f.name}</Mono> },
          {
            title: '大小',
            align: 'right',
            nowrap: true,
            render: (f) => <span className="tb-num">{bytes(f.size)}</span>,
          },
          { title: '备份时间', nowrap: true, render: (f) => <span className="tb-num">{time(f.created_at)}</span> },
        ]}
      />
    </>
  )
}

function BackupForm({ b, onSaved }: { b: Backup; onSaved: () => Promise<void> }) {
  const qc = useQueryClient()
  const [v, setV] = useState(b.config)
  const [busy, setBusy] = useState(false)
  const dirty = v.enabled !== b.config.enabled || v.keep !== b.config.keep
  const rev = useRevision(b.revision, !dirty)
  const save = async (overwrite = false) => {
    setBusy(true)
    try {
      const saved = await rev.run(
        async (revision) =>
          (await api.put<{ revision: number }>('/web-api/admin/operations/backup', v, { revision })).revision,
        overwrite,
      )
      if (saved) {
        await onSaved()
        toastOK('备份设置已保存')
      }
    } catch (err) {
      toastError(err)
    } finally {
      setBusy(false)
    }
  }
  return (
    <Panel title="备份设置" description="用 SQLite 的 VACUUM INTO 生成完整副本，校验通过后才算一份。">
      <Form
        onSubmit={(e) => {
          e.preventDefault()
          void save()
        }}
      >
        <div className="divide-y divide-separator">
          <SettingsRow label="自动备份" description="每天一份，失败后每小时重试；失败或超过 26 小时没成功会告警。">
            <SwitchInput
              label={v.enabled ? '已开启' : '已关闭'}
              value={v.enabled}
              onChange={(enabled) => setV((s) => ({ ...s, enabled }))}
            />
          </SettingsRow>
          <SettingsRow
            label="保留份数"
            description={
              <>
                超过的从最旧的开始删。
                <span className="mt-1 block text-xs">
                  默认 <span className="tb-num">7</span> 份，可设 <span className="tb-num">1–90</span> 份
                </span>
              </>
            }
          >
            <div className="flex items-center gap-3">
              <NumberInput
                ariaLabel="保留份数"
                className="w-40"
                isRequired
                minValue={1}
                maxValue={90}
                value={Number.isNaN(v.keep) ? undefined : v.keep}
                onChange={(n) => setV((s) => ({ ...s, keep: n ?? NaN }))}
              />
              <span className="text-sm text-muted">份</span>
            </div>
          </SettingsRow>
        </div>
        {rev.conflict && (
          <ConflictNotice
            what="备份设置"
            onReload={async () => setV((await qc.fetchQuery({ ...operationsQuery, staleTime: 0 })).backup.config)}
            onOverwrite={() => save(true)}
          />
        )}
        <div className="mt-5 flex justify-end gap-2">
          <Button size="sm" variant="tertiary" isDisabled={!dirty} onPress={() => setV(b.config)}>
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

function ArchiveSection({ d, onSaved }: { d: Operations; onSaved: () => Promise<void> }) {
  const now = useNow()
  const archive = useMutation({
    mutationFn: () => api.post('/web-api/admin/operations/archive'),
    onSuccess: async () => {
      await onSaved()
      toastOK('归档检查完成')
    },
    onError: toastError,
  })
  return (
    <>
      <div className="grid items-start gap-4 lg:grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)]">
        <RetentionForm d={d} onSaved={onSaved} />
        <Panel
          title="归档任务"
          extra={
            <Button
              size="sm"
              variant="secondary"
              isPending={archive.isPending}
              isDisabled={!d.retention.enabled}
              onPress={() => archive.mutate()}
            >
              立即执行归档
            </Button>
          }
        >
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
            <Typography.Paragraph size="xs" color="muted">
              订单、金额、流水编号和关联关系长期保留。只归档过了保留期且已完结的原始响应与通知历史，每笔订单至少保留最后一次通知；有待处理异常的记录继续在线保留。归档先落盘、校验并加密，再清理在线副本。
            </Typography.Paragraph>
          </div>
        </Panel>
      </div>
      <div className="mt-2 flex flex-col gap-1">
        <Typography.Heading level={3} className="text-sm">
          归档批次
        </Typography.Heading>
        <Typography.Paragraph size="sm" color="muted">
          最近 50 批。下载时会校验并解密为 JSONL.GZ，可能含账务信息，请妥善保管；服务器上的加密归档要配套 master.key
          才能恢复。
        </Typography.Paragraph>
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
    help: '支付宝和第三方平台返回的原始数据。流水本身（金额、编号、关联）不受影响。',
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

function RetentionForm({ d, onSaved }: { d: Operations; onSaved: () => Promise<void> }) {
  const qc = useQueryClient()
  const initial = d.retention
  const [v, setV] = useState(initial)
  const [busy, setBusy] = useState(false)
  const dirty = JSON.stringify(v) !== JSON.stringify(initial)
  const rev = useRevision(d.retention_revision, !dirty)
  const save = async (overwrite = false) => {
    setBusy(true)
    try {
      const saved = await rev.run(
        async (revision) =>
          (await api.put<{ revision: number }>('/web-api/admin/operations/retention', v, { revision })).revision,
        overwrite,
      )
      if (saved) {
        await onSaved()
        toastOK('保留期已保存')
      }
    } catch (err) {
      toastError(err)
    } finally {
      setBusy(false)
    }
  }
  return (
    <Panel title="在线保留期" description="超过保留期的记录会归档后从数据库移除。">
      <Form
        onSubmit={(e) => {
          e.preventDefault()
          void save()
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
        {rev.conflict && (
          <ConflictNotice
            what="在线保留期"
            onReload={async () => setV((await qc.fetchQuery({ ...operationsQuery, staleTime: 0 })).retention)}
            onOverwrite={() => save(true)}
          />
        )}
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
