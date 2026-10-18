import { useEffect, useState, type ComponentType, type SVGProps } from 'react'
import { Link, Outlet, createFileRoute, redirect, useNavigate, useRouterState } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Avatar, Button, Chip, Drawer, Dropdown, Form, Header, Label, Separator, Tooltip } from '@yreidev/ui'
import {
  Archive,
  ArrowRightArrowLeft,
  ArrowRightFromSquare,
  ArrowUpRightFromSquare,
  Bars,
  Bell,
  Briefcase,
  ChartColumn,
  ChevronsExpandVertical,
  CreditCard,
  Display,
  FaceRobot,
  Flask,
  Gear,
  HeartPulse,
  Key,
  LayoutSideContentLeft,
  Moon,
  Receipt,
  Sun,
} from '@gravity-ui/icons'
import { Logo } from '../../components/brand'
import { ApiError, api, casesCountQuery, meQuery, overviewQuery } from '../../lib/api'
import { setThemePref, useThemePref, type ThemePref } from '../../lib/theme'
import { Dialog, TextInput, cx, toastError, toastOK, type Tone } from '../../lib/ui'

export const Route = createFileRoute('/admin')({
  beforeLoad: async ({ context, location }) => {
    try {
      await context.queryClient.ensureQueryData(meQuery)
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) {
        throw redirect({ to: '/login', search: { redirect: location.href } })
      }
      throw e
    }
  },
  component: AdminLayout,
})

type Icon = ComponentType<SVGProps<SVGSVGElement>>
type Badge = 'anomalies' | 'notifications' | 'cases' | 'alerts'

interface NavItem {
  to: string
  text: string
  icon: Icon
  /** 菜单右侧的待办数：未处理的异常、放弃了的通知、等人工的申诉、没恢复的告警 */
  badge?: Badge
}

// 菜单按“每天看的、配置的、偶尔用的”分三组，每项图标不重复（docs/ui.md）。
const groups: { title: string; items: NavItem[] }[] = [
  {
    title: '收款',
    items: [
      { to: '/admin', text: '概览', icon: ChartColumn },
      { to: '/admin/orders', text: '订单', icon: Receipt },
      { to: '/admin/ledger', text: '流水与异常', icon: ArrowRightArrowLeft, badge: 'anomalies' },
      { to: '/admin/notifications', text: '通知', icon: Bell, badge: 'notifications' },
      { to: '/admin/assistance', text: 'AI 与付款申诉', icon: FaceRobot, badge: 'cases' },
    ],
  },
  {
    title: '配置',
    items: [
      { to: '/admin/channels', text: '支付渠道', icon: CreditCard },
      { to: '/admin/merchants', text: '商户', icon: Briefcase },
      { to: '/admin/settings', text: '设置', icon: Gear },
    ],
  },
  {
    title: '工具',
    items: [
      { to: '/admin/monitor', text: '监控', icon: HeartPulse, badge: 'alerts' },
      { to: '/admin/payment-test', text: '支付测试', icon: Flask },
      { to: '/admin/operations', text: '数据归档', icon: Archive },
    ],
  },
]

const badgeTone: Record<Badge, Tone> = {
  anomalies: 'warning',
  notifications: 'danger',
  cases: 'warning',
  alerts: 'danger',
}

const themes: { value: ThemePref; text: string; icon: Icon }[] = [
  { value: 'system', text: '跟随系统', icon: Display },
  { value: 'light', text: '浅色', icon: Sun },
  { value: 'dark', text: '深色', icon: Moon },
]

const narrowQuery = '(max-width: 1023px)'

function useNarrow() {
  const [narrow, setNarrow] = useState(() => window.matchMedia(narrowQuery).matches)
  useEffect(() => {
    const mq = window.matchMedia(narrowQuery)
    const on = () => setNarrow(mq.matches)
    mq.addEventListener('change', on)
    return () => mq.removeEventListener('change', on)
  }, [])
  return narrow
}

const collapsedKey = 'tillbox-nav-collapsed'

function readCollapsed() {
  try {
    return localStorage.getItem(collapsedKey) === '1'
  } catch {
    return false
  }
}

// useNavCounts 取菜单角标的数字。概览接口和概览页共用缓存，申诉数只取计数。
function useNavCounts(): Record<Badge, number> {
  const overview = useQuery({ ...overviewQuery(7), refetchInterval: 30_000 })
  const cases = useQuery({ ...casesCountQuery, refetchInterval: 30_000 })
  return {
    anomalies: overview.data?.open_anomalies ?? 0,
    notifications: overview.data?.dead_notifications ?? 0,
    cases: cases.data?.counts?.manual ?? 0,
    alerts: overview.data?.open_alerts ?? 0,
  }
}

function AdminLayout() {
  const { data: me } = useQuery(meQuery)
  const path = useRouterState({ select: (s) => s.location.pathname })
  const narrow = useNarrow()
  const theme = useThemePref()
  const [collapsed, setCollapsed] = useState(readCollapsed)
  const [pwOpen, setPwOpen] = useState(false)
  const [navOpen, setNavOpen] = useState(false)
  const counts = useNavCounts()
  // 换了页面或布局就收起抽屉菜单（在渲染时比较，避免 effect 里再设一次状态）
  const [navFor, setNavFor] = useState(`${path}|${narrow}`)
  if (navFor !== `${path}|${narrow}`) {
    setNavFor(`${path}|${narrow}`)
    setNavOpen(false)
  }

  const all = groups.flatMap((g) => g.items)
  const current = all.find((m) => path === m.to || (m.to !== '/admin' && path.startsWith(m.to + '/')))
  const site = me?.site_name || 'Tillbox'
  useEffect(() => {
    document.title = current ? `${current.text} · ${site}` : `${site} 后台`
  }, [current, site])

  const toggleCollapsed = () => {
    const next = !collapsed
    setCollapsed(next)
    try {
      localStorage.setItem(collapsedKey, next ? '1' : '0')
    } catch {
      // 存不下时只在本页生效
    }
  }
  const ThemeIcon = themes.find((t) => t.value === theme)?.icon ?? Display
  const sidebar = (rail: boolean) => (
    <Sidebar site={site} current={current?.to} counts={counts} rail={rail} onPassword={() => setPwOpen(true)} />
  )

  return (
    <div className="flex min-h-dvh bg-background">
      <a
        className="sr-only z-50 rounded-lg bg-surface px-3 py-2 shadow-overlay focus:not-sr-only focus:fixed focus:top-3 focus:left-3"
        href="#admin-content"
      >
        跳到主内容
      </a>
      {!narrow && (
        <aside
          className={cx(
            'sticky top-0 flex h-dvh shrink-0 flex-col shadow-[inset_-1px_0_0_var(--color-separator)] transition-[width] duration-200 motion-reduce:transition-none',
            collapsed ? 'w-16' : 'w-60',
          )}
        >
          {sidebar(collapsed)}
        </aside>
      )}
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="sticky top-0 z-30 flex h-14 shrink-0 items-center gap-2 border-b border-separator bg-background/85 px-3 backdrop-blur-md sm:px-5">
          <Button
            isIconOnly
            variant="ghost"
            size="sm"
            className="text-muted"
            aria-label={narrow ? '打开导航菜单' : collapsed ? '展开侧边栏' : '收起侧边栏'}
            onPress={narrow ? () => setNavOpen(true) : toggleCollapsed}
          >
            {narrow ? <Bars /> : <LayoutSideContentLeft />}
          </Button>
          <Separator orientation="vertical" className="h-5" />
          <h1 className="min-w-0 truncate px-1 text-base font-semibold sm:text-lg">{current?.text ?? '管理后台'}</h1>
          <span className="flex-1" />
          {me?.demo && (
            <Tooltip delay={200}>
              <Tooltip.Trigger aria-label="演示模式说明">
                <Chip size="sm" variant="soft" color="warning">
                  演示模式
                </Chip>
              </Tooltip.Trigger>
              <Tooltip.Content className="max-w-64">
                以 TILLBOX_DEMO=true 启动：可以建演示渠道，用模拟付款走通整个流程。
              </Tooltip.Content>
            </Tooltip>
          )}
          <Dropdown>
            <Button isIconOnly variant="ghost" size="sm" className="text-muted" aria-label="深浅色">
              <ThemeIcon />
            </Button>
            <Dropdown.Popover placement="bottom end" className="min-w-40">
              <Dropdown.Menu
                aria-label="深浅色"
                selectionMode="single"
                selectedKeys={new Set([theme])}
                onSelectionChange={(keys) => {
                  const v = [...(keys as Set<string>)][0] as ThemePref | undefined
                  if (v) setThemePref(v)
                }}
              >
                {themes.map((t) => (
                  <Dropdown.Item key={t.value} id={t.value} textValue={t.text}>
                    <t.icon className="size-4 shrink-0 text-muted" />
                    <Label>{t.text}</Label>
                    <Dropdown.ItemIndicator className="ms-auto" />
                  </Dropdown.Item>
                ))}
              </Dropdown.Menu>
            </Dropdown.Popover>
          </Dropdown>
        </header>
        <main
          id="admin-content"
          tabIndex={-1}
          className="mx-auto flex w-full max-w-7xl flex-1 flex-col px-4 pt-5 pb-12 outline-none sm:px-6"
        >
          <Outlet />
        </main>
      </div>
      <Drawer.Backdrop isOpen={navOpen && narrow} onOpenChange={setNavOpen}>
        <Drawer.Content placement="left">
          <Drawer.Dialog aria-label="导航菜单" className="w-[min(272px,85vw)] p-0 sm:w-[min(272px,85vw)]">
            {sidebar(false)}
          </Drawer.Dialog>
        </Drawer.Content>
      </Drawer.Backdrop>
      <PasswordModal open={pwOpen} onClose={() => setPwOpen(false)} />
    </div>
  )
}

// Sidebar 是侧栏内容（桌面侧栏和手机抽屉共用）：站点名、菜单、付款人自助入口和管理员菜单。rail 时只显示图标。
function Sidebar({
  site,
  current,
  counts,
  rail,
  onPassword,
}: {
  site: string
  current?: string
  counts: Record<Badge, number>
  rail: boolean
  onPassword: () => void
}) {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const logout = async () => {
    try {
      await api.post('/web-api/auth/logout')
    } finally {
      qc.clear()
      await navigate({ to: '/login', search: { redirect: undefined } })
    }
  }
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className={cx('flex h-14 shrink-0 items-center gap-2.5', rail ? 'justify-center' : 'px-4')}>
        <Logo size={28} title={rail ? site : undefined} />
        {!rail && (
          <div className="min-w-0 leading-tight">
            <div className="truncate text-sm font-semibold">{site}</div>
            <div className="text-xs text-muted">收款后台</div>
          </div>
        )}
      </div>
      <nav
        aria-label="后台菜单"
        className={cx('flex min-h-0 flex-1 flex-col overflow-y-auto pt-2 pb-3', rail ? 'px-2' : 'px-3')}
      >
        {groups.map((g, gi) => (
          <div key={g.title} className={gi > 0 ? (rail ? 'mt-2 border-t border-separator pt-2' : 'mt-5') : undefined}>
            {!rail && <div className="px-2.5 pb-1.5 text-xs font-medium text-muted">{g.title}</div>}
            <ul className="flex flex-col gap-0.5">
              {g.items.map((m) => (
                <li key={m.to}>
                  <NavLink item={m} active={current === m.to} rail={rail} count={m.badge ? counts[m.badge] : 0} />
                </li>
              ))}
            </ul>
          </div>
        ))}
      </nav>
      <div
        className={cx(
          'flex shrink-0 flex-col gap-0.5 border-t border-separator py-3',
          rail ? 'items-center px-2' : 'px-3',
        )}
      >
        <RailTip text="付款人自助（新标签页）" rail={rail}>
          <a
            href="/help"
            target="_blank"
            rel="noopener noreferrer"
            aria-label={rail ? '付款人自助（新标签页打开）' : undefined}
            className={navClass(false, rail)}
          >
            <ArrowUpRightFromSquare className="size-4 shrink-0 text-muted" />
            {!rail && (
              <>
                <span className="flex-1 truncate">付款人自助</span>
                <span className="text-xs text-muted">新标签页</span>
              </>
            )}
          </a>
        </RailTip>
        <Dropdown>
          <Button
            variant="ghost"
            aria-label="管理员菜单"
            isIconOnly={rail}
            className={cx('h-11 rounded-xl', rail ? 'size-11' : 'w-full justify-start gap-2.5 px-2')}
          >
            <Avatar size="sm" color="accent" variant="soft" className="size-7 shrink-0">
              <Avatar.Fallback>管</Avatar.Fallback>
            </Avatar>
            {!rail && (
              <>
                <span className="min-w-0 flex-1 text-left leading-tight">
                  <span className="block truncate text-sm font-medium">管理员</span>
                  <span className="block truncate text-xs font-normal text-muted">{site}</span>
                </span>
                <ChevronsExpandVertical className="size-4 shrink-0 text-muted" />
              </>
            )}
          </Button>
          <Dropdown.Popover placement={rail ? 'right bottom' : 'top start'} className="min-w-52">
            <Dropdown.Menu
              aria-label="管理员菜单"
              onAction={(key) => {
                if (key === 'password') onPassword()
                if (key === 'logout') void logout()
              }}
            >
              <Dropdown.Section>
                <Header>{site}</Header>
                <Dropdown.Item id="password" textValue="修改密码">
                  <Key className="size-4 shrink-0 text-muted" />
                  <Label>修改密码</Label>
                </Dropdown.Item>
              </Dropdown.Section>
              <Separator />
              <Dropdown.Item id="logout" textValue="退出登录" variant="danger">
                <ArrowRightFromSquare className="size-4 shrink-0 text-danger" />
                <Label>退出登录</Label>
              </Dropdown.Item>
            </Dropdown.Menu>
          </Dropdown.Popover>
        </Dropdown>
      </div>
    </div>
  )
}

const navClass = (active: boolean, rail: boolean) =>
  cx(
    'group relative flex h-9 items-center gap-3 rounded-xl text-sm transition-colors outline-none focus-visible:ring-2 focus-visible:ring-focus',
    rail ? 'w-11 justify-center' : 'px-2.5',
    active ? 'bg-default font-medium text-foreground' : 'text-foreground/85 hover:bg-default hover:text-foreground',
  )

// RailTip 在只显示图标时给菜单项加悬停提示。
function RailTip({ text, rail, children }: { text: string; rail: boolean; children: React.ReactElement }) {
  if (!rail) return children
  return (
    <Tooltip delay={0}>
      {/* Trigger 默认是可聚焦的按钮外壳；里面已经是链接，外壳不再占一次 Tab */}
      <Tooltip.Trigger className="block" tabIndex={-1} role="presentation">
        {children}
      </Tooltip.Trigger>
      <Tooltip.Content placement="right">{text}</Tooltip.Content>
    </Tooltip>
  )
}

function NavLink({ item, active, rail, count }: { item: NavItem; active: boolean; rail: boolean; count: number }) {
  const tone = item.badge ? badgeTone[item.badge] : 'default'
  const label = count > 0 ? `${item.text}（${count} 项待处理）` : item.text
  return (
    <RailTip text={label} rail={rail}>
      <Link
        to={item.to}
        aria-current={active ? 'page' : undefined}
        aria-label={rail || count > 0 ? label : undefined}
        className={navClass(active, rail)}
      >
        <item.icon
          className={cx('size-4 shrink-0', active ? 'text-accent' : 'text-muted group-hover:text-foreground')}
        />
        {!rail && <span className="flex-1 truncate">{item.text}</span>}
        {count > 0 &&
          (rail ? (
            <span
              className={cx(
                'absolute top-1.5 right-2 size-2 rounded-full ring-2 ring-background',
                tone === 'danger' ? 'bg-danger' : 'bg-warning',
              )}
            />
          ) : (
            <Chip size="sm" variant="soft" color={tone} className="tb-num h-5 min-w-5 justify-center px-1.5 text-xs">
              {count > 99 ? '99+' : count}
            </Chip>
          ))}
      </Link>
    </RailTip>
  )
}

function PasswordModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [busy, setBusy] = useState(false)
  const [v, setV] = useState({ old_password: '', new_password: '', confirm: '' })
  const close = () => {
    setV({ old_password: '', new_password: '', confirm: '' })
    onClose()
  }
  return (
    <Dialog title="修改密码" open={open} onClose={close} size="sm">
      <Form
        className="flex flex-col gap-4"
        onSubmit={async (e) => {
          e.preventDefault()
          setBusy(true)
          try {
            await api.post('/web-api/auth/password', { old_password: v.old_password, new_password: v.new_password })
            toastOK('密码已修改，其他设备上的登录已失效')
            close()
          } catch (err) {
            toastError(err)
          } finally {
            setBusy(false)
          }
        }}
      >
        <p className="text-sm text-muted">改完后，其他设备上的登录会失效，需要用新密码重新登录。</p>
        <TextInput
          label="原密码"
          type="password"
          autoComplete="current-password"
          isRequired
          value={v.old_password}
          onChange={(old_password) => setV((s) => ({ ...s, old_password }))}
          validate={(x) => (x ? null : '请输入原密码')}
        />
        <TextInput
          label="新密码"
          description="至少 8 位"
          type="password"
          autoComplete="new-password"
          isRequired
          value={v.new_password}
          onChange={(new_password) => setV((s) => ({ ...s, new_password }))}
          validate={(x) => (!x ? '请输入新密码' : x.length < 8 ? '至少 8 位' : null)}
        />
        <TextInput
          label="再输一次新密码"
          type="password"
          autoComplete="new-password"
          isRequired
          value={v.confirm}
          onChange={(confirm) => setV((s) => ({ ...s, confirm }))}
          validate={(x) => (x === v.new_password ? null : '两次输入不一致')}
        />
        <div className="flex justify-end gap-2 pt-1">
          <Button variant="tertiary" onPress={close}>
            取消
          </Button>
          <Button type="submit" isPending={busy}>
            保存新密码
          </Button>
        </div>
      </Form>
    </Dialog>
  )
}
