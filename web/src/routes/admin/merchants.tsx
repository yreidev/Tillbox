import { useId, useState } from 'react'
import { Link, createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Button, Card, Description, Drawer, Dropdown, Form, Label, Separator } from '@heroui/react'
import {
  ArrowRotateRight,
  Briefcase,
  CirclePause,
  CirclePlay,
  Ellipsis,
  Key,
  Pencil,
  Plus,
  TrashBin,
} from '@gravity-ui/icons'
import { api, channelsQuery, meQuery } from '../../lib/api'
import { time } from '../../lib/format'
import type { ChannelView, Merchant, MerchantInput, Settings } from '../../lib/types'
import { channelTypeText, payTypeLabel } from '../../lib/types'
import {
  ConfirmDialog,
  Copyable,
  DataTable,
  Desc,
  Dialog,
  Empty,
  Notice,
  PageHeader,
  Pill,
  RadioInput,
  Section,
  SelectInput,
  Stack,
  SwitchInput,
  TextAreaInput,
  TextInput,
  cx,
  linkClass,
  toastError,
  toastOK,
} from '../../lib/ui'

// 有订单的商户只能停用（后端 merchant.Service.Delete 同样拦）。
const deleteBlocked = '已经有订单，只能停用，不能删除'

export const Route = createFileRoute('/admin/merchants')({
  component: MerchantsPage,
})

type Row = { merchant: Merchant; orders: number }
type Secret = { merchant: Merchant; key: string; created: boolean }
type Pending = { kind: 'reset' | 'delete'; merchant: Merchant } | null

const empty: MerchantInput = {
  name: '',
  rsa_public_key: '',
  key_type: 0,
  domains: '',
  enabled: true,
  channel_id: 0,
  remark: '',
}

const inputOf = (m: Merchant): MerchantInput => ({
  name: m.name,
  rsa_public_key: m.rsa_public_key,
  key_type: m.key_type,
  domains: m.domains,
  enabled: m.enabled,
  channel_id: m.channel_id,
  remark: m.remark,
})

function MerchantsPage() {
  const qc = useQueryClient()
  const q = useQuery({ queryKey: ['merchants'], queryFn: () => api.get<{ items: Row[] }>('/web-api/admin/merchants') })
  const apiURL = useQuery(meQuery).data?.api_url ?? ''
  const ch = useQuery(channelsQuery)
  const channels = ch.data?.items ?? []
  const [editing, setEditing] = useState<Merchant | 'new' | null>(null)
  const [secret, setSecret] = useState<Secret | null>(null)
  const [pending, setPending] = useState<Pending>(null)
  const refresh = () => qc.invalidateQueries({ queryKey: ['merchants'] })

  const viewKey = useMutation({
    mutationFn: (m: Merchant) =>
      api
        .get<{ key: string }>(`/web-api/admin/merchants/${m.id}/key`)
        .then((r) => ({ merchant: m, key: r.key, created: false })),
    onSuccess: setSecret,
    onError: toastError,
  })
  const toggle = useMutation({
    mutationFn: (m: Merchant) => api.put(`/web-api/admin/merchants/${m.id}`, { ...inputOf(m), enabled: !m.enabled }),
    onSuccess: (_d, m) => {
      toastOK(m.enabled ? '已停用，这个商户下不了新单' : '已启用')
      void refresh()
    },
    onError: toastError,
  })
  const rows = q.data?.items ?? []
  const channelOf = (m: Merchant) => channels.find((c) => c.id === m.channel_id)

  const actions = (r: Row) => (
    <div className="flex items-center justify-end gap-1">
      <Button
        size="sm"
        variant="secondary"
        isPending={viewKey.isPending && viewKey.variables?.id === r.merchant.id}
        onPress={() => viewKey.mutate(r.merchant)}
      >
        <Key />
        接入信息
      </Button>
      <Button
        size="sm"
        variant="ghost"
        isIconOnly
        aria-label={`编辑 ${r.merchant.name}`}
        onPress={() => setEditing(r.merchant)}
      >
        <Pencil />
      </Button>
      <RowMenu
        row={r}
        onAction={(k) => {
          if (k === 'toggle') toggle.mutate(r.merchant)
          else setPending({ kind: k, merchant: r.merchant })
        }}
      />
    </div>
  )

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        description="每个接入的网站建一个商户。对方按易支付接口对接：接口地址、商户号和 MD5 密钥，用 V2 接口的还要互换 RSA 公钥。"
        actions={
          rows.length > 0 && (
            <Button size="sm" onPress={() => setEditing('new')}>
              <Plus />
              新建商户
            </Button>
          )
        }
      />
      {ch.isSuccess && channels.length === 0 && (
        <Notice status="warning" title="还没有支付渠道">
          商户要选一个渠道才能收款，先到{' '}
          <Link to="/admin/channels" className={linkClass}>
            支付渠道
          </Link>{' '}
          页新建。
        </Notice>
      )}
      {q.isError && <Notice status="danger">{q.error.message}</Notice>}
      {!q.isPending && rows.length === 0 ? (
        <Card className="rounded-2xl">
          <Empty
            icon={<Briefcase />}
            title="还没有商户"
            description="给要接入的网站建一个商户，拿到商户号和密钥后填到对方的易支付设置里。"
          >
            <Button className="mt-2" onPress={() => setEditing('new')}>
              <Plus />
              新建商户
            </Button>
          </Empty>
        </Card>
      ) : (
        <DataTable
          label="商户列表"
          rows={rows}
          rowKey={(r) => r.merchant.id}
          loading={q.isPending}
          minWidth={1040}
          empty="还没有商户"
          card={(r) => {
            const m = r.merchant
            return (
              <div className="flex flex-col gap-2">
                <div className="flex items-center justify-between gap-3">
                  <span className="flex min-w-0 items-center gap-2">
                    <span className="truncate font-medium">{m.name}</span>
                    {!m.enabled && <Pill>已停用</Pill>}
                  </span>
                  <span className="tb-num shrink-0 text-xs text-muted">商户号 {m.id}</span>
                </div>
                <ChannelLine c={channelOf(m)} />
                <div className="flex items-center justify-between gap-3">
                  <span className="text-xs text-muted">
                    <span className="tb-num">{r.orders}</span> 笔订单
                  </span>
                  {actions(r)}
                </div>
              </div>
            )
          }}
          columns={[
            {
              title: '商户',
              render: (r) => (
                <Stack
                  className="max-w-60"
                  main={
                    <span className="flex items-center gap-2">
                      <span className="truncate font-medium">{r.merchant.name}</span>
                      {!r.merchant.enabled && <Pill>已停用</Pill>}
                    </span>
                  }
                  sub={
                    <>
                      商户号 <span className="tb-num">{r.merchant.id}</span>
                      {r.merchant.remark && ` · ${r.merchant.remark}`}
                    </>
                  }
                />
              ),
            },
            { title: '支付渠道', render: (r) => <ChannelLine c={channelOf(r.merchant)} stacked /> },
            {
              title: '签名',
              nowrap: true,
              render: (r) =>
                r.merchant.key_type === 1 ? (
                  <Pill tone="warning">只收 RSA</Pill>
                ) : r.merchant.rsa_public_key ? (
                  <Pill tone="accent">MD5 + RSA</Pill>
                ) : (
                  <Pill>MD5</Pill>
                ),
            },
            {
              title: '域名白名单',
              render: (r) => {
                const d = r.merchant.domains.split('\n').filter(Boolean)
                return d.length ? (
                  <span className="block max-w-44 truncate text-xs" title={d.join('，')}>
                    {d[0]}
                    {d.length > 1 && <span className="text-muted"> 等 {d.length} 个</span>}
                  </span>
                ) : (
                  <span className="text-xs text-muted">不限制</span>
                )
              },
            },
            {
              title: '订单',
              align: 'right',
              nowrap: true,
              render: (r) =>
                r.orders ? (
                  <Link to="/admin/orders" search={{ merchant_id: r.merchant.id }} className={cx(linkClass, 'tb-num')}>
                    {r.orders}
                  </Link>
                ) : (
                  <span className="tb-num text-muted">0</span>
                ),
            },
            {
              title: '创建时间',
              nowrap: true,
              render: (r) => <span className="tb-num text-muted">{time(r.merchant.created_at, false)}</span>,
            },
            { title: '', align: 'right', nowrap: true, render: actions },
          ]}
        />
      )}
      {rows.length > 0 && apiURL && (
        <p className="text-xs text-muted">
          所有商户共用一个接口地址 <Copyable text={apiURL} mono />
        </p>
      )}
      <Editor
        editing={editing}
        channels={channels}
        onClose={() => setEditing(null)}
        onCreated={(m, key) => {
          setEditing(null)
          setSecret({ merchant: m, key, created: true })
        }}
      />
      <SecretModal secret={secret} onClose={() => setSecret(null)} />
      <ConfirmDialog
        open={pending?.kind === 'reset'}
        onClose={() => setPending(null)}
        status="warning"
        title={`重置「${pending?.merchant.name ?? ''}」的 MD5 密钥？`}
        okText="重置密钥"
        onConfirm={async () => {
          const m = pending!.merchant
          const r = await api.post<{ key: string }>(`/web-api/admin/merchants/${m.id}/reset-key`)
          toastOK('已生成新密钥，旧密钥立即失效')
          setSecret({ merchant: m, key: r.key, created: false })
        }}
      >
        <p>旧密钥立即失效，对方网站要同步改成新密钥，否则下单和验签都会失败。</p>
      </ConfirmDialog>
      <ConfirmDialog
        open={pending?.kind === 'delete'}
        onClose={() => setPending(null)}
        title={`删除商户「${pending?.merchant.name ?? ''}」？`}
        okText="删除商户"
        onConfirm={async () => {
          await api.del(`/web-api/admin/merchants/${pending!.merchant.id}`)
          toastOK('已删除')
          void refresh()
        }}
      >
        <p>删除后不能恢复，对方网站用这个商户号下单会失败。</p>
      </ConfirmDialog>
    </div>
  )
}

function RowMenu({ row, onAction }: { row: Row; onAction: (k: 'toggle' | 'reset' | 'delete') => void }) {
  const m = row.merchant
  return (
    <Dropdown>
      <Button size="sm" variant="ghost" isIconOnly aria-label={`${m.name} 的更多操作`} className="text-muted">
        <Ellipsis />
      </Button>
      <Dropdown.Popover placement="bottom end" className="w-64">
        <Dropdown.Menu
          aria-label="更多操作"
          disabledKeys={row.orders > 0 ? ['delete'] : []}
          onAction={(k) => onAction(k as 'toggle' | 'reset' | 'delete')}
        >
          <Dropdown.Item id="toggle" textValue={m.enabled ? '停用' : '启用'}>
            <div className="flex h-8 items-start justify-center pt-px">
              {m.enabled ? (
                <CirclePause className="size-4 shrink-0 text-muted" />
              ) : (
                <CirclePlay className="size-4 shrink-0 text-muted" />
              )}
            </div>
            <div className="flex flex-col">
              <Label>{m.enabled ? '停用商户' : '启用商户'}</Label>
              <Description className="whitespace-normal">
                {m.enabled ? '停用后下不了新单，已有订单不受影响' : '恢复下单'}
              </Description>
            </div>
          </Dropdown.Item>
          <Dropdown.Item id="reset" textValue="重置 MD5 密钥">
            <div className="flex h-8 items-start justify-center pt-px">
              <ArrowRotateRight className="size-4 shrink-0 text-muted" />
            </div>
            <div className="flex flex-col">
              <Label>重置 MD5 密钥</Label>
              <Description className="whitespace-normal">旧密钥立即失效</Description>
            </div>
          </Dropdown.Item>
          <Separator />
          <Dropdown.Item id="delete" textValue="删除商户" variant="danger">
            <div className="flex h-8 items-start justify-center pt-px">
              <TrashBin className="size-4 shrink-0 text-danger" />
            </div>
            <div className="flex flex-col">
              <Label>删除商户</Label>
              <Description className="whitespace-normal">{row.orders > 0 ? deleteBlocked : '不能恢复'}</Description>
            </div>
          </Dropdown.Item>
        </Dropdown.Menu>
      </Dropdown.Popover>
    </Dropdown>
  )
}

// ChannelLine 是商户所用渠道和它的状态；渠道有问题时标红，提醒这个商户收不了款。
function ChannelLine({ c, stacked }: { c?: ChannelView; stacked?: boolean }) {
  if (!c) return <Pill tone="danger">没选渠道</Pill>
  const bad = !c.enabled ? '渠道已停用' : c.problem ? '渠道不能收款' : ''
  const sub = bad || (c.pay_types ?? []).map(payTypeLabel).join('、')
  if (stacked)
    return (
      <Stack className="max-w-48" main={c.name} sub={<span className={bad ? 'text-danger' : undefined}>{sub}</span>} />
    )
  return (
    <span className="truncate text-xs text-muted">
      {c.name}
      {sub && <span className={bad ? 'text-danger' : undefined}> · {sub}</span>}
    </span>
  )
}

function Editor({
  editing,
  channels,
  onClose,
  onCreated,
}: {
  editing: Merchant | 'new' | null
  channels: ChannelView[]
  onClose: () => void
  onCreated: (m: Merchant, key: string) => void
}) {
  return (
    <Drawer.Backdrop isOpen={editing !== null} onOpenChange={(v) => !v && onClose()}>
      <Drawer.Content placement="right">
        <Drawer.Dialog className="w-[min(560px,100vw)] max-w-[100vw] p-0 sm:w-[min(560px,100vw)]">
          <Drawer.CloseTrigger />
          <Drawer.Header className="gap-0.5 border-b border-separator px-5 pt-5 pb-4 sm:px-6">
            <p className="text-sm text-muted">
              {editing === 'new' ? '新建商户' : `编辑商户 · 商户号 ${editing?.id ?? ''}`}
            </p>
            <Drawer.Heading className="truncate pr-10 text-lg font-semibold">
              {editing === 'new' || !editing ? '新的商户' : editing.name}
            </Drawer.Heading>
          </Drawer.Header>
          {editing !== null && (
            <EditForm
              key={editing === 'new' ? 'new' : editing.id}
              target={editing}
              channels={channels}
              onClose={onClose}
              onCreated={onCreated}
            />
          )}
        </Drawer.Dialog>
      </Drawer.Content>
    </Drawer.Backdrop>
  )
}

function EditForm({
  target,
  channels,
  onClose,
  onCreated,
}: {
  target: Merchant | 'new'
  channels: ChannelView[]
  onClose: () => void
  onCreated: (m: Merchant, key: string) => void
}) {
  const qc = useQueryClient()
  const formId = useId()
  const [busy, setBusy] = useState(false)
  const isNew = target === 'new'
  const firstEnabled = channels.find((c) => c.enabled)?.id ?? channels[0]?.id ?? 0
  const [v, setV] = useState<MerchantInput>(() => (isNew ? { ...empty, channel_id: firstEnabled } : inputOf(target)))
  const set = <K extends keyof MerchantInput>(k: K, x: MerchantInput[K]) => setV((s) => ({ ...s, [k]: x }))
  return (
    <>
      <Drawer.Body className="px-5 py-5 sm:px-6">
        <Form
          id={formId}
          className="flex flex-col gap-7"
          onSubmit={async (e) => {
            e.preventDefault()
            setBusy(true)
            try {
              if (isNew) {
                const r = await api.post<{ merchant: Merchant; key: string }>('/web-api/admin/merchants', v)
                onCreated(r.merchant, r.key)
              } else {
                await api.put(`/web-api/admin/merchants/${target.id}`, v)
                toastOK('已保存')
                onClose()
              }
              void qc.invalidateQueries({ queryKey: ['merchants'] })
            } catch (err) {
              toastError(err)
            } finally {
              setBusy(false)
            }
          }}
        >
          <Section title="基本信息">
            <TextInput
              label="名称"
              placeholder="例如：主站"
              isRequired
              maxLength={64}
              value={v.name}
              onChange={(x) => set('name', x)}
              validate={(x) => (x.trim() ? null : '请填写名称')}
            />
            <TextInput label="备注（可选）" maxLength={500} value={v.remark} onChange={(x) => set('remark', x)} />
            <SwitchInput
              label="启用"
              description="停用后这个商户下不了新单，已有订单照常确认到账、通知。"
              value={v.enabled}
              onChange={(x) => set('enabled', x)}
            />
          </Section>
          <Section title="收款">
            <SelectInput<number>
              label="支付渠道"
              isRequired
              value={v.channel_id || undefined}
              onChange={(x) => set('channel_id', x ?? 0)}
              description={
                channels.length
                  ? '这个商户的新订单走所选渠道；改渠道不影响已经下的单。'
                  : '还没有支付渠道，请先到「支付渠道」页新建。'
              }
              options={channels.map((c) => ({
                value: c.id,
                label: `${c.name}（${channelTypeText[c.type] ?? c.type}${c.enabled ? '' : '，已停用'}）`,
              }))}
            />
          </Section>
          <Section title="签名">
            <RadioInput
              label="接受的签名"
              horizontal
              value={String(v.key_type) as '0' | '1'}
              onChange={(x) => set('key_type', Number(x))}
              options={[
                { value: '0', label: 'MD5 和 RSA 都收' },
                { value: '1', label: '只收 RSA' },
              ]}
            />
            <TextAreaInput
              label="商户 RSA 公钥"
              description="用 V2 接口（RSA 签名）时填对方的公钥，Base64 或 PEM 都可以；只用 V1（MD5）可以不填。"
              rows={3}
              mono
              isRequired={v.key_type === 1}
              value={v.rsa_public_key}
              onChange={(x) => set('rsa_public_key', x)}
              validate={(x) => (v.key_type === 1 && !x.trim() ? '只收 RSA 时必须填' : null)}
            />
          </Section>
          <Section title="安全">
            <TextAreaInput
              label="通知和回跳域名白名单"
              description="一行一个，支持 *.example.com；留空表示不限制。"
              placeholder={'shop.example.com\n*.example.com'}
              rows={3}
              value={v.domains}
              onChange={(x) => set('domains', x)}
            />
          </Section>
        </Form>
      </Drawer.Body>
      <Drawer.Footer className="gap-2 border-t border-separator px-5 py-4 sm:px-6">
        <Button variant="tertiary" onPress={onClose}>
          取消
        </Button>
        <Button type="submit" form={formId} isPending={busy}>
          {isNew ? '创建并查看接入信息' : '保存'}
        </Button>
      </Drawer.Footer>
    </>
  )
}

function SecretModal({ secret, onClose }: { secret: Secret | null; onClose: () => void }) {
  const settings = useQuery({
    queryKey: ['settings'],
    queryFn: () => api.get<Settings>('/web-api/admin/settings'),
    enabled: secret !== null,
  })
  const apiURL = useQuery(meQuery).data?.api_url ?? ''
  return (
    <Dialog
      title={secret?.created ? '商户已创建' : `接入信息 · ${secret?.merchant.name ?? ''}`}
      open={secret !== null}
      onClose={onClose}
      size="lg"
      footer={<Button onPress={onClose}>完成</Button>}
    >
      {secret && (
        <div className="flex flex-col gap-4">
          {secret.created ? (
            <Notice status="success" title="把下面的信息发给对接方">
              以后在商户列表点「接入信息」还能再看到。密钥相当于收款凭据，不要贴到公开的地方。
            </Notice>
          ) : (
            <Notice status="warning">
              密钥相当于收款凭据，只发给对接方，不要贴到公开的地方。每次查看都会记入审计日志。
            </Notice>
          )}
          <Desc
            data={[
              { key: '接口地址', value: <Copyable text={apiURL} mono /> },
              { key: '商户号（pid）', value: <Copyable text={String(secret.merchant.id)} mono /> },
              { key: 'MD5 密钥（key）', value: <Copyable text={secret.key} mono /> },
              {
                key: '平台公钥（V2 用）',
                value: settings.data ? (
                  <Copyable text={settings.data.platform_public_key} mono>
                    {settings.data.platform_public_key.slice(0, 24)}…
                  </Copyable>
                ) : (
                  '…'
                ),
              },
            ]}
          />
          <p className="rounded-xl bg-default/60 px-3 py-2.5 text-xs text-muted">
            调用方按易支付接入：支付地址填接口地址，商户 ID 填商户号，密钥填 MD5
            密钥，支付方式填商户所用渠道开放的方式（经营码渠道只有支付宝 alipay）。
          </p>
        </div>
      )}
    </Dialog>
  )
}
