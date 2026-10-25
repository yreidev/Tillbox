import type { QueryClient } from '@tanstack/react-query'
import { Outlet, createRootRouteWithContext } from '@tanstack/react-router'
import { Button, I18nProvider, Link, Toast, Typography } from '@yreidev/ui'
import { Logo } from '../components/brand'

export const Route = createRootRouteWithContext<{ queryClient: QueryClient }>()({
  component: Root,
  notFoundComponent: NotFound,
})

// 日期、数字组件按中文显示；全站的提示消息从右上角弹出。
function Root() {
  return (
    <I18nProvider locale="zh-CN">
      <Outlet />
      <Toast.Provider placement="top end" />
    </I18nProvider>
  )
}

function NotFound() {
  return (
    <main className="grid min-h-dvh place-items-center p-6">
      <div className="flex max-w-sm flex-col items-center gap-3 text-center">
        <Logo size={56} />
        <Typography.Heading level={1} align="center" className="mt-2 text-xl">
          页面不存在
        </Typography.Heading>
        <Typography.Paragraph size="sm" color="muted" align="center">
          链接可能写错了，或者页面已经移走。
        </Typography.Paragraph>
        <Button className="mt-2" onPress={() => window.location.assign('/help')}>
          前往付款自助
        </Button>
        <Typography.Paragraph size="sm" color="muted" align="center" className="mt-2">
          管理员请前往 <Link href="/admin">后台</Link>
        </Typography.Paragraph>
      </div>
    </main>
  )
}
