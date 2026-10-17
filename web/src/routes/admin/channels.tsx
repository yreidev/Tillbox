import { useId, useRef, useState, type ComponentType, type ReactNode, type SVGProps } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Accordion,
  Button,
  Card,
  Checkbox,
  CheckboxGroup,
  ComboBox,
  Description,
  Drawer,
  Dropdown,
  Form,
  Input,
  Label,
  ListBox,
  Skeleton,
  Switch,
  toast,
} from '@heroui/react'
import {
  ChevronDown,
  CreditCard,
  Ellipsis,
  Flask,
  Globe,
  Pencil,
  Picture,
  PlugConnection,
  Plus,
  QrCode,
  TrashBin,
} from '@gravity-ui/icons'
import { api, channelsQuery, meQuery } from '../../lib/api'
import { relative } from '../../lib/format'
import { useNow } from '../../lib/now'
import { decodeQRImage } from '../../lib/qrdecode'
import type { AlipayQRConfig, ChannelType, ChannelView, UpstreamConfig } from '../../lib/types'
import { channelTypeText, ledgerErrorText, payTypeLabel, payTypeText } from '../../lib/types'
import {
  Code,
  Confirm,
  ConfirmDialog,
  Copyable,
  Notice,
  PageHeader,
  Pill,
  RadioInput,
  Section,
  StatusDot,
  SwitchInput,
  TextAreaInput,
  TextInput,
  cx,
  stepDot,
  toastError,
  toastOK,
  type StepStatus,
  type Tone,
} from '../../lib/ui'

export const Route = createFileRoute('/admin/channels')({ component: ChannelsPage })

type Icon = ComponentType<SVGProps<SVGSVGElement>>
type Editing = { id: number } | { type: ChannelType } | null

const typeMeta: Record<ChannelType, { icon: Icon; blurb: string }> = {
  alipay_qr: { icon: QrCode, blurb: '用自己的支付宝经营码收款：查支付宝账单，按应付金额的尾数认单。' },
  epay: { icon: Globe, blurb: '把订单转给别家易支付收款，由上游的回调和查单确认到账，按原价付款。' },
  demo: { icon: Flask, blurb: '本地演示用，不连支付宝，用开发接口模拟付款。' },
}

interface Base {
  name: string
  remark: string
  enabled: boolean
}

// inputOf 把列表里的渠道还原成修改接口要的输入（密钥留空表示不改），卡片上直接启用、停用时用。
function inputOf(c: ChannelView, enabled: boolean) {
  let config: Record<string, unknown> = {}
  if (c.type === 'alipay_qr') {
    const a = c.config as AlipayQRConfig
    config = {
      app_id: a.app_id,
      gateway: a.gateway,
      alipay_public_key: a.alipay_public_key,
      qrcode_url: a.qrcode_url,
      payee_name: a.payee_name,
    }
  } else if (c.type === 'epay') {
    const u = c.config as UpstreamConfig
    config = {
      api_url: u.api_url,
      version: u.version,
      pid: u.pid,
      platform_public_key: u.platform_public_key,
      types: u.types,
    }
  }
  return { type: c.type, name: c.name, enabled, remark: c.remark, config }
}

interface TestResult {
  ok: boolean
  message: string
}

function ChannelsPage() {
  const qc = useQueryClient()
  const q = useQuery({ ...channelsQuery, refetchInterval: 15_000 })
  const [editing, setEditing] = useState<Editing>(null)
  const [removing, setRemoving] = useState<ChannelView | null>(null)
  const refresh = () => qc.invalidateQueries({ queryKey: ['channels'] })
  const toggle = useMutation({
    mutationFn: ({ c, enabled }: { c: ChannelView; enabled: boolean }) =>
      api.put(`/web-api/admin/channels/${c.id}`, inputOf(c, enabled)),
    onSuccess: (_d, v) => {
      toastOK(v.enabled ? '已启用' : '已停用')
      void refresh()
    },
    onError: toastError,
  })
  const test = useMutation({
    mutationFn: (c: ChannelView) => api.post<TestResult>(`/web-api/admin/channels/${c.id}/test`),
    onSuccess: (r) => (r.ok ? toast.success(r.message, { timeout: 3000 }) : toast.danger(r.message, { timeout: 8000 })),
    onError: toastError,
  })
  const types: ChannelType[] = q.data?.demo ? ['alipay_qr', 'epay', 'demo'] : ['alipay_qr', 'epay']
  const items = q.data?.items ?? []

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        description="商户在商户页选用一个渠道收款，改渠道只影响之后的新订单。"
        actions={items.length > 0 && <NewChannel types={types} onPick={(type) => setEditing({ type })} />}
      />
      {q.isError && <Notice status="danger">{q.error.message}</Notice>}
      {q.isPending ? (
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
          {[0, 1].map((i) => (
            <Skeleton key={i} className="h-72 rounded-2xl" />
          ))}
        </div>
      ) : items.length === 0 ? (
        <Onboarding types={types} onPick={(type) => setEditing({ type })} />
      ) : (
        <div className="grid items-stretch gap-4 md:grid-cols-2 xl:grid-cols-3">
          {items.map((c) => (
            <ChannelCard
              key={c.id}
              c={c}
              onEdit={() => setEditing({ id: c.id })}
              onRemove={() => setRemoving(c)}
              toggling={toggle.isPending && toggle.variables?.c.id === c.id}
              onToggle={(enabled) => toggle.mutate({ c, enabled })}
              testing={test.isPending && test.variables?.id === c.id}
              onTest={() => test.mutate(c)}
            />
          ))}
        </div>
      )}
      <Editor
        editing={editing}
        channels={items}
        onClose={() => setEditing(null)}
        onCreated={(id) => setEditing({ id })}
      />
      <ConfirmDialog
        open={removing !== null}
        onClose={() => setRemoving(null)}
        title={`删除渠道「${removing?.name ?? ''}」？`}
        okText="删除渠道"
        onConfirm={async () => {
          await api.del(`/web-api/admin/channels/${removing!.id}`)
          toastOK('已删除')
          void refresh()
        }}
      >
        <p>配置和密钥一起删除，不能恢复。</p>
      </ConfirmDialog>
    </div>
  )
}

function NewChannel({ types, onPick }: { types: ChannelType[]; onPick: (t: ChannelType) => void }) {
  return (
    <Dropdown>
      <Button size="sm">
        <Plus />
        新建渠道
      </Button>
      <Dropdown.Popover placement="bottom end" className="w-80">
        <Dropdown.Menu aria-label="渠道类型" onAction={(k) => onPick(k as ChannelType)}>
          {types.map((t) => {
            const M = typeMeta[t]
            return (
              <Dropdown.Item key={t} id={t} textValue={channelTypeText[t]}>
                <div className="flex h-8 items-start justify-center pt-px">
                  <M.icon className="size-4 shrink-0 text-muted" />
                </div>
                <div className="flex flex-col">
                  <Label>{channelTypeText[t]}</Label>
                  <Description className="whitespace-normal">{M.blurb}</Description>
                </div>
              </Dropdown.Item>
            )
          })}
        </Dropdown.Menu>
      </Dropdown.Popover>
    </Dropdown>
  )
}

// Onboarding 是还没有渠道时的引导：直接选一种渠道开始。
function Onboarding({ types, onPick }: { types: ChannelType[]; onPick: (t: ChannelType) => void }) {
  return (
    <Card className="items-center gap-6 rounded-2xl px-4 py-10 text-center sm:px-6">
      <span className="grid size-12 place-items-center rounded-2xl bg-accent-soft text-accent-soft-foreground">
        <CreditCard className="size-6" />
      </span>
      <div>
        <h2 className="text-lg font-semibold">先添加一个收款渠道</h2>
        <p className="mt-1 text-sm text-muted">商户下单要有可用的渠道才能收款。选一种开始：</p>
      </div>
      <div className={cx('grid w-full max-w-3xl gap-3', types.length > 2 ? 'sm:grid-cols-3' : 'sm:grid-cols-2')}>
        {types.map((t) => {
          const M = typeMeta[t]
          return (
            <button
              key={t}
              type="button"
              onClick={() => onPick(t)}
              className="flex flex-col items-start gap-2 rounded-2xl border border-separator p-4 text-left outline-none hover:bg-default focus-visible:ring-2 focus-visible:ring-focus"
            >
              <M.icon className="size-5 text-accent" />
              <span className="font-medium">{channelTypeText[t]}</span>
              <span className="text-sm text-muted">{M.blurb}</span>
            </button>
          )
        })}
      </div>
    </Card>
  )
}

function ChannelCard({
  c,
  onEdit,
  onRemove,
  toggling,
  onToggle,
  testing,
  onTest,
}: {
  c: ChannelView
  onEdit: () => void
  onRemove: () => void
  toggling: boolean
  onToggle: (enabled: boolean) => void
  testing: boolean
  onTest: () => void
}) {
  const M = typeMeta[c.type]
  const state: { tone: Tone; text: string } = !c.enabled
    ? { tone: 'default', text: '已停用' }
    : c.problem
      ? { tone: 'danger', text: '不能收款' }
      : { tone: 'success', text: '可以收款' }
  return (
    <Card className="gap-4 rounded-2xl">
      <div className="flex items-start gap-3">
        <span
          className={cx(
            'grid size-10 shrink-0 place-items-center rounded-xl [&>svg]:size-5',
            c.enabled ? 'bg-accent-soft text-accent-soft-foreground' : 'bg-default text-muted',
          )}
        >
          <M.icon />
        </span>
        <div className="min-w-0 flex-1">
          <h2 className="truncate font-semibold" title={c.name}>
            {c.name}
          </h2>
          <p className="text-xs text-muted">
            {channelTypeText[c.type] ?? c.type} · 编号 {c.id}
          </p>
        </div>
        <Switch
          size="sm"
          isSelected={c.enabled}
          aria-label={`启用渠道 ${c.name}`}
          isDisabled={toggling}
          onChange={onToggle}
        >
          <Switch.Content>
            <Switch.Control>
              <Switch.Thumb />
            </Switch.Control>
          </Switch.Content>
        </Switch>
      </div>
      <div className="flex flex-col gap-1.5">
        <StatusDot tone={state.tone}>
          <span className="font-medium">{state.text}</span>
        </StatusDot>
        {c.enabled && c.problem && <p className="text-xs text-danger">{c.problem}</p>}
        <Health c={c} />
      </div>
      {!!c.pay_types?.length && (
        <div className="flex flex-wrap gap-1">
          {c.pay_types.map((t) => (
            <Pill key={t} tone="accent">
              {payTypeLabel(t)}
            </Pill>
          ))}
        </div>
      )}
      <dl className="grid grid-cols-3 rounded-xl bg-default/60 py-2.5 text-center">
        {(
          [
            ['商户', c.merchants],
            ['订单', c.orders],
            ['流水', c.transactions],
          ] as const
        ).map(([k, n]) => (
          <div key={k}>
            <dt className="text-xs text-muted">{k}</dt>
            <dd className="tb-num text-sm font-medium">{n}</dd>
          </div>
        ))}
      </dl>
      {c.remark && <p className="line-clamp-2 text-xs text-muted">{c.remark}</p>}
      <div className="mt-auto flex items-center gap-2">
        <Button size="sm" variant="secondary" onPress={onEdit}>
          <Pencil />
          编辑
        </Button>
        {c.type !== 'demo' && (
          <Button size="sm" variant="ghost" isPending={testing} onPress={onTest}>
            <PlugConnection />
            测试连接
          </Button>
        )}
        <Dropdown>
          <Button
            size="sm"
            variant="ghost"
            isIconOnly
            aria-label={`${c.name} 的更多操作`}
            className="ms-auto text-muted"
          >
            <Ellipsis />
          </Button>
          <Dropdown.Popover placement="bottom end" className="w-64">
            <Dropdown.Menu
              aria-label="更多操作"
              disabledKeys={c.delete_block ? ['delete'] : []}
              onAction={(k) => k === 'delete' && onRemove()}
            >
              <Dropdown.Item id="delete" textValue="删除渠道" variant="danger">
                <div className="flex h-8 items-start justify-center pt-px">
                  <TrashBin className="size-4 shrink-0 text-danger" />
                </div>
                <div className="flex flex-col">
                  <Label>删除渠道</Label>
                  <Description className="whitespace-normal">
                    {c.delete_block || '配置和密钥一起删除，不能恢复'}
                  </Description>
                </div>
              </Dropdown.Item>
            </Dropdown.Menu>
          </Dropdown.Popover>
        </Dropdown>
      </div>
    </Card>
  )
}

// Health 是渠道的运行情况：经营码看查账，上游看查单和回调。
function Health({ c }: { c: ChannelView }) {
  const now = useNow()
  const lines: { text: string; tone?: 'danger' | 'warning' }[] = []
  if (c.ledger && c.enabled) {
    const l = c.ledger
    if (l.last_error) lines.push({ tone: l.failures > 0 ? 'danger' : 'warning', text: `查账${ledgerErrorText(l)}` })
    else lines.push({ text: l.last_ok_at ? `${relative(l.last_ok_at, now)}查账成功` : '还没查过账' })
    if (l.backlog_segments > 0) lines.push({ text: `历史补扫剩余 ${l.backlog_segments} 段` })
  } else if (c.upstream) {
    const u = c.upstream
    if (u.failures > 0) {
      const err = u.check_err_at > u.prepay_err_at ? `查单：${u.check_err}` : `预下单：${u.prepay_err}`
      lines.push({ tone: u.failures >= 3 ? 'danger' : 'warning', text: `连续失败 ${u.failures} 次，${err}` })
    } else {
      lines.push({ text: u.check_ok_at ? `${relative(u.check_ok_at, now)}查单成功` : '本次启动后还没查过单' })
    }
    if (u.notify_err_at > u.notify_at)
      lines.push({ tone: 'warning', text: `${relative(u.notify_err_at, now)}收到不合格的回调：${u.notify_err}` })
    else if (u.notify_at) lines.push({ text: `${relative(u.notify_at, now)}收到回调` })
  } else if (c.type === 'demo') {
    lines.push({ text: '用 /web-api/dev/pay 模拟付款' })
  }
  return (
    <>
      {lines.map((l, i) => (
        <p
          key={i}
          className={cx(
            'text-xs',
            l.tone === 'danger' ? 'text-danger' : l.tone === 'warning' ? 'text-warning' : 'text-muted',
          )}
        >
          {l.text}
        </p>
      ))}
    </>
  )
}

function Editor({
  editing,
  channels,
  onClose,
  onCreated,
}: {
  editing: Editing
  channels: ChannelView[]
  onClose: () => void
  onCreated: (id: number) => void
}) {
  const c = editing && 'id' in editing ? channels.find((x) => x.id === editing.id) : undefined
  const type = c?.type ?? (editing && 'type' in editing ? editing.type : undefined)
  const props = { c, onCreated }
  return (
    <Drawer.Backdrop isOpen={editing !== null} onOpenChange={(v) => !v && onClose()}>
      <Drawer.Content placement="right">
        <Drawer.Dialog className="w-[min(640px,100vw)] max-w-[100vw] p-0 sm:w-[min(640px,100vw)]">
          <Drawer.CloseTrigger />
          <Drawer.Header className="gap-0.5 border-b border-separator px-5 pt-5 pb-4 sm:px-6">
            <p className="text-sm text-muted">
              {c ? '编辑渠道' : '新建渠道'}
              {type && ` · ${channelTypeText[type]}`}
            </p>
            <Drawer.Heading className="truncate pr-10 text-lg font-semibold">
              {c?.name ?? `新的${type ? channelTypeText[type] : ''}渠道`}
            </Drawer.Heading>
          </Drawer.Header>
          {type === 'alipay_qr' && <AlipayForm key={c?.id ?? 'new'} {...props} />}
          {type === 'epay' && <UpstreamForm key={c?.id ?? 'new'} {...props} />}
          {type === 'demo' && <DemoForm key={c?.id ?? 'new'} {...props} />}
        </Drawer.Dialog>
      </Drawer.Content>
    </Drawer.Backdrop>
  )
}

// useSave 保存渠道。新建成功后切到编辑这个渠道（之后才能生成密钥对、测试连接）。
function useSave(c: ChannelView | undefined, type: ChannelType, onCreated: (id: number) => void) {
  const qc = useQueryClient()
  const [busy, setBusy] = useState(false)
  const save = async (base: Base, config: Record<string, unknown>) => {
    setBusy(true)
    try {
      const body = { type, name: base.name, remark: base.remark, enabled: base.enabled, config }
      if (c) {
        await api.put(`/web-api/admin/channels/${c.id}`, body)
        await qc.invalidateQueries({ queryKey: ['channels'] })
        toastOK('已保存')
      } else {
        const r = await api.post<{ channel: { id: number } }>('/web-api/admin/channels', body)
        await qc.invalidateQueries({ queryKey: ['channels'] })
        toastOK('已创建，接着完成下面的接入清单')
        onCreated(r.channel.id)
      }
      return true
    } catch (e) {
      toastError(e)
      return false
    } finally {
      setBusy(false)
    }
  }
  return { save, busy }
}

// useConnTest 测试渠道连接，结果留在接入清单里显示。
function useConnTest(c?: ChannelView) {
  const [result, setResult] = useState<TestResult | null>(null)
  const m = useMutation({
    mutationFn: () => api.post<TestResult>(`/web-api/admin/channels/${c!.id}/test`),
    onSuccess: setResult,
    onError: toastError,
  })
  return { result, clear: () => setResult(null), run: () => m.mutate(), pending: m.isPending }
}

type SetBase = <K extends keyof Base>(k: K, v: Base[K]) => void

function useFormState<T extends Base>(init: () => T) {
  const [v, setV] = useState<T>(init)
  const set = <K extends keyof T>(k: K, x: T[K]) => setV((s) => ({ ...s, [k]: x }))
  const setBase: SetBase = (k, x) => setV((s) => ({ ...s, [k]: x }))
  return [v, set, setBase] as const
}

interface Step {
  title: ReactNode
  description?: ReactNode
  done: boolean
  error?: boolean
}

// Checklist 是接入清单：每一步完成打勾，第一个没完成的高亮。
function Checklist({ steps }: { steps: Step[] }) {
  const done = steps.filter((s) => s.done).length
  const current = steps.findIndex((s) => !s.done)
  const status = (s: Step, i: number): StepStatus =>
    s.done ? 'done' : s.error ? 'error' : i === current ? 'current' : 'wait'
  return (
    <section className="rounded-2xl border border-separator p-4">
      <div className="flex items-center justify-between gap-3">
        <h3 className="text-sm font-semibold">接入清单</h3>
        <span className="tb-num text-xs text-muted">
          已完成 {done}/{steps.length}
        </span>
      </div>
      <div className="mt-2 h-1 overflow-hidden rounded-full bg-default" aria-hidden>
        <div
          className="h-full rounded-full bg-success transition-[width]"
          style={{ width: `${(done / steps.length) * 100}%` }}
        />
      </div>
      <ol className="mt-4 flex flex-col gap-3">
        {steps.map((s, i) => {
          const st = status(s, i)
          return (
            <li key={i} className="flex gap-3">
              <span className="grid size-5 shrink-0 place-items-center">{stepDot(st)}</span>
              <div className="min-w-0">
                <p className={cx('text-sm', st === 'done' ? 'text-muted' : 'font-medium')}>{s.title}</p>
                {s.description && st !== 'done' && (
                  <p className={cx('mt-0.5 text-xs', st === 'error' ? 'text-danger' : 'text-muted')}>{s.description}</p>
                )}
              </div>
            </li>
          )
        })}
      </ol>
    </section>
  )
}

function BaseFields({ v, set }: { v: Base; set: SetBase }) {
  return (
    <Section title="基本信息">
      <div className="grid gap-4 sm:grid-cols-2">
        <TextInput
          label="渠道名称"
          placeholder="例如：主经营码、某某易支付"
          maxLength={32}
          isRequired
          value={v.name}
          onChange={(x) => set('name', x)}
          validate={(x) => (x.trim() ? null : '请填写名称')}
        />
        <TextInput label="备注（可选）" maxLength={500} value={v.remark} onChange={(x) => set('remark', x)} />
      </div>
    </Section>
  )
}

function EnableField({ c, v, set }: { c?: ChannelView; v: Base; set: SetBase }) {
  return (
    <Section title="启用">
      <SwitchInput
        label="启用这个渠道"
        description="启用前要把必填项填完整；停用后用这个渠道的商户下不了新单，已有订单照常确认到账。"
        value={v.enabled}
        onChange={(x) => set('enabled', x)}
      />
      {c?.problem && c.enabled && <Notice status="danger">现在不能收款：{c.problem}</Notice>}
    </Section>
  )
}

function FormFooter({
  c,
  formId,
  busy,
  test,
}: {
  c?: ChannelView
  formId: string
  busy: boolean
  test?: ReturnType<typeof useConnTest>
}) {
  return (
    <Drawer.Footer className="flex-wrap justify-start gap-2 border-t border-separator px-5 py-4 sm:px-6">
      <Button type="submit" form={formId} isPending={busy}>
        {c ? '保存' : '创建渠道'}
      </Button>
      {c && test && (
        <Button variant="secondary" isPending={test.pending} onPress={test.run}>
          <PlugConnection />
          测试连接
        </Button>
      )}
      <span className="ms-auto text-xs text-muted">
        {c ? '密钥类字段留空表示不改' : '创建后才能生成密钥、测试连接'}
      </span>
    </Drawer.Footer>
  )
}

// Fold 是默认收起的一段（导入已有私钥）。
function Fold({ title, children }: { title: string; children: ReactNode }) {
  return (
    <Accordion className="rounded-2xl border border-separator px-1">
      <Accordion.Item>
        <Accordion.Heading>
          <Accordion.Trigger className="text-sm">
            {title}
            <Accordion.Indicator>
              <ChevronDown />
            </Accordion.Indicator>
          </Accordion.Trigger>
        </Accordion.Heading>
        <Accordion.Panel>
          <Accordion.Body>{children}</Accordion.Body>
        </Accordion.Panel>
      </Accordion.Item>
    </Accordion>
  )
}

// ImportKey 导入已有的私钥：和表单其他字段一起保存。
function ImportKey({
  title,
  placeholder,
  busy,
  onImport,
}: {
  title: string
  placeholder: string
  busy: boolean
  onImport: (key: string) => Promise<boolean>
}) {
  const [key, setKey] = useState('')
  return (
    <Fold title={title}>
      <div className="flex flex-col gap-2">
        <TextAreaInput ariaLabel="私钥" value={key} onChange={setKey} placeholder={placeholder} rows={4} mono />
        <div>
          <Button
            variant="secondary"
            size="sm"
            isDisabled={!key.trim()}
            isPending={busy}
            onPress={async () => {
              if (await onImport(key.trim())) setKey('')
            }}
          >
            导入并保存
          </Button>
        </div>
      </div>
    </Fold>
  )
}

// KeyField 显示渠道自己的 RSA 公钥，生成新的密钥对（私钥只加密存库，不再显示）。
function KeyField({
  c,
  label,
  pub,
  has,
  upload,
  warn,
}: {
  c?: ChannelView
  label: string
  pub: string
  has: boolean
  upload: string
  warn: string
}) {
  const qc = useQueryClient()
  const gen = useMutation({
    mutationFn: () => api.post<{ public_key: string }>(`/web-api/admin/channels/${c!.id}/keypair`),
    onSuccess: () => {
      toastOK(`已生成新的密钥对，请把公钥${upload}`)
      void qc.invalidateQueries({ queryKey: ['channels'] })
    },
    onError: toastError,
  })
  return (
    <div className="flex flex-col gap-2">
      <span className="text-sm font-medium">{label}</span>
      {pub ? (
        <div className="flex flex-col gap-1 rounded-xl bg-default/60 px-3 py-2.5">
          <span className="text-xs text-muted">公钥，{upload}</span>
          <Copyable text={pub} mono>
            {pub.slice(0, 32)}…{pub.slice(-12)}
          </Copyable>
        </div>
      ) : (
        <p className="text-sm text-muted">{c ? '还没有密钥对。' : '先创建渠道，再在这里生成密钥对。'}</p>
      )}
      {c && (
        <div>
          {has ? (
            <Confirm title="生成新的密钥对？" content={warn} okText="重新生成" danger onConfirm={() => gen.mutate()}>
              <Button size="sm" variant="secondary" isPending={gen.isPending}>
                重新生成密钥对
              </Button>
            </Confirm>
          ) : (
            <Button size="sm" isPending={gen.isPending} onPress={() => gen.mutate()}>
              生成密钥对
            </Button>
          )}
        </div>
      )}
    </div>
  )
}

const gateways: Record<string, string> = {
  'https://openapi.alipay.com': '正式环境',
  'https://openapi-sandbox.dl.alipaydev.com': '沙箱环境',
}

interface AlipayValues extends Base {
  app_id: string
  alipay_public_key: string
  gateway: string
  qrcode_url: string
  payee_name: string
}

function AlipayForm({ c, onCreated }: { c?: ChannelView; onCreated: (id: number) => void }) {
  const a = (c?.config ?? {}) as Partial<AlipayQRConfig>
  const formId = useId()
  const { save, busy } = useSave(c, 'alipay_qr', onCreated)
  const test = useConnTest(c)
  const fileRef = useRef<HTMLInputElement>(null)
  const [decoding, setDecoding] = useState(false)
  const [v, set, setBase] = useFormState<AlipayValues>(() => ({
    name: c?.name ?? '支付宝经营码',
    remark: c?.remark ?? '',
    enabled: c?.enabled ?? false,
    app_id: a.app_id ?? '',
    alipay_public_key: a.alipay_public_key ?? '',
    gateway: a.gateway || 'https://openapi.alipay.com',
    qrcode_url: a.qrcode_url ?? '',
    payee_name: a.payee_name ?? '',
  }))
  const submit = async (privateKey = '') => {
    const ok = await save(v, {
      app_id: v.app_id,
      alipay_public_key: v.alipay_public_key,
      gateway: v.gateway,
      qrcode_url: v.qrcode_url,
      payee_name: v.payee_name,
      app_private_key: privateKey,
    })
    if (ok) test.clear()
    return ok
  }
  const onFile = async (f: File | undefined) => {
    if (!f) return
    setDecoding(true)
    try {
      const texts = await decodeQRImage(f)
      const hit = texts.find((t) => /^https:\/\/qr\.alipay\.com\//i.test(t))
      if (hit) {
        set('qrcode_url', hit)
        toastOK('已识别经营码')
      } else if (texts.length) {
        toastError(new Error(`识别到的不是支付宝收款码：${texts[0].slice(0, 80)}`))
      } else {
        toastError(new Error('没有在图片里找到二维码，请换一张清晰的截图'))
      }
    } catch (e) {
      toastError(e)
    } finally {
      setDecoding(false)
      if (fileRef.current) fileRef.current.value = ''
    }
  }
  const gatewayOptions = [...Object.keys(gateways), ...(a.gateway && !(a.gateway in gateways) ? [a.gateway] : [])]
  const steps: Step[] = [
    {
      title: '在支付宝开放平台创建应用',
      description: '网页/移动应用均可。开通「账务明细查询」权限并让应用上线，把应用 ID 填到下面。',
      done: !!v.app_id.trim(),
    },
    {
      title: '生成应用密钥，上传应用公钥',
      description: c
        ? '在下面生成密钥对，把应用公钥上传到开放平台（接口加签方式选「公钥」）。'
        : '先创建渠道，之后才能生成密钥对。',
      done: !!a.has_private_key,
    },
    {
      title: '填入支付宝公钥',
      description: '开放平台「接口加签方式」里给出的支付宝公钥，不是应用公钥。',
      done: !!v.alipay_public_key.trim(),
    },
    {
      title: '识别经营码',
      description: '在支付宝 App 里打开经营码（收钱码），截图后上传识别。',
      done: !!v.qrcode_url.trim(),
    },
    {
      title: '测试连接',
      description: test.result?.message ?? '保存后点底部的「测试连接」，确认能查到账单。',
      done: !!test.result?.ok,
      error: test.result?.ok === false,
    },
    {
      title: '启用并分配给商户',
      description: '打开下面的「启用」并保存，再到商户页给商户选这个渠道。',
      done: !!c?.enabled,
    },
  ]
  return (
    <>
      <Drawer.Body className="px-5 py-5 sm:px-6">
        <Form
          id={formId}
          className="flex flex-col gap-7"
          onSubmit={(e) => {
            e.preventDefault()
            void submit()
          }}
        >
          <Checklist steps={steps} />
          <BaseFields v={v} set={setBase} />
          <Section title="开放平台应用">
            <TextInput
              label="应用 ID（AppID）"
              placeholder="2021000000000000"
              mono
              value={v.app_id}
              onChange={(x) => set('app_id', x)}
            />
            <ComboBox
              variant="secondary"
              allowsCustomValue
              inputValue={v.gateway}
              onInputChange={(x) => set('gateway', x)}
              onSelectionChange={(k) => k !== null && set('gateway', String(k))}
            >
              <Label>支付宝网关</Label>
              <ComboBox.InputGroup>
                <Input className="tb-mono" />
                <ComboBox.Trigger />
              </ComboBox.InputGroup>
              <Description>可以选，也可以直接填其他网关地址</Description>
              <ComboBox.Popover>
                <ListBox>
                  {gatewayOptions.map((g) => (
                    <ListBox.Item key={g} id={g} textValue={g}>
                      <div className="flex flex-col">
                        <span className="tb-mono">{g}</span>
                        {gateways[g] && <span className="text-xs text-muted">{gateways[g]}</span>}
                      </div>
                      <ListBox.ItemIndicator />
                    </ListBox.Item>
                  ))}
                </ListBox>
              </ComboBox.Popover>
            </ComboBox>
            <KeyField
              c={c}
              label="应用密钥"
              pub={a.app_public_key ?? ''}
              has={!!a.has_private_key}
              upload="上传到支付宝开放平台"
              warn="旧密钥立即作废，要把新的应用公钥重新上传到支付宝开放平台，否则查账会失败。"
            />
            <ImportKey
              title="已经有应用私钥？导入"
              placeholder="粘贴应用私钥（PKCS#8 或 PKCS#1，Base64 或 PEM）。保存时加密存储，不会再显示。"
              busy={busy}
              onImport={(k) => submit(k)}
            />
            <TextAreaInput
              label="支付宝公钥"
              placeholder="开放平台「接口加签方式」里的支付宝公钥（不是应用公钥）"
              rows={4}
              mono
              value={v.alipay_public_key}
              onChange={(x) => set('alipay_public_key', x)}
            />
          </Section>
          <Section title="经营码">
            <div className="flex flex-col gap-2">
              <TextInput
                label="经营码内容"
                placeholder="https://qr.alipay.com/..."
                mono
                value={v.qrcode_url}
                onChange={(x) => set('qrcode_url', x)}
              />
              <div className="flex flex-wrap items-center gap-2 text-xs text-muted">
                <Button size="sm" variant="secondary" isPending={decoding} onPress={() => fileRef.current?.click()}>
                  <Picture />
                  识别经营码图片
                </Button>
                <span>上传经营码截图，自动识别里面的链接</span>
              </div>
              <input
                ref={fileRef}
                type="file"
                accept="image/*"
                hidden
                onChange={(e) => void onFile(e.target.files?.[0])}
              />
            </div>
            <TextInput
              label="收款方名称（可选）"
              placeholder="显示在收银台上，例如：某某小店"
              maxLength={32}
              value={v.payee_name}
              onChange={(x) => set('payee_name', x)}
            />
          </Section>
          <EnableField c={c} v={v} set={setBase} />
        </Form>
      </Drawer.Body>
      <FormFooter c={c} formId={formId} busy={busy} test={test} />
    </>
  )
}

interface UpstreamValues extends Base {
  api_url: string
  version: 1 | 2
  pid: string
  key: string
  platform_public_key: string
  types: string[]
}

function UpstreamForm({ c, onCreated }: { c?: ChannelView; onCreated: (id: number) => void }) {
  const u = (c?.config ?? {}) as Partial<UpstreamConfig>
  const formId = useId()
  const { save, busy } = useSave(c, 'epay', onCreated)
  const test = useConnTest(c)
  const apiURL = useQuery(meQuery).data?.api_url ?? '/'
  const [v, set, setBase] = useFormState<UpstreamValues>(() => ({
    name: c?.name ?? '',
    remark: c?.remark ?? '',
    enabled: c?.enabled ?? false,
    api_url: u.api_url ?? '',
    version: u.version ?? 1,
    pid: u.pid ?? '',
    key: '',
    platform_public_key: u.platform_public_key ?? '',
    types: u.types ?? ['alipay'],
  }))
  const submit = async (privateKey = '') => {
    const ok = await save(v, {
      api_url: v.api_url,
      version: v.version,
      pid: v.pid,
      key: v.key,
      private_key: privateKey,
      platform_public_key: v.platform_public_key,
      types: v.types,
    })
    if (ok) {
      set('key', '')
      test.clear()
    }
    return ok
  }
  const steps: Step[] = [
    {
      title: '填写上游接口地址和商户号',
      description: '在上游商户后台能找到。',
      done: !!v.api_url.trim() && !!v.pid.trim(),
    },
    v.version === 2
      ? {
          title: '配置 RSA 密钥',
          description: c
            ? '生成或导入商户私钥，把商户公钥填到上游后台，再把上游的平台公钥填到下面。'
            : '先创建渠道，之后才能生成密钥对。',
          done: !!u.has_private_key && !!v.platform_public_key.trim(),
        }
      : { title: '填写商户密钥（MD5）', description: '上游商户后台里的商户密钥。', done: !!u.has_key || !!v.key },
    { title: '勾选开放的支付方式', description: '只勾上游真正开通了的方式。', done: v.types.length > 0 },
    {
      title: '测试连接',
      description: test.result?.message ?? '保存后点底部的「测试连接」，确认上游能正常查单。',
      done: !!test.result?.ok,
      error: test.result?.ok === false,
    },
    {
      title: '启用并分配给商户',
      description: '打开下面的「启用」并保存，再到商户页给商户选这个渠道。',
      done: !!c?.enabled,
    },
  ]
  return (
    <>
      <Drawer.Body className="px-5 py-5 sm:px-6">
        <Form
          id={formId}
          className="flex flex-col gap-7"
          onSubmit={(e) => {
            e.preventDefault()
            void submit()
          }}
        >
          <Notice>
            下单时向上游预下单，付款人在本站收银台扫上游的码或跳到上游付款，上游回调或查单确认后到账。按原价付款，不加尾数。
          </Notice>
          <Checklist steps={steps} />
          <BaseFields v={v} set={setBase} />
          <Section title="上游接口">
            <TextInput
              label="上游接口地址"
              placeholder="https://pay.example.com/"
              description="填上游给的易支付接口地址（网站根地址），要 HTTPS；末尾的 submit.php、mapi.php 会自动去掉。"
              mono
              value={v.api_url}
              onChange={(x) => set('api_url', x)}
            />
            <TextInput
              label="商户号（pid）"
              placeholder="上游商户后台里的商户 ID"
              maxLength={32}
              mono
              value={v.pid}
              onChange={(x) => set('pid', x)}
            />
            <RadioInput
              label="接口版本"
              horizontal
              value={String(v.version) as '1' | '2'}
              onChange={(x) => set('version', x === '2' ? 2 : 1)}
              options={[
                { value: '1', label: 'V1（MD5 签名）' },
                { value: '2', label: 'V2（RSA 签名）' },
              ]}
            />
          </Section>
          <Section title="签名密钥">
            {v.version === 2 ? (
              <>
                <KeyField
                  c={c}
                  label="商户密钥对"
                  pub={u.public_key ?? ''}
                  has={!!u.has_private_key}
                  upload="填到上游商户后台的「商户公钥」"
                  warn="旧私钥立即作废，要把新的商户公钥重新填到上游商户后台，否则预下单和查单会失败。"
                />
                <ImportKey
                  title="上游已经给了商户私钥？导入"
                  placeholder="粘贴商户私钥（PKCS#8 或 PKCS#1，Base64 或 PEM）。保存时加密存储，不会再显示。"
                  busy={busy}
                  onImport={(k) => submit(k)}
                />
                <TextAreaInput
                  label="上游平台公钥"
                  placeholder="上游商户后台里的「平台公钥」，用来验证上游的响应和回调"
                  rows={4}
                  mono
                  value={v.platform_public_key}
                  onChange={(x) => set('platform_public_key', x)}
                />
              </>
            ) : (
              <TextInput
                label="商户密钥（MD5）"
                type="password"
                autoComplete="off"
                placeholder={u.has_key ? '已保存，留空表示不改' : '上游商户后台里的商户密钥'}
                description="加密存储，保存后不再显示。换了上游接口地址时要重新填。"
                value={v.key}
                onChange={(x) => set('key', x)}
              />
            )}
          </Section>
          <Section title="支付方式">
            <CheckboxGroup
              variant="secondary"
              aria-label="开放的支付方式"
              value={v.types}
              onChange={(x) => set('types', x)}
            >
              <div className="flex flex-wrap gap-x-5 gap-y-2">
                {Object.entries(payTypeText).map(([value, label]) => (
                  <Checkbox key={value} value={value}>
                    <Checkbox.Content>
                      <Checkbox.Control>
                        <Checkbox.Indicator />
                      </Checkbox.Control>
                      {label}
                      <span className="tb-mono text-xs text-muted">{value}</span>
                    </Checkbox.Content>
                  </Checkbox>
                ))}
              </div>
              <Description>
                只勾上游真正开通了的方式。商户下单没指定方式、而这里勾了多种时，付款人在收银台选择。
              </Description>
            </CheckboxGroup>
            {c && (
              <p className="text-xs text-muted">
                下单时会把回调地址 <Code>{`${apiURL}web-api/channels/${c.id}/notify`}</Code>{' '}
                带给上游，上游后台不用另外配置；上游要能访问到这个地址。
              </p>
            )}
          </Section>
          <EnableField c={c} v={v} set={setBase} />
        </Form>
      </Drawer.Body>
      <FormFooter c={c} formId={formId} busy={busy} test={test} />
    </>
  )
}

function DemoForm({ c, onCreated }: { c?: ChannelView; onCreated: (id: number) => void }) {
  const formId = useId()
  const { save, busy } = useSave(c, 'demo', onCreated)
  const [v, , setBase] = useFormState<Base>(() => ({
    name: c?.name ?? '演示渠道',
    remark: c?.remark ?? '',
    enabled: c?.enabled ?? true,
  }))
  return (
    <>
      <Drawer.Body className="px-5 py-5 sm:px-6">
        <Form
          id={formId}
          className="flex flex-col gap-7"
          onSubmit={(e) => {
            e.preventDefault()
            void save(v, {})
          }}
        >
          <Notice status="warning">
            演示渠道不连支付宝，流水存在内存里，向 POST /web-api/dev/pay
            提交订单号模拟付款（见开发文档）。只在本地演示模式（TILLBOX_CHANNEL=fake）下可用。
          </Notice>
          <BaseFields v={v} set={setBase} />
          <EnableField c={c} v={v} set={setBase} />
        </Form>
      </Drawer.Body>
      <FormFooter c={c} formId={formId} busy={busy} />
    </>
  )
}
