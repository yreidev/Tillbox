import { useState, type ReactNode } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Button, Form } from '@heroui/react'
import { Copy } from '@gravity-ui/icons'
import { TelegramSettings } from '../../components/telegram-settings'
import { api, query } from '../../lib/api'
import { time } from '../../lib/format'
import type { AuditLog, Page, Payment, Settings } from '../../lib/types'
import {
  ConfirmDialog,
  copyText,
  DataTable,
  FilterTabs,
  Loading,
  Notice,
  NumberInput,
  PageHeader,
  Panel,
  Pill,
  SettingsRow,
  Stack,
  TextInput,
  toastError,
  toastOK,
  type Tone,
} from '../../lib/ui'

const tabs = ['payment', 'platform', 'site', 'notify', 'audit'] as const
type Tab = (typeof tabs)[number]

export const Route = createFileRoute('/admin/settings')({
  validateSearch: (s: Record<string, unknown>): { tab?: Tab } => ({
    tab: tabs.includes(s.tab as Tab) ? (s.tab as Tab) : undefined,
  }),
  component: SettingsPage,
})

const settingsQuery = { queryKey: ['settings'], queryFn: () => api.get<Settings>('/web-api/admin/settings') }

const tabText: Record<Tab, string> = {
  payment: '收款参数',
  platform: '平台密钥',
  site: '站点',
  notify: '管理员通知',
  audit: '审计日志',
}

function SettingsPage() {
  const { tab = 'payment' } = Route.useSearch()
  const navigate = useNavigate({ from: Route.fullPath })
  const q = useQuery(settingsQuery)
  const body = (render: (s: Settings) => ReactNode) => (q.data ? render(q.data) : q.isPending ? <Loading /> : null)
  return (
    <div className="flex flex-col gap-4">
      <PageHeader description="收款节奏、对接用的平台密钥、站点名称、发给管理员的 Telegram 通知，以及所有管理操作的记录。" />
      {q.isError && <Notice status="danger">{q.error.message}</Notice>}
      <FilterTabs<Tab>
        ariaLabel="设置分类"
        value={tab}
        onChange={(t) => void navigate({ search: { tab: t === 'payment' ? undefined : t } })}
        options={tabs.map((t) => ({ value: t, label: tabText[t] }))}
      >
        {tab === 'payment' && body((s) => <PaymentForm s={s} />)}
        {tab === 'platform' && body((s) => <Platform s={s} />)}
        {tab === 'site' && body((s) => <SiteForm s={s} />)}
        {tab === 'notify' && <TelegramSettings />}
        {tab === 'audit' && <Audit />}
      </FilterTabs>
    </div>
  )
}

interface Field {
  field: keyof Payment
  label: string
  help: ReactNode
  unit: '秒' | '分'
  min?: number
  max?: number
}

// 范围和 internal/settings/settings.go 的 Payment.Validate 一致。
const groups: { title: string; description: string; fields: Field[] }[] = [
  {
    title: '订单',
    description: '对所有渠道生效。',
    fields: [
      {
        field: 'order_ttl_seconds',
        label: '订单有效期',
        help: '下单后多久内付款有效，过期后订单关闭。',
        unit: '秒',
        min: 60,
        max: 1800,
      },
    ],
  },
  {
    title: '金额尾数',
    description: '只用于经营码渠道：靠应付金额的尾数区分同时待付的订单。',
    fields: [
      {
        field: 'max_tail_cents',
        label: '尾数上限',
        help: '应付金额 = 订单金额 + 0.01 元到这个上限。越大，能同时待付的同价订单越多。',
        unit: '分',
        min: 1,
        max: 99,
      },
      {
        field: 'cooldown_seconds',
        label: '金额冷却时间',
        help: '订单结束后，过多久这个应付金额才能再分给新订单，防止晚到的付款对错订单。',
        unit: '秒',
        min: 60,
        max: 3600,
      },
    ],
  },
  {
    title: '查账节奏',
    description: '只用于经营码渠道：多久查一次支付宝账单。查得太勤可能被支付宝限流。',
    fields: [
      {
        field: 'normal_interval',
        label: '常规查账间隔',
        help: '没有待付订单时多久查一次。',
        unit: '秒',
        min: 10,
        max: 3600,
      },
      {
        field: 'active_interval',
        label: '活跃查账间隔',
        help: '有待付订单时多久查一次；要不小于最小间隔、不大于常规间隔。',
        unit: '秒',
      },
      {
        field: 'min_interval',
        label: '最小查账间隔',
        help: '付款人点「我已付款」时最快多久查一次。',
        unit: '秒',
        min: 3,
        max: 300,
      },
      {
        field: 'safety_delay_second',
        label: '安全延迟',
        help: '每次只查到「现在减去这么多秒」为止，等支付宝的明细落库。',
        unit: '秒',
        min: 0,
        max: 120,
      },
    ],
  },
]

const allFields = groups.flatMap((g) => g.fields)

// human 把秒数换成更好读的说法，例如 300 秒 → 5 分钟。
function human(n: number | undefined, unit: Field['unit']) {
  if (!n || unit !== '秒' || n < 60) return ''
  if (n % 3600 === 0) return `= ${n / 3600} 小时`
  if (n % 60 === 0) return `= ${n / 60} 分钟`
  return `≈ ${(n / 60).toFixed(1)} 分钟`
}

function PaymentForm({ s }: { s: Settings }) {
  const qc = useQueryClient()
  const [busy, setBusy] = useState(false)
  const [v, setV] = useState<Payment>(s.payment)
  const dirty = allFields.some((f) => v[f.field] !== s.payment[f.field])
  const isDefault = allFields.every((f) => v[f.field] === s.defaults[f.field])
  return (
    <Form
      className="flex flex-col gap-4"
      onSubmit={async (e) => {
        e.preventDefault()
        setBusy(true)
        try {
          const body = Object.fromEntries(allFields.map((f) => [f.field, Number(v[f.field])]))
          await api.put('/web-api/admin/settings/payment', body)
          toastOK('已保存，新订单开始生效')
          await qc.invalidateQueries({ queryKey: ['settings'] })
        } catch (err) {
          toastError(err)
        } finally {
          setBusy(false)
        }
      }}
    >
      {groups.map((g) => (
        <Panel key={g.title} title={g.title} description={g.description}>
          <div className="divide-y divide-separator">
            {g.fields.map((f) => (
              <SettingsRow
                key={f.field}
                label={f.label}
                description={
                  <>
                    {f.help}
                    <span className="mt-1 block text-xs">
                      默认 <span className="tb-num">{s.defaults[f.field]}</span> {f.unit}
                      {f.min !== undefined && (
                        <>
                          ，可设{' '}
                          <span className="tb-num">
                            {f.min}–{f.max}
                          </span>{' '}
                          {f.unit}
                        </>
                      )}
                    </span>
                  </>
                }
              >
                <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                  <NumberInput
                    ariaLabel={f.label}
                    className="w-40"
                    minValue={f.min ?? 0}
                    maxValue={f.max}
                    value={Number.isNaN(v[f.field]) ? undefined : v[f.field]}
                    onChange={(n) => setV((x) => ({ ...x, [f.field]: n ?? NaN }))}
                  />
                  <span className="text-sm text-muted">{f.unit}</span>
                  <span className="tb-num text-xs text-muted">{human(v[f.field], f.unit)}</span>
                </div>
                {f.field === 'max_tail_cents' && v.max_tail_cents > 0 && (
                  <p className="text-xs text-muted">
                    例：10.00 元的订单，付款人要付{' '}
                    <span className="tb-num">10.01～10.{String(Math.min(v.max_tail_cents, 99)).padStart(2, '0')}</span>{' '}
                    元中的一个金额。
                  </p>
                )}
              </SettingsRow>
            ))}
          </div>
        </Panel>
      ))}
      <div className="sticky bottom-4 z-10 flex flex-wrap items-center gap-2 rounded-2xl bg-surface px-4 py-3 shadow-overlay">
        <span className="me-auto text-sm text-muted">
          {dirty ? '有未保存的修改，保存后对新订单生效。' : '修改后在这里保存。'}
        </span>
        <Button size="sm" variant="ghost" isDisabled={isDefault} onPress={() => setV(s.defaults)}>
          恢复默认值
        </Button>
        <Button size="sm" variant="tertiary" isDisabled={!dirty} onPress={() => setV(s.payment)}>
          撤销修改
        </Button>
        <Button size="sm" type="submit" isDisabled={!dirty} isPending={busy}>
          保存
        </Button>
      </div>
    </Form>
  )
}

function Platform({ s }: { s: Settings }) {
  const qc = useQueryClient()
  const [confirm, setConfirm] = useState(false)
  return (
    <>
      <Panel
        title="平台公钥"
        description="V2 接口（RSA 签名）用平台私钥给响应和通知签名，对接方用这个公钥验签。平台私钥加密存储，不会显示。"
      >
        <div className="flex flex-col gap-2">
          <pre className="tb-mono max-h-40 overflow-auto rounded-xl bg-default/60 px-3 py-2.5 text-xs break-all whitespace-pre-wrap">
            {s.platform_public_key}
          </pre>
          <div>
            <Button size="sm" variant="secondary" onPress={() => void copyText(s.platform_public_key)}>
              <Copy />
              复制公钥
            </Button>
          </div>
        </div>
      </Panel>
      <Panel title="危险操作" className="border border-danger/30">
        <SettingsRow
          label="重新生成平台密钥对"
          description="旧密钥立即作废。所有用 V2 接口的对接方都要换成新的平台公钥，否则验签会失败。"
        >
          <div>
            <Button variant="danger-soft" onPress={() => setConfirm(true)}>
              重新生成
            </Button>
          </div>
        </SettingsRow>
      </Panel>
      <ConfirmDialog
        open={confirm}
        onClose={() => setConfirm(false)}
        title="重新生成平台密钥对？"
        okText="重新生成"
        onConfirm={async () => {
          await api.post('/web-api/admin/settings/platform/regenerate')
          toastOK('已生成新的平台密钥对，记得通知用 V2 接口的对接方')
          await qc.invalidateQueries({ queryKey: ['settings'] })
        }}
      >
        <p>所有用 V2 接口的对接方都要换成新的平台公钥，否则验签会失败。只用 V1（MD5）的对接方不受影响。</p>
      </ConfirmDialog>
    </>
  )
}

function SiteForm({ s }: { s: Settings }) {
  const qc = useQueryClient()
  const [name, setName] = useState(s.site.name)
  const save = useMutation({
    mutationFn: () => api.put('/web-api/admin/settings/site', { name }),
    onSuccess: async () => {
      toastOK('已保存')
      await qc.invalidateQueries()
    },
    onError: toastError,
  })
  return (
    <Panel title="站点">
      <Form
        onSubmit={(e) => {
          e.preventDefault()
          save.mutate()
        }}
      >
        <SettingsRow label="站点名称" description="显示在后台左上角、浏览器标签和收银台标题上。">
          <TextInput
            ariaLabel="站点名称"
            isRequired
            maxLength={32}
            value={name}
            onChange={setName}
            validate={(x) => (x.trim() ? null : '请填写')}
          />
          <div>
            <Button size="sm" type="submit" isDisabled={name.trim() === s.site.name} isPending={save.isPending}>
              保存
            </Button>
          </div>
        </SettingsRow>
      </Form>
    </Panel>
  )
}

const actionText: Record<string, string> = {
  'auth.login': '登录',
  'auth.password': '修改密码',
  'auth.reset': '重置密码',
  'merchant.create': '新建商户',
  'merchant.update': '修改商户',
  'merchant.delete': '删除商户',
  'merchant.view_key': '查看商户密钥',
  'merchant.reset_key': '重置商户密钥',
  'order.close': '关闭订单',
  'ledger.link': '关联流水',
  'ledger.unlink': '撤销关联',
  'anomaly.resolve': '处理异常',
  'notify.resend': '重发通知',
  'settings.payment': '修改收款参数',
  'settings.platform_regenerate': '重新生成平台密钥',
  'channel.create': '新建渠道',
  'channel.update': '修改渠道',
  'channel.delete': '删除渠道',
  'channel.keypair': '生成渠道密钥对',
  'payment_test.create': '创建测试单',
  'retention.update': '修改保留期限',
  'archive.run': '归档',
  'archive.download': '下载归档',
  'assistance.config': '修改 AI 处理设置',
  'assistance.associate': '申诉关联订单',
  'assistance.retry': '申诉重新核查',
  'assistance.resolve': '申诉结案',
  'ledger.verified_link': 'AI 补认到账',
  'telegram.update': '修改管理员通知',
  'telegram.clear': '清除 Telegram 设置',
  'telegram.unbind': '解绑 Telegram 接收人',
}

// 删除、重置、撤销这类改变资金或凭据的操作标红，查看密钥标黄，方便扫一眼找出敏感操作。
function actionTone(a: string): Tone {
  if (/delete|reset|regenerate|unlink|unbind|clear|keypair/.test(a)) return 'danger'
  if (/view_key|download|link|close/.test(a)) return 'warning'
  return 'default'
}

const actionLabel = (a: string) => actionText[a] ?? (a.startsWith('assistance.') ? `AI 申诉处理（${a.slice(11)}）` : a)

function Audit() {
  const [page, setPage] = useState(1)
  const q = useQuery({
    queryKey: ['audit', page],
    queryFn: () => api.get<Page<AuditLog>>(`/web-api/admin/audit${query({ page, page_size: 20 })}`),
    placeholderData: keepPreviousData,
  })
  return (
    <>
      <p className="text-sm text-muted">后台和 AI 自动处理做过的每一项改动都记在这里，不能修改或删除。</p>
      {q.isError && <Notice status="danger">{q.error.message}</Notice>}
      <DataTable
        label="审计日志"
        rows={q.data?.items ?? []}
        rowKey={(l) => l.id}
        loading={q.isFetching && !q.data}
        minWidth={800}
        empty="还没有操作记录"
        pagination={{ page, pageSize: 20, total: q.data?.total ?? 0, onChange: setPage }}
        card={(l) => (
          <div className="flex flex-col gap-1.5">
            <div className="flex items-center justify-between gap-3">
              <Pill tone={actionTone(l.action)}>{actionLabel(l.action)}</Pill>
              <span className="tb-num text-xs text-muted">{time(l.created_at)}</span>
            </div>
            <span className="text-xs text-muted">
              {l.actor}
              {l.target && ` · ${l.target}`}
            </span>
            {l.detail && <p className="text-sm break-all">{l.detail}</p>}
          </div>
        )}
        columns={[
          { title: '时间', nowrap: true, render: (l) => <span className="tb-num">{time(l.created_at)}</span> },
          {
            title: '操作',
            nowrap: true,
            render: (l) => <Pill tone={actionTone(l.action)}>{actionLabel(l.action)}</Pill>,
          },
          {
            title: '操作人 / 对象',
            render: (l) => <Stack className="max-w-56" main={l.actor} sub={l.target || undefined} />,
          },
          {
            title: '说明',
            render: (l) => <span className="break-all">{l.detail || <span className="text-muted">—</span>}</span>,
          },
        ]}
      />
    </>
  )
}
