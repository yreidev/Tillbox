// 页面共用的小组件和工具：把 HeroUI 组件常用的组合方式包一层，页面里直接用，不重复拼装。规范见 docs/ui.md。
// 基础组件在 ui-base.tsx、表单控件在 ui-form.tsx，这里一并导出；对外页面直接引那两个文件，少下载后台专用的组件。
import { useEffect, useState, type ReactElement, type ReactNode } from 'react'
import {
  AlertDialog,
  Button,
  Card,
  Chip,
  DateField,
  DateRangePicker,
  EmptyState,
  Label,
  Link,
  Modal,
  Pagination,
  Popover,
  RangeCalendar,
  SearchField,
  Skeleton,
  Spinner,
  Table,
  Tabs,
  ToggleButton,
  ToggleButtonGroup,
  type Key,
} from '@yreidev/ui'
import { Tray, Xmark } from '@gravity-ui/icons'
import { fromDate, getLocalTimeZone, toCalendarDate } from '@internationalized/date'
import { yuan } from './format'
import type { OrderState } from './types'
import { stateColor, stateText } from './types'
import { cx, toastError, Pill, type Tone } from './ui-base'
import { TextAreaInput, useFieldVariant, type FieldProps } from './ui-form'

export * from './ui-base'
export * from './ui-form'

/** Mono 是不需要复制的编号。 */
export function Mono({ children }: { children: ReactNode }) {
  return <span className="tb-mono">{children}</span>
}

/** Code 是正文里的一小段代码或地址。 */
export function Code({ children }: { children: ReactNode }) {
  return <code className="tb-mono rounded-md bg-default px-1.5 py-0.5 text-foreground">{children}</code>
}

/** OrderLink 是表格里点开订单详情的订单号（键盘也能点）。 */
export function OrderLink({ tradeNo, onClick }: { tradeNo: ReactNode; onClick: () => void }) {
  return (
    <Link className="tb-mono cursor-pointer no-underline hover:underline" onPress={onClick}>
      {tradeNo}
    </Link>
  )
}

/** Money 把分显示成元，数字等宽。 */
export function Money({ cents, sign, type }: { cents: number; sign?: boolean; type?: 'success' | 'tertiary' }) {
  return (
    <span className={cx('tb-num', type === 'success' && 'text-success', type === 'tertiary' && 'text-muted')}>
      {sign && cents > 0 ? '+' : ''}
      {yuan(cents)}
    </span>
  )
}

export function StateTag({ state }: { state: OrderState }) {
  return <Pill tone={stateColor[state]}>{stateText[state]}</Pill>
}

/**
 * PageHeader 是页面正文的第一行：左边一句话说明这页做什么，右边是整页的操作按钮。
 * 页面标题在顶栏里（admin/route.tsx），这里不重复。
 */
export function PageHeader({
  description,
  actions,
  children,
}: {
  description?: ReactNode
  actions?: ReactNode
  children?: ReactNode
}) {
  return (
    <div className="flex flex-col gap-3">
      {(description || actions) && (
        <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
          {description ? <p className="min-w-0 text-sm text-muted">{description}</p> : <span />}
          {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
        </div>
      )}
      {children}
    </div>
  )
}

const kpiTone: Record<Tone, string> = {
  default: 'bg-default text-default-foreground',
  accent: 'bg-accent-soft text-accent-soft-foreground',
  success: 'bg-success-soft text-success-soft-foreground',
  warning: 'bg-warning-soft text-warning-soft-foreground',
  danger: 'bg-danger-soft text-danger-soft-foreground',
}

/** Kpi 是一个关键数字：名称、数值、底部一行补充；tone 不是 default 时数值和图标跟着变色，提醒去处理。 */
export function Kpi({
  label,
  value,
  unit,
  icon,
  tone = 'default',
  foot,
  className,
}: {
  label: ReactNode
  value: ReactNode
  unit?: ReactNode
  icon?: ReactNode
  tone?: Tone
  foot?: ReactNode
  className?: string
}) {
  return (
    <Card className={cx('min-w-0 gap-1 rounded-2xl', className)}>
      <div className="flex items-center justify-between gap-2">
        <span className="truncate text-sm font-medium text-muted">{label}</span>
        {icon && (
          <span className={cx('grid size-8 shrink-0 place-items-center rounded-xl [&>svg]:size-4', kpiTone[tone])}>
            {icon}
          </span>
        )}
      </div>
      <div className="flex items-baseline gap-1.5">
        <span
          className={cx(
            'tb-num text-2xl font-semibold tracking-tight',
            tone === 'warning' && 'text-warning',
            tone === 'danger' && 'text-danger',
          )}
        >
          {value}
        </span>
        {unit && <span className="text-sm text-muted">{unit}</span>}
      </div>
      {foot && <div className="mt-auto pt-1 text-xs text-muted">{foot}</div>}
    </Card>
  )
}

/** SettingsRow 是设置表单的一行：左边名称和说明，右边控件；窄屏上下排。 */
export function SettingsRow({
  label,
  description,
  children,
}: {
  label: ReactNode
  description?: ReactNode
  children: ReactNode
}) {
  return (
    <div className="grid gap-x-8 gap-y-3 py-5 first:pt-0 last:pb-0 md:grid-cols-[minmax(0,2fr)_minmax(0,3fr)]">
      <div className="min-w-0">
        <div className="text-sm font-medium">{label}</div>
        {description && <p className="mt-1 text-sm text-muted">{description}</p>}
      </div>
      <div className="flex min-w-0 flex-col gap-3">{children}</div>
    </div>
  )
}

/** Section 是抽屉、弹窗里的一个分节：小标题加内容。 */
export function Section({
  title,
  extra,
  children,
  className,
}: {
  title: ReactNode
  extra?: ReactNode
  children: ReactNode
  className?: string
}) {
  return (
    <section className={cx('flex flex-col gap-3', className)}>
      <div className="flex items-center justify-between gap-2">
        <h3 className="text-sm font-semibold">{title}</h3>
        {extra}
      </div>
      {children}
    </section>
  )
}

/** Desc 是“名称 —— 值”的明细列表：名称在左，值靠右，行间细线分隔。用于抽屉、弹窗里的详情。 */
export function Desc({
  data,
  className,
}: {
  data: ({ key: ReactNode; value: ReactNode } | false | null | undefined | '' | 0)[]
  className?: string
}) {
  return (
    <dl className={cx('flex flex-col divide-y divide-separator text-sm', className)}>
      {data.map(
        (it, i) =>
          it && (
            <div key={i} className="flex items-start justify-between gap-6 py-2.5 first:pt-0 last:pb-0">
              <dt className="shrink-0 text-muted">{it.key}</dt>
              <dd className="min-w-0 text-right break-words">{it.value}</dd>
            </div>
          ),
      )}
    </dl>
  )
}

/** Empty 是没有数据时的占位。 */
export function Empty({
  title,
  description,
  icon,
  children,
  className,
}: {
  title?: ReactNode
  description?: ReactNode
  icon?: ReactNode
  children?: ReactNode
  className?: string
}) {
  return (
    <EmptyState className={cx('flex flex-col items-center justify-center gap-2 px-4 py-8 text-center', className)}>
      <span className="text-muted [&>svg]:size-6">{icon ?? <Tray />}</span>
      {title && <p className="font-medium">{title}</p>}
      {description && <p className="text-sm text-muted">{description}</p>}
      {children}
    </EmptyState>
  )
}

/** PageSkeleton 是整页数据还没到时的骨架。 */
export function PageSkeleton({ rows = 3 }: { rows?: number }) {
  return (
    <div className="flex flex-col gap-4" aria-busy="true" aria-label="加载中">
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        {[0, 1, 2, 3].map((i) => (
          <Skeleton key={i} className="h-28 rounded-2xl" />
        ))}
      </div>
      {Array.from({ length: rows }, (_, i) => (
        <Skeleton key={i} className="h-40 rounded-2xl" />
      ))}
    </div>
  )
}

/** Segmented 是一排互斥的切换按钮（近 7 天 / 近 30 天这类）。 */
export function Segmented<V extends string>({
  value,
  onChange,
  options,
  ariaLabel,
  className,
}: {
  value: V
  onChange: (v: V) => void
  options: { value: V; label: ReactNode }[]
  ariaLabel: string
  className?: string
}) {
  return (
    <ToggleButtonGroup
      aria-label={ariaLabel}
      size="sm"
      className={className}
      selectionMode="single"
      disallowEmptySelection
      selectedKeys={new Set([value])}
      onSelectionChange={(keys) => {
        const v = [...keys][0]
        if (v !== undefined) onChange(String(v) as V)
      }}
    >
      {options.map((o, i) => (
        <ToggleButton key={o.value} id={o.value}>
          {i > 0 && <ToggleButtonGroup.Separator />}
          {o.label}
        </ToggleButton>
      ))}
    </ToggleButtonGroup>
  )
}

/**
 * FilterTabs 是表格上方按状态分组的标签页（全部、待支付、已支付……）。children 是当前标签下的内容；
 * 只渲染选中的那一页，切换时由调用方改查询条件。
 */
export function FilterTabs<V extends string>({
  ariaLabel,
  value,
  onChange,
  options,
  children,
  className,
}: {
  ariaLabel: string
  value: V
  onChange: (v: V) => void
  options: { value: V; label: ReactNode; count?: number; tone?: Tone }[]
  children: ReactNode
  className?: string
}) {
  return (
    <Tabs
      variant="secondary"
      selectedKey={value}
      onSelectionChange={(k) => onChange(String(k) as V)}
      className={cx('min-w-0', className)}
    >
      <Tabs.ListContainer className="-mx-1 overflow-x-auto px-1">
        <Tabs.List aria-label={ariaLabel} className="w-max min-w-full">
          {options.map((o) => (
            <Tabs.Tab key={o.value} id={o.value} className="gap-1.5 whitespace-nowrap">
              {o.label}
              {!!o.count && (
                <Chip
                  size="sm"
                  variant="soft"
                  color={o.tone ?? 'default'}
                  className="tb-num h-5 min-w-5 justify-center px-1.5 text-xs"
                >
                  {o.count > 999 ? '999+' : o.count}
                </Chip>
              )}
              <Tabs.Indicator />
            </Tabs.Tab>
          ))}
        </Tabs.List>
      </Tabs.ListContainer>
      <Tabs.Panel id={value} className="flex flex-col gap-3 pt-4 outline-none">
        {children}
      </Tabs.Panel>
    </Tabs>
  )
}

/** Toolbar 是表格上方的一排筛选控件；窄屏时自动换行，搜索框占满一行。 */
export function Toolbar({ children, end }: { children: ReactNode; end?: ReactNode }) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      {children}
      {end && <div className="ms-auto flex flex-wrap items-center gap-2">{end}</div>}
    </div>
  )
}

/** Loading 是区块加载中的占位。 */
export function Loading({ className }: { className?: string }) {
  return (
    <div className={cx('flex justify-center py-10', className)}>
      <Spinner />
    </div>
  )
}

export interface Column<T> {
  title: ReactNode
  render: (row: T, index: number) => ReactNode
  align?: 'right' | 'center'
  /** 单元格不换行（时间、金额） */
  nowrap?: boolean
  className?: string
}

const alignClass = { right: 'text-right', center: 'text-center' }

/** useMediaQuery 跟踪一个媒体查询是否命中（窗口宽度变化时更新）。 */
export function useMediaQuery(q: string) {
  const [hit, setHit] = useState(() => window.matchMedia(q).matches)
  useEffect(() => {
    const mq = window.matchMedia(q)
    const on = () => setHit(mq.matches)
    on()
    mq.addEventListener('change', on)
    return () => mq.removeEventListener('change', on)
  }, [q])
  return hit
}

/**
 * DataTable 用列定义渲染 HeroUI 表格。minWidth 是表格的最小宽度，放不下时横向滚动；
 * onRowClick 让整行可点（键盘回车也行）；pagination 在总数超过一页时显示分页。
 * 给了 card 时，手机宽度下不画表格，改成一行一张的卡片列表（card 渲染每张卡片的内容）。
 */
export function DataTable<T>({
  label,
  rows,
  rowKey,
  columns,
  onRowClick,
  empty,
  loading,
  minWidth,
  pagination,
  className,
  card,
}: {
  label: string
  rows: T[]
  rowKey: (row: T) => Key
  columns: Column<T>[]
  onRowClick?: (row: T) => void
  empty?: ReactNode
  loading?: boolean
  minWidth?: number
  pagination?: { page: number; pageSize: number; total: number; onChange: (page: number) => void }
  className?: string
  card?: (row: T) => ReactNode
}) {
  const phone = useMediaQuery('(max-width: 639px)')
  const emptyNode = loading ? (
    <Loading />
  ) : typeof empty === 'string' || !empty ? (
    <Empty description={empty || '暂无数据'} />
  ) : (
    empty
  )
  const pager = pagination && pagination.total > pagination.pageSize && <Pager {...pagination} />
  if (card && phone) {
    return (
      <div className={cx('flex flex-col gap-3', className)}>
        <div className="overflow-hidden rounded-2xl bg-surface shadow-surface">
          {rows.length === 0 ? (
            emptyNode
          ) : (
            <ul aria-label={label} className="divide-y divide-separator">
              {rows.map((r) => (
                <li key={rowKey(r)}>
                  {onRowClick ? (
                    <button
                      type="button"
                      className="block w-full px-4 py-3 text-left outline-none focus-visible:bg-default active:bg-default"
                      onClick={() => onRowClick(r)}
                    >
                      {card(r)}
                    </button>
                  ) : (
                    <div className="px-4 py-3">{card(r)}</div>
                  )}
                </li>
              ))}
            </ul>
          )}
        </div>
        {pager}
      </div>
    )
  }
  return (
    <Table className={className}>
      <Table.ScrollContainer>
        <Table.Content aria-label={label} style={minWidth ? { minWidth } : undefined}>
          <Table.Header>
            {columns.map((c, i) => (
              <Table.Column
                key={i}
                id={`c${i}`}
                isRowHeader={i === 0}
                className={cx('whitespace-nowrap', c.align && alignClass[c.align])}
              >
                {c.title}
              </Table.Column>
            ))}
          </Table.Header>
          <Table.Body renderEmptyState={() => emptyNode}>
            {rows.map((r, idx) => (
              <Table.Row
                key={rowKey(r)}
                id={rowKey(r)}
                className={onRowClick && 'cursor-pointer'}
                onAction={onRowClick ? () => onRowClick(r) : undefined}
              >
                {columns.map((c, i) => (
                  <Table.Cell
                    key={i}
                    className={cx(c.align && alignClass[c.align], c.nowrap && 'whitespace-nowrap', c.className)}
                  >
                    {c.render(r, idx)}
                  </Table.Cell>
                ))}
              </Table.Row>
            ))}
          </Table.Body>
        </Table.Content>
      </Table.ScrollContainer>
      {pager && <Table.Footer>{pager}</Table.Footer>}
    </Table>
  )
}

/** Stack 是表格单元格里的两行：上面主要信息，下面一行小字补充。 */
export function Stack({ main, sub, className }: { main: ReactNode; sub?: ReactNode; className?: string }) {
  return (
    <div className={cx('flex min-w-0 flex-col gap-0.5', className)}>
      <div className="min-w-0 truncate">{main}</div>
      {sub && <div className="min-w-0 truncate text-xs text-muted">{sub}</div>}
    </div>
  )
}

/** Pager 是分页条：首尾页、当前页前后各一页，其余用省略号。 */
export function Pager({
  page,
  pageSize,
  total,
  onChange,
}: {
  page: number
  pageSize: number
  total: number
  onChange: (page: number) => void
}) {
  const last = Math.max(1, Math.ceil(total / pageSize))
  const pages: (number | 'gap')[] = [1]
  if (page > 3) pages.push('gap')
  for (let p = Math.max(2, page - 1); p <= Math.min(last - 1, page + 1); p++) pages.push(p)
  if (page < last - 2) pages.push('gap')
  if (last > 1) pages.push(last)
  return (
    <Pagination size="sm" className="w-full flex-wrap justify-between gap-2">
      <Pagination.Summary>
        共 <span className="tb-num">{total}</span> 条
      </Pagination.Summary>
      <Pagination.Content>
        <Pagination.Item>
          <Pagination.Previous isDisabled={page <= 1} onPress={() => onChange(page - 1)} aria-label="上一页">
            <Pagination.PreviousIcon />
          </Pagination.Previous>
        </Pagination.Item>
        {pages.map((p, i) =>
          p === 'gap' ? (
            <Pagination.Item key={`gap-${i}`}>
              <Pagination.Ellipsis />
            </Pagination.Item>
          ) : (
            <Pagination.Item key={p}>
              <Pagination.Link isActive={p === page} onPress={() => onChange(p)}>
                {p}
              </Pagination.Link>
            </Pagination.Item>
          ),
        )}
        <Pagination.Item>
          <Pagination.Next isDisabled={page >= last} onPress={() => onChange(page + 1)} aria-label="下一页">
            <Pagination.NextIcon />
          </Pagination.Next>
        </Pagination.Item>
      </Pagination.Content>
    </Pagination>
  )
}

/** Confirm 点按钮后先弹出确认气泡，确认了才执行。children 是触发按钮。 */
export function Confirm({
  title,
  content,
  okText = '确定',
  danger,
  onConfirm,
  children,
}: {
  title: ReactNode
  content?: ReactNode
  okText?: string
  danger?: boolean
  onConfirm: () => void
  children: ReactElement
}) {
  const [open, setOpen] = useState(false)
  return (
    <Popover isOpen={open} onOpenChange={setOpen}>
      {children}
      <Popover.Content className="max-w-80">
        <Popover.Dialog>
          <Popover.Heading className="font-medium">{title}</Popover.Heading>
          {content && <p className="mt-1 text-sm text-muted">{content}</p>}
          <div className="mt-3 flex justify-end gap-2">
            <Button size="sm" variant="tertiary" onPress={() => setOpen(false)}>
              取消
            </Button>
            <Button
              size="sm"
              variant={danger ? 'danger' : 'primary'}
              onPress={() => {
                setOpen(false)
                onConfirm()
              }}
            >
              {okText}
            </Button>
          </div>
        </Popover.Dialog>
      </Popover.Content>
    </Popover>
  )
}

/**
 * ConfirmDialog 是删除、重置这类不可撤销操作的确认弹窗。比 Confirm 气泡醒目，也能从下拉菜单里打开。
 * onConfirm 抛错时弹出错误提示、弹窗不关。
 */
export function ConfirmDialog({
  open,
  onClose,
  title,
  children,
  okText = '确定',
  status = 'danger',
  onConfirm,
}: {
  open: boolean
  onClose: () => void
  title: ReactNode
  children?: ReactNode
  okText?: string
  status?: 'danger' | 'warning' | 'accent'
  onConfirm: () => Promise<unknown> | void
}) {
  const [busy, setBusy] = useState(false)
  return (
    <AlertDialog.Backdrop
      isOpen={open}
      onOpenChange={(v) => !v && !busy && onClose()}
      isDismissable
      isKeyboardDismissDisabled={false}
    >
      <AlertDialog.Container size="sm">
        <AlertDialog.Dialog>
          <AlertDialog.Header>
            <AlertDialog.Icon status={status} />
            <AlertDialog.Heading>{title}</AlertDialog.Heading>
          </AlertDialog.Header>
          {children && (
            <AlertDialog.Body>
              <div className="flex flex-col gap-2 text-sm text-muted">{children}</div>
            </AlertDialog.Body>
          )}
          <AlertDialog.Footer>
            <Button variant="tertiary" isDisabled={busy} onPress={onClose}>
              取消
            </Button>
            <Button
              variant={status === 'danger' ? 'danger' : 'primary'}
              isPending={busy}
              onPress={async () => {
                setBusy(true)
                try {
                  await onConfirm()
                  onClose()
                } catch (e) {
                  toastError(e)
                } finally {
                  setBusy(false)
                }
              }}
            >
              {okText}
            </Button>
          </AlertDialog.Footer>
        </AlertDialog.Dialog>
      </AlertDialog.Container>
    </AlertDialog.Backdrop>
  )
}

/** SearchInput 是关键词搜索框：回车、失焦或清空时调用 onSubmit。 */
export function SearchInput({
  ariaLabel,
  className,
  value,
  onChange,
  onSubmit,
  placeholder,
}: {
  ariaLabel: string
  className?: string
  value: string
  onChange: (v: string) => void
  onSubmit: (v: string) => void
  placeholder?: string
}) {
  const variant = useFieldVariant()
  return (
    <SearchField
      variant={variant}
      aria-label={ariaLabel}
      className={className}
      value={value}
      onChange={onChange}
      onSubmit={(v) => onSubmit(v.trim())}
      onClear={() => onSubmit('')}
      onBlur={() => onSubmit(value.trim())}
    >
      <SearchField.Group>
        <SearchField.SearchIcon />
        <SearchField.Input placeholder={placeholder} />
        <SearchField.ClearButton />
      </SearchField.Group>
    </SearchField>
  )
}

/**
 * DateRangeInput 选一段日期。值是浏览器本地时间的毫秒：from 是起始日零点，to 是结束日次日零点；
 * 两个都是 undefined 表示不限。
 */
export function DateRangeInput({
  label,
  ariaLabel,
  className,
  from,
  to,
  onChange,
}: Pick<FieldProps, 'label' | 'ariaLabel' | 'className'> & {
  from?: number
  to?: number
  onChange: (from: number | undefined, to: number | undefined) => void
}) {
  const variant = useFieldVariant()
  const tz = getLocalTimeZone()
  const value =
    from !== undefined && to !== undefined
      ? { start: toCalendarDate(fromDate(new Date(from), tz)), end: toCalendarDate(fromDate(new Date(to - 1), tz)) }
      : null
  return (
    <DateRangePicker
      className={className}
      aria-label={label ? undefined : ariaLabel}
      value={value}
      onChange={(r) =>
        r
          ? onChange(r.start.toDate(tz).getTime(), r.end.add({ days: 1 }).toDate(tz).getTime())
          : onChange(undefined, undefined)
      }
    >
      {label && <Label>{label}</Label>}
      <DateField.Group fullWidth variant={variant}>
        <DateField.Input slot="start">{(segment) => <DateField.Segment segment={segment} />}</DateField.Input>
        <DateRangePicker.RangeSeparator />
        <DateField.Input slot="end">{(segment) => <DateField.Segment segment={segment} />}</DateField.Input>
        <DateField.Suffix>
          {value && (
            <Button
              isIconOnly
              size="sm"
              variant="ghost"
              aria-label="清除日期"
              className="size-6 min-w-6 text-muted"
              onPress={() => onChange(undefined, undefined)}
            >
              <Xmark className="size-3.5" />
            </Button>
          )}
          <DateRangePicker.Trigger>
            <DateRangePicker.TriggerIndicator />
          </DateRangePicker.Trigger>
        </DateField.Suffix>
      </DateField.Group>
      <DateRangePicker.Popover>
        <RangeCalendar aria-label={typeof label === 'string' ? label : ariaLabel}>
          <RangeCalendar.Header>
            <RangeCalendar.YearPickerTrigger>
              <RangeCalendar.YearPickerTriggerHeading />
              <RangeCalendar.YearPickerTriggerIndicator />
            </RangeCalendar.YearPickerTrigger>
            <RangeCalendar.NavButton slot="previous" />
            <RangeCalendar.NavButton slot="next" />
          </RangeCalendar.Header>
          <RangeCalendar.Grid>
            <RangeCalendar.GridHeader>
              {(day) => <RangeCalendar.HeaderCell>{day}</RangeCalendar.HeaderCell>}
            </RangeCalendar.GridHeader>
            <RangeCalendar.GridBody>{(date) => <RangeCalendar.Cell date={date} />}</RangeCalendar.GridBody>
          </RangeCalendar.Grid>
          <RangeCalendar.YearPickerGrid>
            <RangeCalendar.YearPickerGridBody>
              {({ year }) => <RangeCalendar.YearPickerCell year={year} />}
            </RangeCalendar.YearPickerGridBody>
          </RangeCalendar.YearPickerGrid>
        </RangeCalendar>
      </DateRangePicker.Popover>
    </DateRangePicker>
  )
}

/** Dialog 是带标题、正文和底部按钮的弹窗。 */
export function Dialog({
  title,
  open,
  onClose,
  children,
  footer,
  size = 'md',
  dismissable = true,
}: {
  title: ReactNode
  open: boolean
  onClose: () => void
  children: ReactNode
  footer?: ReactNode
  size?: 'xs' | 'sm' | 'md' | 'lg'
  dismissable?: boolean
}) {
  return (
    <Modal.Backdrop isOpen={open} onOpenChange={(v) => !v && onClose()} isDismissable={dismissable}>
      <Modal.Container size={size} scroll="inside">
        <Modal.Dialog>
          <Modal.CloseTrigger />
          <Modal.Header>
            <Modal.Heading>{title}</Modal.Heading>
          </Modal.Header>
          <Modal.Body>{children}</Modal.Body>
          {footer && <Modal.Footer>{footer}</Modal.Footer>}
        </Modal.Dialog>
      </Modal.Container>
    </Modal.Backdrop>
  )
}

// NoteModal 弹框让管理员填理由，确认后调用 onOK。用于关联、撤销关联、处理异常这类要留痕的操作。
export function NoteModal({
  title,
  description,
  visible,
  onCancel,
  onOK,
  children,
  okText = '确定',
  danger,
}: {
  title: string
  description?: ReactNode
  visible: boolean
  onCancel: () => void
  onOK: (note: string) => Promise<void>
  children?: ReactNode
  okText?: string
  danger?: boolean
}) {
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const close = () => {
    setNote('')
    onCancel()
  }
  return (
    <Dialog
      title={title}
      open={visible}
      onClose={close}
      footer={
        <>
          <Button variant="tertiary" onPress={close}>
            取消
          </Button>
          <Button
            variant={danger ? 'danger' : 'primary'}
            isDisabled={!note.trim()}
            isPending={busy}
            onPress={async () => {
              setBusy(true)
              try {
                await onOK(note.trim())
                setNote('')
              } catch (e) {
                toastError(e)
              } finally {
                setBusy(false)
              }
            }}
          >
            {okText}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3">
        {description && <p className="text-sm text-muted">{description}</p>}
        {children}
        <TextAreaInput
          label="理由"
          value={note}
          onChange={setNote}
          placeholder="必填，会记入审计日志"
          maxLength={500}
          rows={3}
        />
      </div>
    </Dialog>
  )
}
