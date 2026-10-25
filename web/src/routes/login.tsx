import { useState } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { Button, Card, Form, Typography } from '@yreidev/ui'
import { Logo, siteName } from '../components/brand'
import { api } from '../lib/api'
import { Notice } from '../lib/ui-base'
import { TextInput } from '../lib/ui-form'

// 登录页：站点名加登录卡片。不写产品介绍，从登录页看不出这是什么系统。只有一个管理员，所以只输密码。

export const Route = createFileRoute('/login')({
  validateSearch: (s: Record<string, unknown>): { redirect?: string } => ({
    redirect: typeof s.redirect === 'string' && s.redirect.startsWith('/admin') ? s.redirect : undefined,
  }),
  component: Login,
})

function Login() {
  const { redirect } = Route.useSearch()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [password, setPassword] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const site = siteName()

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
    <main className="flex min-h-dvh flex-col items-center justify-center gap-6 px-4 py-10 sm:px-6">
      <div className="flex flex-col items-center gap-3 text-center">
        <Logo size={44} />
        <span className="text-lg font-semibold">{site}</span>
      </div>
      <Card className="w-full max-w-sm p-6 sm:p-8">
        <Card.Header className="gap-1">
          <Card.Title className="text-xl">登录后台</Card.Title>
          <Card.Description>输入管理员密码登录。</Card.Description>
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
          <Typography.Paragraph size="xs" color="muted">
            忘记密码？请联系站点维护人员恢复访问。
          </Typography.Paragraph>
        </Card.Content>
      </Card>
    </main>
  )
}
