import { useCallback, useEffect, useState, type ComponentType, type SVGProps } from 'react'
import { Outlet, createFileRoute, redirect, useNavigate, useRouterState } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  AppLayout,
  Avatar,
  Badge,
  Button,
  Chip,
  Dropdown,
  Form,
  Header,
  Label,
  Navbar,
  Separator,
  Sidebar,
  Tooltip,
  Typography,
  useSidebar,
} from '@yreidev/ui'
import {
  Archive,
  ArrowRightArrowLeft,
  ArrowUpFromLine,
  ArrowRightFromSquare,
  ArrowUpRightFromSquare,
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
  Moon,
  Receipt,
  Sun,
} from '@gravity-ui/icons'
import { Logo } from '../../components/brand'
import { ApiError, api, casesCountQuery, meQuery, overviewQuery } from '../../lib/api'
import { setThemePref, useThemePref, type ThemePref } from '../../lib/theme'
import { Dialog, TextInput, cx, enter, toastError, toastOK, type Tone } from '../../lib/ui'

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
type Todo = 'anomalies' | 'notifications' | 'cases' | 'alerts'

interface NavItem {
  to: string
  text: string
  icon: Icon
  /** 菜单右侧的待办数：未处理的异常、放弃了的通知、等人工的申诉、没恢复的告警 */
  badge?: Todo
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
      { to: '/admin/operations', text: '备份与归档', icon: Archive },
    ],
  },
]

const badgeTone: Record<Todo, Tone> = {
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

const collapsedKey = 'tillbox-nav-collapsed'

function readCollapsed() {
  try {
    return localStorage.getItem(collapsedKey) === '1'
  } catch {
    return false
  }
}

function saveCollapsed(collapsed: boolean) {
  try {
    localStorage.setItem(collapsedKey, collapsed ? '1' : '0')
  } catch {
    // 存不下时只在本页生效
  }
}

// useNavCounts 取菜单角标的数字。概览接口和概览页共用缓存，申诉数只取计数。
function useNavCounts(): Record<Todo, number> {
  const overview = useQuery({ ...overviewQuery(7), refetchInterval: 30_000 })
  const cases = useQuery({ ...casesCountQuery, refetchInterval: 30_000 })
  return {
    anomalies: overview.data?.open_anomalies ?? 0,
    notifications: overview.data?.dead_notifications ?? 0,
    cases: cases.data?.counts?.manual ?? 0,
    alerts: overview.data?.open_alerts ?? 0,
  }
}

// 后台外壳用组件库的 AppLayout：桌面是可收成图标栏的侧栏（Ctrl/⌘+B 也能切换），手机宽度收进抽屉。
function AdminLayout() {
  const { data: me } = useQuery(meQuery)
  const path = useRouterState({ select: (s) => s.location.pathname })
  const navigate = useNavigate()
  const [pwOpen, setPwOpen] = useState(false)
  const counts = useNavCounts()

  const all = groups.flatMap((g) => g.items)
  const current = all.find((m) => path === m.to || (m.to !== '/admin' && path.startsWith(m.to + '/')))
  const site = me?.site_name || 'Tillbox'
  useEffect(() => {
    document.title = current ? `${current.text} · ${site}` : `${site} 后台`
  }, [current, site])

  const go = useCallback((href: string) => void navigate({ href }), [navigate])
  const sidebar = {
    site,
    current: current?.to,
    counts,
    version: me?.version ?? '',
    newVersion: me?.new_version ?? '',
    onPassword: () => setPwOpen(true),
  }

  return (
    <>
      <a
        className="sr-only z-50 rounded-lg bg-surface px-3 py-2 shadow-overlay focus:not-sr-only focus:fixed focus:top-3 focus:left-3"
        href="#admin-content"
      >
        跳到主内容
      </a>
      <AppLayout
        navigate={go}
        defaultSidebarOpen={!readCollapsed()}
        onSidebarOpenChange={(open) => saveCollapsed(!open)}
        navbar={<AdminNavbar title={current?.text ?? '管理后台'} demo={!!me?.demo} />}
        sidebar={
          <>
            <Sidebar aria-label="后台菜单">
              <SidebarBody {...sidebar} />
            </Sidebar>
            <Sidebar.Mobile aria-label="后台菜单">
              <SidebarBody {...sidebar} mobile />
            </Sidebar.Mobile>
          </>
        }
      >
        <div
          id="admin-content"
          tabIndex={-1}
          className="mx-auto flex w-full max-w-7xl flex-1 flex-col px-4 pt-5 pb-12 outline-none sm:px-6"
        >
          {/* 换页时正文淡入；同一页里换筛选、换标签不重播 */}
          <div key={path} className={cx('flex flex-1 flex-col', enter)}>
            <Outlet />
          </div>
        </div>
      </AppLayout>
      <PasswordModal open={pwOpen} onClose={() => setPwOpen(false)} />
    </>
  )
}

// AdminNavbar 是顶栏：手机上的菜单按钮、侧栏开关、页面标题，右边是演示模式标记和深浅色切换。
function AdminNavbar({ title, demo }: { title: string; demo: boolean }) {
  const theme = useThemePref()
  const ThemeIcon = themes.find((t) => t.value === theme)?.icon ?? Display
  return (
    <Navbar maxWidth="full" height="3.5rem" className="border-b border-separator bg-background/85 backdrop-blur-md">
      <Navbar.Header>
        <AppLayout.MenuToggle aria-label="打开导航菜单" className="text-muted" />
        <Sidebar.Trigger aria-label="展开或收起侧边栏" className="text-muted" />
        <Separator orientation="vertical" className="h-5 max-md:hidden" />
        <Typography.Heading level={1} truncate className="min-w-0 px-1 text-base sm:text-lg">
          {title}
        </Typography.Heading>
        <Navbar.Spacer />
        <Navbar.Content>
          {demo && (
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
        </Navbar.Content>
      </Navbar.Header>
    </Navbar>
  )
}

// SidebarBody 是侧栏内容（桌面侧栏和手机抽屉各渲染一份）：站点名、菜单、付款人自助入口和管理员菜单。
// 手机抽屉里的文字不带 data-sidebar="label"，桌面侧栏收成图标时才隐藏这些文字。
function SidebarBody({
  site,
  current,
  counts,
  version,
  newVersion,
  onPassword,
  mobile,
}: {
  site: string
  current?: string
  counts: Record<Todo, number>
  version: string
  newVersion: string
  onPassword: () => void
  mobile?: boolean
}) {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const label = mobile ? undefined : 'label'
  const id = (key: string) => (mobile ? `m-${key}` : key)
  const logout = async () => {
    try {
      await api.post('/web-api/auth/logout')
    } finally {
      qc.clear()
      await navigate({ to: '/login', search: { redirect: undefined } })
    }
  }
  return (
    <>
      <Sidebar.Header>
        <div className="flex items-center gap-2.5 px-1 py-1">
          <Logo size={28} title={site} />
          <div className="min-w-0 leading-tight" data-sidebar={label}>
            <div className="truncate text-sm font-semibold">{site}</div>
            <div className="text-xs text-muted">收款后台</div>
          </div>
        </div>
      </Sidebar.Header>
      <Sidebar.Content>
        {groups.map((g) => (
          <Sidebar.Group key={g.title}>
            <Sidebar.GroupLabel>{g.title}</Sidebar.GroupLabel>
            <Sidebar.Menu aria-label={g.title}>
              {g.items.map((m) => (
                <NavLink
                  key={m.to}
                  id={id(m.to)}
                  item={m}
                  active={current === m.to}
                  count={m.badge ? counts[m.badge] : 0}
                />
              ))}
            </Sidebar.Menu>
          </Sidebar.Group>
        ))}
      </Sidebar.Content>
      <Sidebar.Footer>
        <Sidebar.Menu aria-label="付款人入口">
          <Sidebar.MenuItem
            id={id('help')}
            href="/help"
            target="_blank"
            textValue="付款人自助（新标签页打开）"
            tooltip="付款人自助（新标签页）"
          >
            <Sidebar.MenuIcon>
              <ArrowUpRightFromSquare className="size-4" />
            </Sidebar.MenuIcon>
            <Sidebar.MenuLabel>付款人自助</Sidebar.MenuLabel>
            <Sidebar.MenuChip>
              <span className="text-xs text-muted">新标签页</span>
            </Sidebar.MenuChip>
          </Sidebar.MenuItem>
          {newVersion && (
            <Sidebar.MenuItem
              id={id('update')}
              className={enter}
              href="/admin/settings?tab=site"
              textValue={`有新版本 ${newVersion}`}
              tooltip={`有新版本 ${newVersion}`}
            >
              <Sidebar.MenuIcon>
                <ArrowUpFromLine className="size-4 text-accent" />
              </Sidebar.MenuIcon>
              <Sidebar.MenuLabel>有新版本</Sidebar.MenuLabel>
              <Sidebar.MenuChip>
                <span className="tb-num text-xs text-accent">{newVersion}</span>
              </Sidebar.MenuChip>
            </Sidebar.MenuItem>
          )}
        </Sidebar.Menu>
        <Dropdown>
          <Dropdown.Trigger
            aria-label="管理员菜单"
            className="flex w-full items-center gap-2.5 rounded-xl px-1 py-1.5 text-left outline-none hover:bg-default focus-visible:ring-2 focus-visible:ring-focus"
          >
            <Avatar size="sm" color="accent" variant="soft" className="size-7 shrink-0">
              <Avatar.Fallback>管</Avatar.Fallback>
            </Avatar>
            <span className="min-w-0 flex-1 leading-tight" data-sidebar={label}>
              <span className="block truncate text-sm font-medium">管理员</span>
              <span className="block truncate text-xs text-muted">{site}</span>
            </span>
            <ChevronsExpandVertical className="size-4 shrink-0 text-muted" data-sidebar={label} />
          </Dropdown.Trigger>
          <Dropdown.Popover placement="top start" className="min-w-52">
            <Dropdown.Menu
              aria-label="管理员菜单"
              onAction={(key) => {
                if (key === 'password') onPassword()
                if (key === 'logout') void logout()
              }}
            >
              <Dropdown.Section>
                <Header>
                  {site}
                  {version && <span className="tb-num ms-1.5 font-normal text-muted">版本 {version}</span>}
                </Header>
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
      </Sidebar.Footer>
    </>
  )
}

// NavLink 是一项菜单。有待办时右边显示数字；侧栏收成图标时数字藏起来，改在图标右上角放一个圆点。
function NavLink({ id, item, active, count }: { id: string; item: NavItem; active: boolean; count: number }) {
  const { isOpen, isMobile } = useSidebar()
  const tone = item.badge ? badgeTone[item.badge] : 'default'
  const label = count > 0 ? `${item.text}（${count} 项待处理）` : item.text
  const color = tone === 'danger' ? 'danger' : 'warning'
  return (
    <Sidebar.MenuItem id={id} href={item.to} isCurrent={active} textValue={label} tooltip={label}>
      <Sidebar.MenuIcon>
        {count > 0 && !isOpen && !isMobile ? (
          <Badge.Anchor>
            <item.icon className="size-4" />
            <Badge color={color} placement="top-right" size="sm" />
          </Badge.Anchor>
        ) : (
          <item.icon className="size-4" />
        )}
      </Sidebar.MenuIcon>
      <Sidebar.MenuLabel>{item.text}</Sidebar.MenuLabel>
      {count > 0 && (
        <Sidebar.MenuChip>
          <Chip size="sm" variant="soft" color={tone} className="tb-num h-5 min-w-5 justify-center px-1.5 text-xs">
            {count > 99 ? '99+' : count}
          </Chip>
        </Sidebar.MenuChip>
      )}
    </Sidebar.MenuItem>
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
        <Typography.Paragraph size="sm" color="muted">
          改完后，其他设备上的登录会失效，需要用新密码重新登录。
        </Typography.Paragraph>
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
