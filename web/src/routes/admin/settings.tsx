import { useMemo, useState, type ReactNode } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ActionBar, Button, Form, Link, Surface, Typography } from '@yreidev/ui'
import { ArrowsRotateRight, ArrowUpRightFromSquare, Copy, Eye, Shield } from '@gravity-ui/icons'
import { TelegramSettings } from '../../components/telegram-settings'
import { UpgradeGuide, updateQuery, type UpdateView } from '../../components/upgrade-guide'
import { api, query } from '../../lib/api'
import { time } from '../../lib/format'
import type { AuditLog, Home, HomeMode, Page, Payment, Settings } from '../../lib/types'
import {
  Code,
  ConfirmDialog,
  ConflictNotice,
  copyText,
  cx,
  enter,
  DataTable,
  Dialog,
  FilterTabs,
  Loading,
  Notice,
  NumberInput,
  PageHeader,
  Panel,
  Pill,
  RadioInput,
  SettingsRow,
  Stack,
  SwitchInput,
  TextAreaInput,
  TextInput,
  toastError,
  toastOK,
  useRevision,
  type Option,
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
      <PageHeader description="收款节奏、对接用的平台密钥、站点名称和首页、版本更新、发给管理员的 Telegram 通知，以及所有管理操作的记录。" />
      {q.isError && <Notice status="danger">{q.error.message}</Notice>}
      <FilterTabs<Tab>
        ariaLabel="设置分类"
        value={tab}
        onChange={(t) => void navigate({ search: { tab: t === 'payment' ? undefined : t } })}
        options={tabs.map((t) => ({ value: t, label: tabText[t] }))}
      >
        {tab === 'payment' && body((s) => <PaymentForm s={s} />)}
        {tab === 'platform' && body((s) => <Platform s={s} />)}
        {tab === 'site' &&
          body((s) => (
            <>
              <UpgradeGuide />
              <SiteForm s={s} />
              <HomeForm s={s} />
              <EntryPanel s={s} />
              <UpdatePanel />
            </>
          ))}
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

// useLatestSettings 重新读一次设置（不用缓存），给“载入最新”用。
function useLatestSettings() {
  const qc = useQueryClient()
  return () => qc.fetchQuery({ ...settingsQuery, staleTime: 0 })
}

function PaymentForm({ s }: { s: Settings }) {
  const qc = useQueryClient()
  const latest = useLatestSettings()
  const [busy, setBusy] = useState(false)
  const [v, setV] = useState<Payment>(s.payment)
  const dirty = allFields.some((f) => v[f.field] !== s.payment[f.field])
  const rev = useRevision(s.revisions.payment, !dirty)
  const isDefault = allFields.every((f) => v[f.field] === s.defaults[f.field])
  const save = async (overwrite = false) => {
    setBusy(true)
    try {
      const body = Object.fromEntries(allFields.map((f) => [f.field, Number(v[f.field])]))
      const saved = await rev.run(
        async (revision) =>
          (await api.put<{ revision: number }>('/web-api/admin/settings/payment', body, { revision })).revision,
        overwrite,
      )
      if (saved) {
        toastOK('已保存，新订单开始生效')
        await qc.invalidateQueries({ queryKey: ['settings'] })
      }
    } catch (err) {
      toastError(err)
    } finally {
      setBusy(false)
    }
  }
  return (
    <Form
      className={cx('flex flex-col gap-4', dirty && 'pb-16')}
      onSubmit={(e) => {
        e.preventDefault()
        void save()
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
                  <Typography.Paragraph size="xs" color="muted">
                    例：10.00 元的订单，付款人要付{' '}
                    <span className="tb-num">10.01～10.{String(Math.min(v.max_tail_cents, 99)).padStart(2, '0')}</span>{' '}
                    元中的一个金额。
                  </Typography.Paragraph>
                )}
              </SettingsRow>
            ))}
          </div>
        </Panel>
      ))}
      {rev.conflict && (
        <ConflictNotice
          what="收款参数"
          onReload={async () => setV((await latest()).payment)}
          onOverwrite={() => save(true)}
        />
      )}
      <div className="flex flex-wrap items-center justify-end gap-x-3 gap-y-2">
        <Typography.Paragraph size="sm" color="muted">
          拿不准怎么设时可以恢复默认值，恢复后要保存才生效。
        </Typography.Paragraph>
        <Button size="sm" variant="secondary" isDisabled={isDefault} onPress={() => setV(s.defaults)}>
          恢复默认值
        </Button>
      </div>
      {/* 改过才浮出底部的保存条；手机上只留按钮，提示文字留给屏幕阅读器。 */}
      <ActionBar isOpen={dirty || busy} aria-label="保存收款参数">
        <ActionBar.Prefix>
          <span className="action-bar__label">有未保存的修改，保存后对新订单生效</span>
        </ActionBar.Prefix>
        <ActionBar.Content>
          <Button size="sm" variant="tertiary" isDisabled={busy} onPress={() => setV(s.payment)}>
            撤销修改
          </Button>
          <Button size="sm" type="submit" isPending={busy}>
            保存
          </Button>
        </ActionBar.Content>
      </ActionBar>
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
          <Surface variant="secondary" className="max-h-40 overflow-auto rounded-xl px-3 py-2.5">
            <pre className="tb-mono text-xs break-all whitespace-pre-wrap">{s.platform_public_key}</pre>
          </Surface>
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
        <Typography.Paragraph size="sm" color="muted">
          所有用 V2 接口的对接方都要换成新的平台公钥，否则验签会失败。只用 V1（MD5）的对接方不受影响。
        </Typography.Paragraph>
      </ConfirmDialog>
    </>
  )
}

function SiteForm({ s }: { s: Settings }) {
  const qc = useQueryClient()
  const latest = useLatestSettings()
  const [name, setName] = useState(s.site.name)
  const rev = useRevision(s.revisions.site, name.trim() === s.site.name)
  const save = useMutation({
    mutationFn: (overwrite: boolean) =>
      rev.run(
        async (revision) =>
          (await api.put<{ revision: number }>('/web-api/admin/settings/site', { name }, { revision })).revision,
        overwrite,
      ),
    onSuccess: async (saved) => {
      if (!saved) return
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
          save.mutate(false)
        }}
      >
        <SettingsRow
          label="站点名称"
          description="显示在后台左上角、登录页、浏览器标签、收银台标题上，也是添加到桌面时的应用名。"
        >
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
        {rev.conflict && (
          <ConflictNotice
            what="站点名称"
            onReload={async () => setName((await latest()).site.name)}
            onOverwrite={() => save.mutateAsync(true)}
          />
        )}
      </Form>
    </Panel>
  )
}

// 和 internal/settings/home.go 的 MaxHomeHTML 一致。
const maxHomeHTML = 512 * 1024

const homeOptions: Option<HomeMode>[] = [
  { value: 'help', label: '付款人自助页', description: '默认。付款人在这里查订单、提交申诉。' },
  { value: 'page', label: '自定义页面', description: '贴一整段 HTML，打开域名就显示它。' },
  { value: 'redirect', label: '跳转到其他网址', description: '打开域名直接跳到你填的网址。' },
  { value: 'not_found', label: '404', description: '和不存在的路径一样，只返回“404 page not found”。' },
]

const kb = (n: number) => `${(n / 1024).toFixed(n < 10240 ? 1 : 0)} KB`

function HomeForm({ s }: { s: Settings }) {
  const qc = useQueryClient()
  const latest = useLatestSettings()
  const [v, setV] = useState<Home>(s.home)
  const [preview, setPreview] = useState(false)
  const size = useMemo(() => new TextEncoder().encode(v.html).length, [v.html])
  const dirty = v.mode !== s.home.mode || v.html !== s.home.html || v.url.trim() !== s.home.url
  const rev = useRevision(s.revisions.home, !dirty)
  const set = (x: Partial<Home>) => setV((o) => ({ ...o, ...x }))
  const save = useMutation({
    mutationFn: (overwrite: boolean) =>
      rev.run(
        async (revision) =>
          (await api.put<{ revision: number }>('/web-api/admin/settings/home', v, { revision })).revision,
        overwrite,
      ),
    onSuccess: async (saved) => {
      if (!saved) return
      toastOK('已保存，首页立即生效')
      await qc.invalidateQueries({ queryKey: ['settings'] })
    },
    onError: toastError,
  })
  return (
    <Panel
      title="首页"
      description="直接打开域名时看到的页面。收银台、付款人自助页（/help）、易支付接口和后台都不受影响。"
      extra={
        <Button size="sm" variant="ghost" onPress={() => window.open('/', '_blank', 'noopener,noreferrer')}>
          <ArrowUpRightFromSquare />
          打开首页
        </Button>
      }
    >
      <Form
        className="flex flex-col"
        onSubmit={(e) => {
          e.preventDefault()
          save.mutate(false)
        }}
      >
        <div className="divide-y divide-separator">
          <SettingsRow label="显示" description="切换后，另外几项填过的内容会保留，换回来不用重填。">
            <RadioInput ariaLabel="首页显示" value={v.mode} onChange={(mode) => set({ mode })} options={homeOptions} />
          </SettingsRow>
          {v.mode === 'page' && (
            <SettingsRow
              className={enter}
              label="页面 HTML"
              description="原样返回给浏览器，最多 512 KB。图片、样式可以写在页面里或用外部链接。页面里的脚本在隔离环境中运行，读不到本站的 cookie 和本地存储，也调不了后台接口。"
            >
              <TextAreaInput
                ariaLabel="页面 HTML"
                isRequired
                mono
                rows={14}
                value={v.html}
                onChange={(html) => set({ html })}
                placeholder="<!doctype html>"
                validate={(x) =>
                  !x.trim() ? '请填写' : new TextEncoder().encode(x).length > maxHomeHTML ? '超过 512 KB' : null
                }
                description={
                  <span className={cx('tb-num', size > maxHomeHTML && 'text-danger')}>{kb(size)} / 512 KB</span>
                }
              />
              <div>
                <Button size="sm" variant="secondary" isDisabled={!v.html.trim()} onPress={() => setPreview(true)}>
                  <Eye />
                  预览
                </Button>
              </div>
            </SettingsRow>
          )}
          {v.mode === 'redirect' && (
            <SettingsRow
              className={enter}
              label="跳转网址"
              description="用临时跳转（302），浏览器不会记住，改回别的首页后立即生效。"
            >
              <TextInput
                ariaLabel="跳转网址"
                isRequired
                mono
                type="url"
                inputMode="url"
                maxLength={2048}
                placeholder="https://example.com"
                value={v.url}
                onChange={(url) => set({ url })}
                validate={(x) =>
                  /^https?:\/\/[^/\s]+/i.test(x.trim()) ? null : '要是 http:// 或 https:// 开头的完整网址'
                }
              />
            </SettingsRow>
          )}
        </div>
        {rev.conflict && (
          <ConflictNotice
            what="首页设置"
            onReload={async () => setV((await latest()).home)}
            onOverwrite={() => save.mutateAsync(true)}
          />
        )}
        <div className="mt-5 flex flex-wrap items-center gap-2 border-t border-separator pt-4">
          <span className="me-auto text-sm text-muted">{dirty ? '有未保存的修改。' : '保存后立即生效。'}</span>
          <Button size="sm" variant="tertiary" isDisabled={!dirty} onPress={() => setV(s.home)}>
            撤销修改
          </Button>
          <Button size="sm" type="submit" isDisabled={!dirty} isPending={save.isPending}>
            保存
          </Button>
        </div>
      </Form>
      <Dialog title="预览自定义首页" size="lg" open={preview} onClose={() => setPreview(false)}>
        <iframe
          title="自定义首页预览"
          sandbox="allow-scripts allow-forms allow-popups allow-modals"
          srcDoc={v.html}
          className="h-[65svh] w-full rounded-xl border border-separator bg-white"
        />
      </Dialog>
    </Panel>
  )
}

type EntryAction = 'on' | 'reset' | 'off'

const entryConfirm: Record<
  EntryAction,
  { title: string; ok: string; status: 'warning' | 'danger'; body: ReactNode; done: string }
> = {
  on: {
    title: '开启后台安全入口？',
    ok: '开启',
    status: 'warning',
    body: (
      <Typography.Paragraph size="sm" color="muted">
        当前浏览器自动放行。其他设备和浏览器要先打开入口链接才看得到登录页，请把链接存进密码管理器。忘了也能在服务器上运行{' '}
        <Code>tillbox entry</Code> 找回。
      </Typography.Paragraph>
    ),
    done: '已开启，请把入口链接存好',
  },
  reset: {
    title: '换一个入口链接？',
    ok: '换一个',
    status: 'warning',
    body: (
      <Typography.Paragraph size="sm" color="muted">
        旧链接立即失效，已经放行的其他浏览器也要重新打开新链接。当前浏览器自动放行。
      </Typography.Paragraph>
    ),
    done: '已换成新的入口链接，旧链接失效',
  },
  off: {
    title: '关闭后台安全入口？',
    ok: '关闭',
    status: 'danger',
    body: (
      <Typography.Paragraph size="sm" color="muted">
        关闭后，任何人都能打开登录页和后台接口（仍然要密码才能登录）。
      </Typography.Paragraph>
    ),
    done: '已关闭后台安全入口',
  },
}

function EntryPanel({ s }: { s: Settings }) {
  const qc = useQueryClient()
  // 关闭动画期间对话框里的文字还要在，所以记着上一次的操作，另用 open 控制显示
  const [action, setAction] = useState<EntryAction>('on')
  const [open, setOpen] = useState(false)
  const ask = (a: EntryAction) => {
    setAction(a)
    setOpen(true)
  }
  const e = s.entry
  const c = entryConfirm[action]
  return (
    <Panel
      title="后台安全入口"
      description="开启后，只有打开过入口链接的浏览器才看得到后台和登录页；其他人访问后台、登录页和后台接口，得到的和不存在的路径一样是 404。"
    >
      {e.enabled ? (
        <div className={cx('divide-y divide-separator', enter)}>
          <SettingsRow
            label="入口链接"
            description="在新设备或新浏览器上先打开一次，这个浏览器之后 400 天内不用再开。请存进密码管理器，不要发给别人。"
          >
            <TextInput
              ariaLabel="入口链接"
              isReadOnly
              mono
              value={e.url}
              onChange={() => {}}
              suffix={
                <Button
                  isIconOnly
                  size="sm"
                  variant="ghost"
                  aria-label="复制入口链接"
                  onPress={() => void copyText(e.url)}
                >
                  <Copy />
                </Button>
              }
            />
            <div className="flex flex-col gap-1">
              <Typography.Paragraph size="xs" color="muted">
                忘了链接：在服务器上运行 <Code>tillbox entry</Code> 查看。
              </Typography.Paragraph>
              <Typography.Paragraph size="xs" color="muted">
                从 Telegram 通知的按钮打开后台看到 404：Telegram 自带的浏览器也要打开一次入口链接，或者让 Telegram
                用系统浏览器打开链接。
              </Typography.Paragraph>
            </div>
          </SettingsRow>
          <SettingsRow label="换一个链接" description="链接泄露时用。旧链接和已经放行的浏览器全部失效。">
            <div>
              <Button variant="secondary" onPress={() => ask('reset')}>
                <ArrowsRotateRight />
                换一个链接
              </Button>
            </div>
          </SettingsRow>
          <SettingsRow label="关闭安全入口" description="关闭后任何人都能打开登录页。">
            <div>
              <Button variant="danger-soft" onPress={() => ask('off')}>
                关闭
              </Button>
            </div>
          </SettingsRow>
        </div>
      ) : (
        <SettingsRow
          className={enter}
          label="未开启"
          description="现在任何人都能打开登录页。开启后当前浏览器自动放行，别的设备要先打开入口链接。"
        >
          <div>
            <Button onPress={() => ask('on')}>
              <Shield />
              开启
            </Button>
          </div>
        </SettingsRow>
      )}
      <ConfirmDialog
        open={open}
        onClose={() => setOpen(false)}
        title={c.title}
        okText={c.ok}
        status={c.status}
        onConfirm={async () => {
          if (action === 'off') await api.del('/web-api/admin/settings/entry')
          else await api.post('/web-api/admin/settings/entry')
          toastOK(c.done)
          await qc.invalidateQueries({ queryKey: ['settings'] })
        }}
      >
        {c.body}
      </ConfirmDialog>
    </Panel>
  )
}

// UpdatePanel 是版本和新版本提醒。提醒开关改了马上保存。
function UpdatePanel() {
  const qc = useQueryClient()
  const q = useQuery(updateQuery)
  const u = q.data
  return (
    <Panel
      title="版本"
      description="新版本提醒每 12 小时向 GitHub（api.github.com）查一次 Tillbox 的正式版，只读公开的版本列表，不发送本站的任何信息；GitHub 能看到服务器的出口 IP。"
    >
      {q.isError && <Notice status="danger">{q.error.message}</Notice>}
      {q.isPending && <Loading />}
      {u && (
        <UpdateBody
          key={u.revision}
          u={u}
          onChange={async (v) => {
            qc.setQueryData(updateQuery.queryKey, v)
            await qc.invalidateQueries({ queryKey: ['me'] })
          }}
        />
      )}
    </Panel>
  )
}

function UpdateBody({ u, onChange }: { u: UpdateView; onChange: (v: UpdateView) => Promise<void> }) {
  const qc = useQueryClient()
  const [check, setCheck] = useState(u.check)
  const rev = useRevision(u.revision, check === u.check)
  const save = async (next: boolean, overwrite = false) => {
    setCheck(next)
    let view: UpdateView | undefined
    try {
      const saved = await rev.run(async (revision) => {
        view = await api.put<UpdateView>('/web-api/admin/settings/update', { check: next }, { revision })
        return view.revision
      }, overwrite)
      if (saved && view) {
        toastOK(next ? '已开启新版本提醒' : '已关闭新版本提醒')
        await onChange(view)
      }
    } catch (err) {
      setCheck(u.check)
      toastError(err)
    }
  }
  const run = useMutation({
    mutationFn: () => api.post<UpdateView>('/web-api/admin/update/check'),
    onSuccess: async (v) => {
      toastOK(v.newer ? `有新版本 ${v.latest}` : '已经是最新版本')
      await onChange(v)
    },
    onError: async (err) => {
      toastError(err)
      await qc.invalidateQueries({ queryKey: updateQuery.queryKey })
    },
  })
  const status: { tone: Tone; text: string } =
    u.current === 'dev'
      ? { tone: 'default', text: '开发构建，不和正式版比较' }
      : u.newer
        ? { tone: 'warning', text: `有新版本 ${u.latest}` }
        : u.latest
          ? { tone: 'success', text: '已是最新' }
          : { tone: 'default', text: '还没检查过' }
  return (
    <div className="divide-y divide-separator">
      <SettingsRow label="当前版本">
        <div className="flex flex-wrap items-center gap-2">
          <span className="tb-num text-sm font-medium">{u.current}</span>
          <Pill tone={status.tone}>{status.text}</Pill>
          {u.notes_url && (
            <Link href={u.notes_url} target="_blank" rel="noreferrer" className="text-sm">
              {u.latest} 的更新说明
              <Link.Icon />
            </Link>
          )}
        </div>
      </SettingsRow>
      <SettingsRow label="新版本提醒" description="有新的正式版时在侧栏底部提示。关掉后不再自动检查，可以手动检查。">
        <SwitchInput
          label={check ? '已开启' : '已关闭'}
          value={check}
          isDisabled={rev.conflict}
          onChange={(v) => void save(v)}
        />
        {rev.conflict && (
          <ConflictNotice what="新版本提醒" onReload={() => setCheck(u.check)} onOverwrite={() => save(check, true)} />
        )}
      </SettingsRow>
      <SettingsRow
        label="上次检查"
        description={
          u.error
            ? undefined
            : u.newer
              ? `升级步骤见本页最上面的“升级到 ${u.latest}”。`
              : '有新版本时，本页最上面会列出升级步骤。'
        }
      >
        <div className="flex flex-wrap items-center gap-3">
          <span className="tb-num text-sm">{time(u.checked_at) || '还没检查过'}</span>
          <Button size="sm" variant="secondary" isPending={run.isPending} onPress={() => run.mutate()}>
            <ArrowsRotateRight />
            现在检查
          </Button>
        </div>
        {u.error && <Notice status="warning">上次检查失败：{u.error}</Notice>}
      </SettingsRow>
    </div>
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
  'settings.home': '修改首页',
  'settings.entry': '后台安全入口',
  'settings.entry_off': '关闭后台安全入口',
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

// 删除、重置、撤销这类改变资金或凭据的操作标红，查看密钥、改后台入口标黄，方便扫一眼找出敏感操作。
function actionTone(a: string): Tone {
  if (/delete|reset|regenerate|unlink|unbind|clear|keypair|entry_off/.test(a)) return 'danger'
  if (/view_key|download|link|close|entry/.test(a)) return 'warning'
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
      <Typography.Paragraph size="sm" color="muted">
        后台和 AI 自动处理做过的每一项改动都记在这里，不能修改或删除。
      </Typography.Paragraph>
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
            {l.detail && (
              <Typography.Paragraph size="sm" className="break-all">
                {l.detail}
              </Typography.Paragraph>
            )}
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
