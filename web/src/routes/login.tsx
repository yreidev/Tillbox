import { useState } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { Button, Card, Form, Link } from '@heroui/react'
import { ArrowRightArrowLeft, Bell, CircleQuestion, FaceRobot } from '@gravity-ui/icons'
import { Logo } from '../components/brand'
import { api } from '../lib/api'
import { Notice } from '../lib/ui-base'
import { TextInput } from '../lib/ui-form'

// 登录页：宽屏左边是产品说明，右边是登录卡片；手机上只有卡片。只有一个管理员，所以只输密码。

export const Route = createFileRoute('/login')({
  validateSearch: (s: Record<string, unknown>): { redirect?: string } => ({
    redirect: typeof s.redirect === 'string' && s.redirect.startsWith('/admin') ? s.redirect : undefined,
  }),
  component: Login,
})

const features = [
  { icon: ArrowRightArrowLeft, title: '自动对账', text: '从支付宝账单或上游渠道确认到账，对不上的进入异常集中处理。' },
  { icon: Bell, title: '通知商户', text: '到账后通知商户，没送达就按间隔重试，共尝试 8 次。' },
  { icon: FaceRobot, title: '付款申诉', text: '付款人自助提交问题，AI 先核查，拿不准的转人工。' },
]

function Login() {
  const { redirect } = Route.useSearch()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [password, setPassword] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async () => {
    setBusy(true)
    setErr('')
    try {
      await api.post('/web-api/auth/login', { password })
      qc.clear()
      await navigate({ href: redirect ?? '/admin', replace: true })
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="grid min-h-dvh lg:grid-cols-2">
      <aside className="relative hidden flex-col justify-between overflow-hidden border-r border-separator bg-surface p-12 lg:flex">
        <div
          aria-hidden
          className="pointer-events-none absolute inset-0 [background-image:radial-gradient(var(--separator)_1px,transparent_1px)] [mask-image:linear-gradient(to_bottom,black,transparent_75%)] [background-size:22px_22px]"
        />
        <div className="relative flex items-center gap-3">
          <Logo size={36} />
          <span className="text-lg font-semibold">Tillbox</span>
        </div>
        <div className="relative max-w-md">
          <p className="text-sm font-medium text-accent">收款管理工作台</p>
          <h2 className="mt-3 text-3xl leading-snug font-semibold tracking-tight">订单、到账和通知，在一处核对。</h2>
          <ul className="mt-10 flex flex-col gap-6">
            {features.map((f) => (
              <li key={f.title} className="flex gap-4">
                <span className="grid size-10 shrink-0 place-items-center rounded-xl bg-accent-soft text-accent-soft-foreground">
                  <f.icon className="size-5" />
                </span>
                <span>
                  <span className="block font-medium">{f.title}</span>
                  <span className="mt-0.5 block text-sm text-muted">{f.text}</span>
                </span>
              </li>
            ))}
          </ul>
        </div>
        <p className="relative text-xs text-muted">Tillbox · 订单与收款管理</p>
      </aside>
      <main className="flex flex-col items-center justify-center gap-6 px-4 py-10 sm:px-6">
        <div className="flex flex-col items-center gap-3 text-center lg:hidden">
          <Logo size={44} />
          <span className="text-lg font-semibold">Tillbox</span>
        </div>
        <Card className="w-full max-w-sm p-6 sm:p-8">
          <Card.Header className="gap-1">
            <Card.Title className="text-xl">登录后台</Card.Title>
            <Card.Description>登录以管理订单、收款与付款人申诉。</Card.Description>
          </Card.Header>
          <Card.Content className="mt-2 gap-4">
            {err && <Notice status="danger">{err}</Notice>}
            <Form
              className="flex flex-col gap-5"
              onSubmit={(e) => {
                e.preventDefault()
                void submit()
              }}
            >
              <TextInput
                label="管理员密码"
                type="password"
                autoComplete="current-password"
                autoFocus
                isRequired
                value={password}
                onChange={setPassword}
                validate={(v) => (v ? null : '请输入密码')}
              />
              <Button type="submit" size="lg" fullWidth isPending={busy}>
                登录
              </Button>
            </Form>
            <p className="text-xs text-muted">忘记密码？请联系站点维护人员恢复访问。</p>
          </Card.Content>
        </Card>
        <Link href="/help" className="gap-1.5 text-sm">
          <CircleQuestion className="size-4" />
          付款遇到问题？前往付款人自助
        </Link>
      </main>
    </div>
  )
}
