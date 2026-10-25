// 表单控件：把 HeroUI 的输入框、下拉、日期这些组合成带标签、说明和校验提示的一个组件。规范见 docs/ui.md。
import { useContext, type ReactNode } from 'react'
import {
  Calendar,
  Checkbox,
  DateField,
  DatePicker,
  Description,
  FieldError,
  Input,
  InputGroup,
  Label,
  ListBox,
  NumberField,
  Radio,
  RadioGroup,
  Select,
  SurfaceContext,
  Switch,
  TextArea,
  TextField,
  TimeField,
  type Key,
} from '@yreidev/ui'
import { fromDate, getLocalTimeZone, toZoned } from '@internationalized/date'
import { cx } from './ui-base'

/**
 * fieldVariant 按所在位置选输入框样式：放在卡片、抽屉、弹窗里用 secondary（灰底、无阴影），
 * 在页面底色上用默认的白底。HeroUI 的这些容器会提供 SurfaceContext，页面底色上没有。
 */
export function useFieldVariant(): 'primary' | 'secondary' {
  return useContext(SurfaceContext).variant ? 'secondary' : 'primary'
}

/** fieldWidth 让输入框默认占满一行；调用方自己给了宽度（如 w-40）就不再加 w-full，免得两个宽度类打架。 */
const fieldWidth = (className?: string) =>
  className && /(^|\s)w-/.test(className) ? className : cx('w-full', className)

export interface FieldProps {
  label?: ReactNode
  /** 没有可见标签时给读屏用 */
  ariaLabel?: string
  description?: ReactNode
  isRequired?: boolean
  isDisabled?: boolean
  className?: string
}

/** TextInput 是单行输入框，带标签、说明和校验提示。 */
export function TextInput({
  label,
  ariaLabel,
  description,
  isRequired,
  isDisabled,
  isReadOnly,
  className,
  value,
  onChange,
  onBlur,
  onKeyDown,
  type,
  placeholder,
  autoComplete,
  autoFocus,
  maxLength,
  validate,
  mono,
  name,
  inputMode,
  suffix,
}: FieldProps & {
  value: string
  onChange: (v: string) => void
  onBlur?: () => void
  onKeyDown?: (e: React.KeyboardEvent) => void
  isReadOnly?: boolean
  type?: 'text' | 'password' | 'url' | 'email' | 'search'
  placeholder?: string
  autoComplete?: string
  autoFocus?: boolean
  maxLength?: number
  validate?: (v: string) => string | null | undefined
  mono?: boolean
  name?: string
  inputMode?: 'decimal' | 'numeric' | 'text' | 'url'
  /** 输入框右侧的按钮（如显示/隐藏、粘贴），用 size="sm" variant="ghost" 的图标按钮 */
  suffix?: ReactNode
}) {
  const variant = useFieldVariant()
  const inputProps = { placeholder, autoComplete, inputMode, className: mono ? 'tb-mono' : undefined }
  return (
    <TextField
      className={fieldWidth(className)}
      value={value}
      onChange={onChange}
      onBlur={onBlur}
      onKeyDown={onKeyDown}
      type={type}
      name={name}
      aria-label={label ? undefined : ariaLabel}
      isRequired={isRequired}
      isDisabled={isDisabled}
      isReadOnly={isReadOnly}
      autoFocus={autoFocus}
      maxLength={maxLength}
      validate={validate}
    >
      {label && <Label>{label}</Label>}
      {suffix ? (
        <InputGroup variant={variant} fullWidth>
          <InputGroup.Input {...inputProps} />
          <InputGroup.Suffix className="gap-0.5 pe-1">{suffix}</InputGroup.Suffix>
        </InputGroup>
      ) : (
        <Input variant={variant} {...inputProps} />
      )}
      {description && <Description>{description}</Description>}
      <FieldError />
    </TextField>
  )
}

/** TextAreaInput 是多行输入框；rows 是默认行数。 */
export function TextAreaInput({
  label,
  ariaLabel,
  description,
  isRequired,
  isDisabled,
  className,
  value,
  onChange,
  placeholder,
  rows = 3,
  maxLength,
  validate,
  mono,
}: FieldProps & {
  value: string
  onChange: (v: string) => void
  placeholder?: string
  rows?: number
  maxLength?: number
  validate?: (v: string) => string | null | undefined
  mono?: boolean
}) {
  const variant = useFieldVariant()
  return (
    <TextField
      className={fieldWidth(className)}
      value={value}
      onChange={onChange}
      aria-label={label ? undefined : ariaLabel}
      isRequired={isRequired}
      isDisabled={isDisabled}
      maxLength={maxLength}
      validate={validate}
    >
      {label && <Label>{label}</Label>}
      <TextArea variant={variant} placeholder={placeholder} rows={rows} className={cx('resize-y', mono && 'tb-mono')} />
      {(description || maxLength) && (
        <Description>
          {description}
          {maxLength && (
            <span className="tb-num float-right">
              {value.length}/{maxLength}
            </span>
          )}
        </Description>
      )}
      <FieldError />
    </TextField>
  )
}

export interface Option<V extends Key> {
  value: V
  label: string
  description?: ReactNode
  isDisabled?: boolean
}

/** DateTimeInput 选到分钟的日期时间。值和范围都是毫秒时间戳，按浏览器本地时区显示。 */
export function DateTimeInput({
  label,
  ariaLabel,
  description,
  isRequired,
  isDisabled,
  className,
  value,
  onChange,
  minValue,
  maxValue,
  validate,
}: FieldProps & {
  value: number | undefined
  onChange: (v: number | undefined) => void
  minValue?: number
  maxValue?: number
  /** 自定义校验，参数是毫秒时间戳；返回的文字优先于自带的范围提示 */
  validate?: (ms: number) => string | null | undefined
}) {
  const variant = useFieldVariant()
  const tz = getLocalTimeZone()
  const zoned = (ms: number | undefined) =>
    ms === undefined ? undefined : fromDate(new Date(ms), tz).set({ second: 0, millisecond: 0 })
  return (
    <DatePicker
      className={fieldWidth(className)}
      aria-label={label ? undefined : ariaLabel}
      value={zoned(value) ?? null}
      onChange={(d) => onChange(d ? toZoned(d, tz).toDate().getTime() : undefined)}
      minValue={zoned(minValue)}
      maxValue={zoned(maxValue)}
      validate={validate && ((d) => validate(toZoned(d, tz).toDate().getTime()))}
      granularity="minute"
      hourCycle={24}
      hideTimeZone
      isRequired={isRequired}
      isDisabled={isDisabled}
    >
      {({ state }) => (
        <>
          {label && <Label>{label}</Label>}
          {/* 窄屏大字号时日期和时间放不下一行，允许把时间折到第二行，日历按钮留在右边 */}
          <DateField.Group fullWidth variant={variant} className="h-auto min-h-9">
            <DateField.Input className="min-w-0 flex-wrap">
              {(segment) => <DateField.Segment segment={segment} />}
            </DateField.Input>
            <DateField.Suffix>
              <DatePicker.Trigger>
                <DatePicker.TriggerIndicator />
              </DatePicker.Trigger>
            </DateField.Suffix>
          </DateField.Group>
          {description && <Description>{description}</Description>}
          <FieldError />
          <DatePicker.Popover className="flex flex-col gap-3">
            <Calendar aria-label={typeof label === 'string' ? label : ariaLabel}>
              <Calendar.Header>
                <Calendar.YearPickerTrigger>
                  <Calendar.YearPickerTriggerHeading />
                  <Calendar.YearPickerTriggerIndicator />
                </Calendar.YearPickerTrigger>
                <Calendar.NavButton slot="previous" />
                <Calendar.NavButton slot="next" />
              </Calendar.Header>
              <Calendar.Grid>
                <Calendar.GridHeader>{(day) => <Calendar.HeaderCell>{day}</Calendar.HeaderCell>}</Calendar.GridHeader>
                <Calendar.GridBody>{(date) => <Calendar.Cell date={date} />}</Calendar.GridBody>
              </Calendar.Grid>
              <Calendar.YearPickerGrid>
                <Calendar.YearPickerGridBody>
                  {({ year }) => <Calendar.YearPickerCell year={year} />}
                </Calendar.YearPickerGridBody>
              </Calendar.YearPickerGrid>
            </Calendar>
            <div className="flex items-center justify-between gap-3">
              <Label>时间</Label>
              <TimeField
                aria-label="时间"
                granularity="minute"
                hourCycle={24}
                hideTimeZone
                value={state.timeValue}
                onChange={(v) => v && state.setTimeValue(v)}
              >
                <TimeField.Group variant="secondary">
                  <TimeField.Input>{(segment) => <TimeField.Segment segment={segment} />}</TimeField.Input>
                </TimeField.Group>
              </TimeField>
            </div>
          </DatePicker.Popover>
        </>
      )}
    </DatePicker>
  )
}

/** SelectInput 是下拉单选；clearable 时可以清空（值变成 undefined）。 */
export function SelectInput<V extends Key>({
  label,
  ariaLabel,
  description,
  isRequired,
  isDisabled,
  className,
  value,
  onChange,
  options,
  placeholder = '请选择',
  clearable,
}: FieldProps & {
  value: V | undefined
  onChange: (v: V | undefined) => void
  options: Option<V>[]
  placeholder?: string
  clearable?: boolean
}) {
  const variant = useFieldVariant()
  return (
    <Select
      variant={variant}
      className={className}
      placeholder={placeholder}
      value={value ?? null}
      onChange={(k) => onChange((k ?? undefined) as V | undefined)}
      aria-label={label ? undefined : ariaLabel}
      isRequired={isRequired}
      isDisabled={isDisabled}
      disabledKeys={options.filter((o) => o.isDisabled).map((o) => o.value)}
    >
      {label && <Label>{label}</Label>}
      <Select.Trigger>
        <Select.Value />
        {clearable && value !== undefined && <Select.ClearButton />}
        <Select.Indicator />
      </Select.Trigger>
      {description && <Description>{description}</Description>}
      <FieldError />
      <Select.Popover>
        <ListBox>
          {options.map((o) => (
            <ListBox.Item key={String(o.value)} id={o.value} textValue={o.label}>
              {o.label}
              <ListBox.ItemIndicator />
            </ListBox.Item>
          ))}
        </ListBox>
      </Select.Popover>
    </Select>
  )
}

/** SwitchInput 是开关。 */
export function SwitchInput({
  label,
  description,
  isDisabled,
  className,
  value,
  onChange,
}: Omit<FieldProps, 'isRequired' | 'ariaLabel'> & { value: boolean; onChange: (v: boolean) => void }) {
  return (
    <Switch className={className} isSelected={value} onChange={onChange} isDisabled={isDisabled}>
      <Switch.Content>
        <Switch.Control>
          <Switch.Thumb />
        </Switch.Control>
        {label}
      </Switch.Content>
      {description && <Description>{description}</Description>}
    </Switch>
  )
}

/** CheckboxInput 是单个勾选框。 */
export function CheckboxInput({
  label,
  description,
  isDisabled,
  className,
  value,
  onChange,
}: Omit<FieldProps, 'isRequired' | 'ariaLabel'> & { value: boolean; onChange: (v: boolean) => void }) {
  const variant = useFieldVariant()
  return (
    <Checkbox variant={variant} className={className} isSelected={value} onChange={onChange} isDisabled={isDisabled}>
      <Checkbox.Content>
        <Checkbox.Control>
          <Checkbox.Indicator />
        </Checkbox.Control>
        {label}
      </Checkbox.Content>
      {description && <Description>{description}</Description>}
    </Checkbox>
  )
}

/** RadioInput 是单选组；horizontal 时横排。 */
export function RadioInput<V extends string>({
  label,
  ariaLabel,
  description,
  isRequired,
  isDisabled,
  className,
  value,
  onChange,
  options,
  horizontal,
}: FieldProps & { value: V; onChange: (v: V) => void; options: Option<V>[]; horizontal?: boolean }) {
  const variant = useFieldVariant()
  return (
    <RadioGroup
      variant={variant}
      className={className}
      value={value}
      onChange={(v) => onChange(v as V)}
      orientation={horizontal ? 'horizontal' : 'vertical'}
      aria-label={label ? undefined : ariaLabel}
      isRequired={isRequired}
      isDisabled={isDisabled}
    >
      {label && <Label>{label}</Label>}
      {description && <Description>{description}</Description>}
      {options.map((o) => (
        <Radio key={o.value} value={o.value} isDisabled={o.isDisabled}>
          <Radio.Content>
            <Radio.Control>
              <Radio.Indicator />
            </Radio.Control>
            {o.label}
          </Radio.Content>
          {o.description && <Description>{o.description}</Description>}
        </Radio>
      ))}
    </RadioGroup>
  )
}

/** NumberInput 是整数输入框，带加减按钮。 */
export function NumberInput({
  label,
  ariaLabel,
  description,
  isRequired,
  isDisabled,
  className,
  value,
  onChange,
  minValue,
  maxValue,
  step,
}: FieldProps & {
  value: number | undefined
  onChange: (v: number | undefined) => void
  minValue?: number
  maxValue?: number
  step?: number
}) {
  const variant = useFieldVariant()
  return (
    <NumberField
      variant={variant}
      className={fieldWidth(className)}
      value={value ?? NaN}
      onChange={(v) => onChange(Number.isNaN(v) ? undefined : v)}
      minValue={minValue}
      maxValue={maxValue}
      step={step}
      formatOptions={{ useGrouping: false, maximumFractionDigits: 0 }}
      aria-label={label ? undefined : ariaLabel}
      isRequired={isRequired}
      isDisabled={isDisabled}
    >
      {label && <Label>{label}</Label>}
      <NumberField.Group>
        <NumberField.DecrementButton />
        <NumberField.Input
          className="tb-num"
          onInput={(e) => {
            // NumberField 失焦或回车才提交，表单在那之前不知道改了，保存按钮也不亮。在范围内、写法规范的整数
            // 边输边提交（提交后输入框按同样写法显示，光标不跳）；超出范围的留到失焦时按范围收拢。
            const t = e.currentTarget.value
            const n = Number(t)
            const fits = Number.isInteger(n) && String(n) === t && (step ?? 1) === 1
            if (fits && n >= (minValue ?? -Infinity) && n <= (maxValue ?? Infinity) && n !== value) onChange(n)
          }}
        />
        <NumberField.IncrementButton />
      </NumberField.Group>
      {description && <Description>{description}</Description>}
      <FieldError />
    </NumberField>
  )
}
