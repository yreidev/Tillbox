// 对外页面和后台都用的基础小组件：提示条、标签、卡片、步骤圆点、复制。只依赖少量 HeroUI 组件，
// 收银台只引这个文件，不会连带下载后台的表格、分页这些组件。规范见 docs/ui.md。
import type { ReactNode } from 'react'
import { Alert, Button, Card, Chip, linkVariants, toast, type Key } from '@yreidev/ui'
import { CircleCheckFill, CircleXmarkFill, Copy } from '@gravity-ui/icons'

/** cx 拼接 className，跳过空值。 */
export const cx = (...c: (string | false | null | undefined)[]) => c.filter(Boolean).join(' ')

export function toastError(e: unknown) {
  toast.danger((e as Error)?.message || '操作失败', { timeout: 6000 })
}

export function toastOK(msg: string) {
  toast.success(msg, { timeout: 2500 })
}

/** copyText 复制到剪贴板；http 页面没有 Clipboard API，退回到选中文字再复制。quiet 时成功不弹提示，由调用方自己显示。 */
export async function copyText(text: string, quiet = false): Promise<boolean> {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text)
    } else {
      const el = document.createElement('textarea')
      el.value = text
      el.setAttribute('readonly', '')
      el.style.position = 'fixed'
      el.style.opacity = '0'
      document.body.appendChild(el)
      el.select()
      const ok = document.execCommand('copy')
      el.remove()
      if (!ok) throw new Error('copy failed')
    }
    if (!quiet) toastOK('已复制')
    return true
  } catch {
    toastError(new Error('复制失败，请手动选中后复制'))
    return false
  }
}

/** Copyable 显示一段可复制的文字；mono 用等宽字体（订单号、密钥）。 */
export function Copyable({ text, children, mono }: { text: string; children?: ReactNode; mono?: boolean }) {
  return (
    <span className="inline-flex max-w-full items-center gap-0.5 align-middle">
      <span className={cx('min-w-0 break-all', mono && 'tb-mono')}>{children ?? text}</span>
      <Button
        isIconOnly
        size="sm"
        variant="ghost"
        aria-label="复制"
        className="size-7 min-w-7 shrink-0 text-muted"
        onPress={() => void copyText(text)}
      >
        <Copy className="size-3.5" />
      </Button>
    </span>
  )
}

export type Tone = 'default' | 'accent' | 'success' | 'warning' | 'danger'

/** Pill 是状态、类型这类小标签。 */
export function Pill({
  tone = 'default',
  children,
  className,
}: {
  tone?: Tone
  children: ReactNode
  className?: string
}) {
  return (
    <Chip size="sm" variant="soft" color={tone} className={cx('shrink-0', className)}>
      {children}
    </Chip>
  )
}

// HeroUI 的 Alert 默认白底加阴影，放进卡片和抽屉里就看不出来；统一换成对应状态的浅色底。
const noticeBg: Record<Tone, string> = {
  default: 'bg-default',
  accent: 'bg-accent-soft',
  success: 'bg-success-soft',
  warning: 'bg-warning-soft',
  danger: 'bg-danger-soft',
}

/** Notice 是页面里的提示条：成功、警告、出错，或一段说明（accent）。 */
export function Notice({
  status = 'accent',
  title,
  children,
  action,
  className,
}: {
  status?: Tone
  title?: ReactNode
  children?: ReactNode
  action?: ReactNode
  className?: string
}) {
  return (
    <Alert status={status} className={cx('rounded-2xl shadow-none', noticeBg[status], className)}>
      <Alert.Indicator />
      <Alert.Content>
        {title && <Alert.Title>{title}</Alert.Title>}
        {children && <Alert.Description>{children}</Alert.Description>}
      </Alert.Content>
      {action}
    </Alert>
  )
}

export const toneDot: Record<Tone, string> = {
  default: 'bg-default-foreground/40',
  accent: 'bg-accent',
  success: 'bg-success',
  warning: 'bg-warning',
  danger: 'bg-danger',
}

/**
 * Panel 是页面上的一块内容：标题、一句说明、右上角的操作，下面是正文。
 * flush 让表格、列表贴满卡片左右边缘（标题仍留内边距）。
 */
export function Panel({
  title,
  description,
  extra,
  children,
  className,
  flush,
}: {
  title?: ReactNode
  description?: ReactNode
  extra?: ReactNode
  children?: ReactNode
  className?: string
  flush?: boolean
}) {
  return (
    <Card className={cx('min-w-0 rounded-2xl', flush && 'gap-0 overflow-hidden p-0', className)}>
      {(title || extra) && (
        <Card.Header
          className={cx('flex-row flex-wrap items-start justify-between gap-x-4 gap-y-2', flush && 'px-4 pt-4 pb-3')}
        >
          <div className="min-w-0">
            {typeof title === 'string' ? <Card.Title className="text-base font-semibold">{title}</Card.Title> : title}
            {description && <Card.Description className="mt-0.5">{description}</Card.Description>}
          </div>
          {extra && <div className="flex flex-wrap items-center gap-2">{extra}</div>}
        </Card.Header>
      )}
      <Card.Content className="min-w-0">{children}</Card.Content>
    </Card>
  )
}

/** StatusDot 是一个带颜色圆点的状态短语，比标签更轻，适合列表里大量出现的状态。 */
export function StatusDot({
  tone = 'default',
  children,
  pulse,
}: {
  tone?: Tone
  children: ReactNode
  pulse?: boolean
}) {
  return (
    <span className="inline-flex items-center gap-1.5 text-sm">
      <span className="relative flex size-2 shrink-0">
        {pulse && (
          <span className={cx('absolute inline-flex size-full animate-ping rounded-full opacity-60', toneDot[tone])} />
        )}
        <span className={cx('relative inline-flex size-2 rounded-full', toneDot[tone])} />
      </span>
      {children}
    </span>
  )
}

/** linkClass 给路由的 <Link> 套上 HeroUI 链接的样式。 */
export const linkClass = linkVariants().base()

export type StepStatus = 'done' | 'current' | 'wait' | 'error'

/** stepDot 是步骤条每一步前面的圆点：完成打勾、进行中实心、未开始空心、出错打叉。 */
export function stepDot(status: StepStatus) {
  switch (status) {
    case 'done':
      return <CircleCheckFill className="size-5 text-success" />
    case 'error':
      return <CircleXmarkFill className="size-5 text-danger" />
    case 'current':
      return <span className="size-3 rounded-full bg-accent ring-4 ring-accent-soft" />
    default:
      return <span className="size-3 rounded-full border-2 border-border" />
  }
}

export interface TimelineItem {
  key: Key
  title: ReactNode
  /** 标题右侧的小字（时间） */
  meta?: ReactNode
  children?: ReactNode
  tone?: Tone
  /** 替换默认的圆点，比如 stepDot(...) */
  dot?: ReactNode
}

/** Timeline 是竖排的步骤或事件记录，用一条细线把圆点串起来。 */
export function Timeline({ items, className }: { items: TimelineItem[]; className?: string }) {
  return (
    <ol className={cx('flex flex-col', className)}>
      {items.map((it, i) => (
        <li key={it.key} className="relative flex gap-3 pb-5 last:pb-0">
          {i < items.length - 1 && (
            <span aria-hidden className="absolute top-7 bottom-1 left-[11.5px] w-px bg-separator" />
          )}
          <span className="grid size-6 shrink-0 place-items-center">
            {it.dot ?? <span className={cx('size-2.5 rounded-full', toneDot[it.tone ?? 'default'])} />}
          </span>
          <div className="min-w-0 flex-1 pt-0.5">
            <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5">
              <span className="text-sm font-medium">{it.title}</span>
              {it.meta && <span className="tb-num text-xs text-muted">{it.meta}</span>}
            </div>
            {it.children && <div className="mt-1 text-sm text-muted">{it.children}</div>}
          </div>
        </li>
      ))}
    </ol>
  )
}
