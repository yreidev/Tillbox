// 设置 → 管理员通知：Telegram 机器人、接收人，以及告警、收款到账、付款申诉要不要发。
// 结构和 internal/admin/telegram.go 一致。
import { Fragment, useEffect, useMemo, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Button,
  Description,
  Dropdown,
  Form,
  ItemCard,
  ItemCardGroup,
  Label,
  Link,
  Separator,
  Typography,
} from '@yreidev/ui'
import {
  ArrowUpRightFromSquare,
  ChevronDown,
  CircleCheck,
  Comment,
  CreditCard,
  Eye,
  EyeSlash,
  Layers,
  LogoTelegram,
  PaperPlane,
  Plus,
  TriangleExclamation,
} from '@gravity-ui/icons'
import { encode } from 'uqr'
import { ApiError, api } from '../lib/api'
import { relative } from '../lib/format'
import { useNow } from '../lib/now'
import {
  Confirm,
  ConfirmDialog,
  ConflictNotice,
  Copyable,
  Dialog,
  enter,
  Loading,
  Notice,
  NumberInput,
  Panel,
  SettingsRow,
  StatusDot,
  SwitchInput,
  TextInput,
  toastError,
  toastOK,
  useRevision,
} from '../lib/ui'

export interface Binding {
  link: string
  expires_at: number
  /** 收消息出错（例如机器人设了 Webhook） */
  error: string
}

export interface TelegramChat {
  id: number
  chat_id: number
  name: string
  created_at: number
  sent_at: number
  error_at: number
  error: string
}

/** 和 internal/monitor 的 State 一致：告警任务的运行情况和最近一次发送 */
export interface MonitorState {
  last_run_at: number
  last_error: string
  sent_at: number
  send_error_at: number
  send_error: string
}

export interface TelegramView {
  configured: boolean
  bot_username: string
  remind_hours: number
  paid: boolean
  claims: boolean
  chats: TelegramChat[]
  binding: Binding | null
  state: MonitorState
  /** 保存时放进 If-Match，检查有没有在别处改过 */
  revision: number
}

interface Prefs {
  remind_hours: number
  paid: boolean
  claims: boolean
}

export const telegramKey = ['telegram']
const base = '/web-api/admin/settings/telegram'
const telegramQuery = { queryKey: telegramKey, queryFn: () => api.get<TelegramView>(base) }

/** 测试消息的种类，和 internal/monitor 的 TestKinds 一致；all 是每种各发一条 */
const testKinds = [
  { id: 'alert_open', label: '告警开始', description: '示例：第三方平台查单连续失败', Icon: TriangleExclamation },
  { id: 'alert_resolved', label: '告警恢复', description: '示例：同一条告警恢复了', Icon: CircleCheck },
  { id: 'paid', label: '收款到账', description: '示例：一笔 ¥12.00 的订单到账', Icon: CreditCard },
  { id: 'claim', label: '付款申诉', description: '示例：付款人申诉已付款未到账', Icon: Comment },
]

export function TelegramSettings() {
  const q = useQuery({
    ...telegramQuery,
    // 等待绑定时勤快一点，绑好了马上能看到
    refetchInterval: (query) => (query.state.data?.binding ? 2_000 : 15_000),
  })
  const now = useNow()
  const tg = q.data
  if (q.isError) return <Notice status="danger">{q.error.message}</Notice>
  if (!tg) return <Loading />
  return (
    <>
      <Typography.Paragraph size="sm" color="muted">
        用 Telegram
        机器人把告警、收款到账和付款申诉推给管理员，只支持和机器人单聊。这里的“通知”是发给自己的；发给商户的异步通知在左侧“通知”页。
      </Typography.Paragraph>
      {tg.configured && tg.state.send_error_at > tg.state.sent_at && (
        <Notice status="warning" title="最近一次没发出去">
          {relative(tg.state.send_error_at, now)}：{tg.state.send_error}。会在下一轮（30 秒内）重发。
        </Notice>
      )}
      <BotPanel tg={tg} />
      {tg.configured && (
        <>
          <ChatsPanel tg={tg} now={now} />
          <PrefsPanel tg={tg} />
        </>
      )}
    </>
  )
}

/** TokenInput 是机器人 Token 的输入框：默认打码，可以显示出来核对；手机上长按粘贴不方便时用“粘贴”按钮。 */
function TokenInput({
  value,
  onChange,
  configured,
  botUsername,
}: {
  value: string
  onChange: (v: string) => void
  configured: boolean
  botUsername: string
}) {
  const [shown, setShown] = useState(false)
  const canPaste = typeof navigator !== 'undefined' && !!navigator.clipboard?.readText
  return (
    <TextInput
      ariaLabel="机器人 Token"
      type={shown ? 'text' : 'password'}
      mono
      autoComplete="off"
      isRequired={!configured}
      maxLength={200}
      placeholder={configured ? `已设置 @${botUsername}，换机器人时才填` : '123456789:AA…'}
      value={value}
      onChange={onChange}
      validate={(v) => (!configured && !v.trim() ? '请粘贴 @BotFather 给的 Token' : null)}
      suffix={
        <>
          {canPaste && (
            <Button
              size="sm"
              variant="ghost"
              onPress={async () => {
                try {
                  const text = (await navigator.clipboard.readText()).trim()
                  if (!text) throw new Error('剪贴板是空的')
                  onChange(text)
                } catch {
                  toastError(new Error('浏览器没让读剪贴板，请在输入框里长按粘贴'))
                }
              }}
            >
              粘贴
            </Button>
          )}
          <Button
            isIconOnly
            size="sm"
            variant="ghost"
            aria-label={shown ? '隐藏 Token' : '显示 Token'}
            onPress={() => setShown((v) => !v)}
          >
            {shown ? <EyeSlash className="size-4" /> : <Eye className="size-4" />}
          </Button>
        </>
      }
    />
  )
}

function BotPanel({ tg }: { tg: TelegramView }) {
  const qc = useQueryClient()
  const [token, setToken] = useState('')
  const [clearing, setClearing] = useState(false)
  const save = useMutation({
    // 只换机器人，发哪些消息沿用服务器上的设置；这期间别处改过时重读一次再存，免得把别人的修改改回去。
    mutationFn: async () => {
      const put = (t: TelegramView) =>
        api.put<TelegramView>(
          base,
          { token: token.trim(), remind_hours: t.remind_hours, paid: t.paid, claims: t.claims },
          { revision: t.revision },
        )
      try {
        return await put(tg)
      } catch (err) {
        if (!(err instanceof ApiError && err.status === 409)) throw err
        return put(await qc.fetchQuery({ ...telegramQuery, staleTime: 0 }))
      }
    },
    onSuccess: (r) => {
      qc.setQueryData(telegramKey, r)
      void qc.invalidateQueries({ queryKey: ['monitor'] })
      setToken('')
      toastOK(tg.configured ? `已换成 @${r.bot_username}` : `已保存，机器人是 @${r.bot_username}。下一步添加接收人`)
    },
    onError: toastError,
  })
  return (
    <Panel title="Telegram 机器人" description="告警、收款到账、付款申诉都由它发出">
      {!tg.configured && (
        <Notice title="怎么建机器人" className="mb-5">
          <ol className="mt-1 list-decimal space-y-1 pl-4">
            <li>在 Telegram 里搜索 @BotFather，打开后发送 /newbot。</li>
            <li>按提示给机器人起名字和用户名（用户名以 bot 结尾）。</li>
            <li>复制它回复的 Token，粘贴到下面，保存。</li>
            <li>保存后在“接收人”里点“添加接收人”，绑定自己的 Telegram 账号。</li>
          </ol>
        </Notice>
      )}
      <Form
        className="divide-y divide-separator"
        onSubmit={(e) => {
          e.preventDefault()
          save.mutate()
        }}
      >
        <SettingsRow
          label={tg.configured ? '机器人' : '机器人 Token'}
          description={
            tg.configured ? (
              <>
                现在是{' '}
                <Link href={`https://t.me/${tg.bot_username}`} target="_blank" rel="noopener noreferrer">
                  @{tg.bot_username}
                </Link>
                。换成另一个机器人会清空已绑定的接收人，需要重新绑定。
              </>
            ) : (
              '保存时会向 Telegram 核对。Token 加密存储，保存后不再显示。'
            )
          }
        >
          <TokenInput value={token} onChange={setToken} configured={tg.configured} botUsername={tg.bot_username} />
          <div>
            <Button size="sm" type="submit" isDisabled={tg.configured && !token.trim()} isPending={save.isPending}>
              {!tg.configured && <LogoTelegram />}
              {tg.configured ? '换机器人' : '保存'}
            </Button>
          </div>
        </SettingsRow>
        {tg.configured && (
          <SettingsRow
            className={enter}
            label="清除设置"
            description="删除机器人 Token 和全部接收人，不再发任何消息。监控页照常检查和显示告警。"
          >
            <div>
              <Button size="sm" variant="danger-soft" onPress={() => setClearing(true)}>
                清除 Telegram 设置
              </Button>
            </div>
          </SettingsRow>
        )}
      </Form>
      <ConfirmDialog
        open={clearing}
        onClose={() => setClearing(false)}
        title="清除 Telegram 设置？"
        okText="清除"
        onConfirm={async () => {
          await api.del(base)
          await qc.invalidateQueries({ queryKey: telegramKey })
          void qc.invalidateQueries({ queryKey: ['monitor'] })
          toastOK('已清除，不再发 Telegram 消息')
        }}
      >
        <Typography.Paragraph size="sm" color="muted">
          机器人 Token 和 {tg.chats.length} 个接收人都会删除。之后要用得重新设置和绑定。
        </Typography.Paragraph>
      </ConfirmDialog>
    </Panel>
  )
}

function ChatsPanel({ tg, now }: { tg: TelegramView; now: number }) {
  const qc = useQueryClient()
  const [bindOpen, setBindOpen] = useState(false)
  // cancelled 记下管理员自己取消了绑定，prev 是上次看到的绑定链接，用来判断绑定是不是成功了
  const cancelled = useRef(false)
  const prev = useRef<Binding | null>(null)
  const refresh = () => qc.invalidateQueries({ queryKey: telegramKey })
  const bind = useMutation({
    mutationFn: () => api.post<Binding>(`${base}/bind`),
    onSuccess: (b) => {
      cancelled.current = false
      qc.setQueryData<TelegramView>(telegramKey, (old) => old && { ...old, binding: b })
      setBindOpen(true)
    },
    onError: toastError,
  })
  const test = useMutation({
    mutationFn: (kind: string) => api.post(`${base}/test`, { kind }),
    onSuccess: async (_, kind) => {
      await refresh()
      toastOK(
        kind === 'all'
          ? `${testKinds.length} 条测试消息已发出，请到 Telegram 查看`
          : '测试消息已发出，请到 Telegram 查看',
      )
    },
    onError: async (e) => {
      await refresh()
      toastError(e)
    },
  })

  // 绑定链接从“有”变成“没有”：没到期又不是管理员取消的，就是对方已经点了“开始”，绑定成功。
  useEffect(() => {
    const was = prev.current
    prev.current = tg.binding
    if (!was || tg.binding || cancelled.current) return
    if (Date.now() < was.expires_at) toastOK('绑定成功，消息会发到这个 Telegram 账号')
    else toastError(new Error('绑定链接已过期，没有绑上，请重新添加'))
  }, [tg.binding])

  const cancelBind = async () => {
    cancelled.current = true
    setBindOpen(false)
    try {
      await api.del(`${base}/bind`)
    } catch (e) {
      toastError(e)
    }
    await refresh()
  }

  return (
    <Panel
      title="接收人"
      description={tg.state.sent_at ? `上次发出消息是 ${relative(tg.state.sent_at, now)}` : '还没发过消息'}
      extra={
        <>
          <Dropdown>
            <Button size="sm" variant="tertiary" isDisabled={tg.chats.length === 0} isPending={test.isPending}>
              <PaperPlane />
              发测试消息
              <ChevronDown className="size-3.5" />
            </Button>
            {/* HeroUI 默认最宽 48svw，手机上说明会挤成两行 */}
            <Dropdown.Popover placement="bottom end" className="w-64 max-w-[calc(100svw-2rem)]">
              <Dropdown.Menu aria-label="发哪种测试消息" onAction={(k) => test.mutate(String(k))}>
                {testKinds.map(({ id, label, description, Icon }) => (
                  <Dropdown.Item key={id} id={id} textValue={label}>
                    <div className="flex h-8 items-start justify-center pt-px">
                      <Icon className="size-4 shrink-0 text-muted" />
                    </div>
                    <div className="flex flex-col">
                      <Label>{label}</Label>
                      <Description className="whitespace-normal">{description}</Description>
                    </div>
                  </Dropdown.Item>
                ))}
                <Separator />
                <Dropdown.Item id="all" textValue="全部发一遍">
                  <div className="flex h-8 items-start justify-center pt-px">
                    <Layers className="size-4 shrink-0 text-muted" />
                  </div>
                  <div className="flex flex-col">
                    <Label>全部发一遍</Label>
                    <Description className="whitespace-normal">上面四种各发一条</Description>
                  </div>
                </Dropdown.Item>
              </Dropdown.Menu>
            </Dropdown.Popover>
          </Dropdown>
          <Button size="sm" variant="secondary" isPending={bind.isPending} onPress={() => bind.mutate()}>
            <Plus />
            添加接收人
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3">
        {tg.binding && !bindOpen && (
          <Notice
            status="accent"
            action={
              <Button size="sm" variant="tertiary" onPress={() => setBindOpen(true)}>
                查看
              </Button>
            }
          >
            有一个绑定链接在等对方打开，{relative(tg.binding.expires_at, now)}失效。
          </Notice>
        )}
        {tg.chats.length === 0 ? (
          <Typography.Paragraph size="sm" color="muted">
            还没有接收人，消息发不出去。点“添加接收人”，用自己的 Telegram 打开链接，点“开始”就绑好了。
          </Typography.Paragraph>
        ) : (
          <ItemCardGroup variant="transparent">
            {tg.chats.map((c, i) => (
              <Fragment key={c.id}>
                {i > 0 && <Separator />}
                <ItemCard variant="transparent" className="px-0">
                  <ItemCard.Content>
                    <ItemCard.Title className="break-all whitespace-normal">{c.name}</ItemCard.Title>
                    <ItemCard.Description className="whitespace-normal">
                      {c.error_at > c.sent_at ? (
                        <span className="text-danger">
                          {relative(c.error_at, now)}发送失败：{c.error}
                        </span>
                      ) : c.sent_at ? (
                        `${relative(c.sent_at, now)}送达`
                      ) : (
                        `${relative(c.created_at, now)}绑定，还没发过消息`
                      )}
                    </ItemCard.Description>
                  </ItemCard.Content>
                  <ItemCard.Action>
                    <Confirm
                      title="解除绑定？"
                      content="之后不再给这个账号发消息。"
                      okText="解除"
                      danger
                      onConfirm={async () => {
                        try {
                          await api.del(`${base}/chats/${c.id}`)
                          await refresh()
                          toastOK('已解除绑定')
                        } catch (e) {
                          toastError(e)
                        }
                      }}
                    >
                      <Button size="sm" variant="ghost" className="shrink-0">
                        解除
                      </Button>
                    </Confirm>
                  </ItemCard.Action>
                </ItemCard>
              </Fragment>
            ))}
          </ItemCardGroup>
        )}
      </div>
      <BindDialog binding={bindOpen ? tg.binding : null} now={now} onCancel={cancelBind} />
    </Panel>
  )
}

function PrefsPanel({ tg }: { tg: TelegramView }) {
  const qc = useQueryClient()
  const saved: Prefs = { remind_hours: tg.remind_hours, paid: tg.paid, claims: tg.claims }
  const [v, setV] = useState<Prefs>(saved)
  const dirty = v.remind_hours !== saved.remind_hours || v.paid !== saved.paid || v.claims !== saved.claims
  const rev = useRevision(tg.revision, !dirty)
  const save = useMutation({
    mutationFn: (overwrite: boolean) =>
      rev.run(async (revision) => {
        const r = await api.put<TelegramView>(base, { token: '', ...v }, { revision })
        qc.setQueryData(telegramKey, r)
        return r.revision
      }, overwrite),
    onSuccess: (saved) => saved && toastOK('已保存'),
    onError: toastError,
  })
  return (
    <Panel title="发哪些消息">
      <Form
        className="flex flex-col gap-5"
        onSubmit={(e) => {
          e.preventDefault()
          save.mutate(false)
        }}
      >
        <div className="divide-y divide-separator">
          <SettingsRow
            label="告警"
            description="监控页发现的问题：开始、笔数变多、恢复时各发一次。一直发，不能关：配好 Telegram 后宿主机巡检不再重复发这些告警。"
          >
            <div className="flex items-end gap-3">
              <NumberInput
                label="一直没恢复时，每隔多久再提醒"
                className="w-56"
                isRequired
                minValue={0}
                maxValue={24}
                value={Number.isNaN(v.remind_hours) ? undefined : v.remind_hours}
                onChange={(n) => setV((x) => ({ ...x, remind_hours: n ?? NaN }))}
              />
              <span className="pb-2.5 text-sm text-muted">小时</span>
            </div>
            <Typography.Paragraph size="xs" color="muted" className="-mt-1">
              默认 <span className="tb-num">1</span> 小时，可设 <span className="tb-num">0–24</span>，填 0
              表示只在开始和恢复时各发一次。
            </Typography.Paragraph>
          </SettingsRow>
          <SettingsRow
            label="收款到账"
            description="订单确认到账时发：实付金额、商户、渠道、商品名、订单号。不带付款人信息。"
          >
            <SwitchInput label="发送" value={v.paid} onChange={(paid) => setV((x) => ({ ...x, paid }))} />
          </SettingsRow>
          <SettingsRow
            label="付款申诉"
            description="付款人在自助页提交申诉时发：申诉类型、申报金额、订单号。付款人留的联系方式和说明不发，到“AI 与付款申诉”页看。"
          >
            <SwitchInput label="发送" value={v.claims} onChange={(claims) => setV((x) => ({ ...x, claims }))} />
          </SettingsRow>
        </div>
        {rev.conflict && (
          <ConflictNotice
            what="发哪些消息"
            onReload={async () => {
              const l = await qc.fetchQuery({ ...telegramQuery, staleTime: 0 })
              setV({ remind_hours: l.remind_hours, paid: l.paid, claims: l.claims })
            }}
            onOverwrite={() => save.mutateAsync(true)}
          />
        )}
        <div className="flex flex-wrap items-center justify-end gap-2">
          <Button size="sm" variant="tertiary" isDisabled={!dirty} onPress={() => setV(saved)}>
            撤销修改
          </Button>
          <Button size="sm" type="submit" isDisabled={!dirty} isPending={save.isPending}>
            保存
          </Button>
        </div>
      </Form>
    </Panel>
  )
}

// QRCode 把链接画成二维码。白底黑点，深色模式下也能扫。
function QRCode({ text }: { text: string }) {
  const { size, path } = useMemo(() => {
    const qr = encode(text, { ecc: 'M', border: 2 })
    let path = ''
    qr.data.forEach((row, y) =>
      row.forEach((on, x) => {
        if (on) path += `M${x} ${y}h1v1h-1z`
      }),
    )
    return { size: qr.size, path }
  }, [text])
  return (
    <svg
      viewBox={`0 0 ${size} ${size}`}
      shapeRendering="crispEdges"
      role="img"
      aria-label="绑定链接的二维码"
      className="size-44 rounded-xl bg-white"
    >
      <path d={path} fill="#000" />
    </svg>
  )
}

function BindDialog({ binding, now, onCancel }: { binding: Binding | null; now: number; onCancel: () => void }) {
  return (
    <Dialog
      title="添加接收人"
      size="sm"
      open={binding !== null}
      onClose={onCancel}
      footer={
        <Button variant="tertiary" onPress={onCancel}>
          取消绑定
        </Button>
      }
    >
      {binding && (
        <div className="flex flex-col items-center gap-4 text-center">
          <Typography.Paragraph size="sm" color="muted" align="center">
            用要接收通知的 Telegram
            账号打开这个链接（手机可以直接扫码），在机器人对话里点“开始”（Start）就绑好了。只支持和机器人单聊。
          </Typography.Paragraph>
          <QRCode text={binding.link} />
          <Button size="sm" onPress={() => window.open(binding.link, '_blank', 'noopener,noreferrer')}>
            <ArrowUpRightFromSquare />在 Telegram 中打开
          </Button>
          <div className="max-w-full text-xs break-all text-muted">
            <Copyable text={binding.link} mono>
              {binding.link}
            </Copyable>
          </div>
          {binding.error ? (
            <Notice status="danger" className="w-full text-left">
              {binding.error}
            </Notice>
          ) : (
            <StatusDot tone="accent" pulse>
              正在等对方点“开始”，链接 {relative(binding.expires_at, now)}失效
            </StatusDot>
          )}
        </div>
      )}
    </Dialog>
  )
}
