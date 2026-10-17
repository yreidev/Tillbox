// 图表用 ECharts，只引入用到的图表类型和组件，用 SVG 渲染。只有监控页用，跟着路由拆包，不进公共入口。
import { useEffect, useRef, useSyncExternalStore } from 'react'
import { BarChart, LineChart, type BarSeriesOption, type LineSeriesOption } from 'echarts/charts'
import {
  GridComponent,
  LegendComponent,
  TooltipComponent,
  type GridComponentOption,
  type LegendComponentOption,
  type TooltipComponentOption,
} from 'echarts/components'
import { init, use as register, type ComposeOption, type EChartsType } from 'echarts/core'
import { SVGRenderer } from 'echarts/renderers'
import { cx } from '../lib/ui-base'

register([BarChart, LineChart, GridComponent, LegendComponent, TooltipComponent, SVGRenderer])

export type ChartOption = ComposeOption<
  BarSeriesOption | LineSeriesOption | GridComponentOption | LegendComponentOption | TooltipComponentOption
>

/** Palette 是从 HeroUI 主题变量取到的颜色，深浅色模式各一套。 */
export interface Palette {
  accent: string
  success: string
  danger: string
  warning: string
  muted: string
  foreground: string
  separator: string
  overlay: string
  font: string
}

let probe: { el: HTMLSpanElement; ctx: CanvasRenderingContext2D | null } | undefined

// cssColor 把主题变量换成 rgba。HeroUI 的颜色是 oklch，ECharts 不认识：先让浏览器算出元素的实际颜色，
// 再画到 1×1 的画布上读回像素。
function cssColor(name: string): string {
  if (!probe) {
    const el = document.createElement('span')
    el.style.display = 'none'
    document.body.append(el)
    const canvas = document.createElement('canvas')
    canvas.width = canvas.height = 1
    probe = { el, ctx: canvas.getContext('2d', { willReadFrequently: true }) }
  }
  probe.el.style.color = `var(${name})`
  const computed = getComputedStyle(probe.el).color
  const ctx = probe.ctx
  if (!ctx) return computed
  ctx.clearRect(0, 0, 1, 1)
  ctx.fillStyle = computed
  ctx.fillRect(0, 0, 1, 1)
  const [r, g, b, a] = ctx.getImageData(0, 0, 1, 1).data
  return `rgba(${r}, ${g}, ${b}, ${Math.round((a / 255) * 1000) / 1000})`
}

function readPalette(): Palette {
  return {
    accent: cssColor('--accent'),
    success: cssColor('--success'),
    danger: cssColor('--danger'),
    warning: cssColor('--warning'),
    muted: cssColor('--muted'),
    foreground: cssColor('--foreground'),
    separator: cssColor('--separator'),
    overlay: cssColor('--overlay'),
    font: getComputedStyle(document.body).fontFamily,
  }
}

// 深浅色由 <html> 的 data-theme 决定（lib/theme.ts），变了就重新取色。
function subscribeMode(cb: () => void) {
  const mo = new MutationObserver(cb)
  mo.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme', 'class'] })
  return () => mo.disconnect()
}

const readMode = () => document.documentElement.dataset.theme ?? ''

/**
 * Chart 画一张 ECharts 图。build 用当前主题的颜色生成配置；容器尺寸变了自动重排。
 * label 是给读屏软件的一句话说明。
 */
export function Chart({
  build,
  label,
  className,
}: {
  build: (p: Palette) => ChartOption
  label: string
  className?: string
}) {
  const box = useRef<HTMLDivElement>(null)
  const chart = useRef<EChartsType | null>(null)
  const mode = useSyncExternalStore(subscribeMode, readMode)

  useEffect(() => {
    const el = box.current
    if (!el) return
    const c = init(el, null, { renderer: 'svg' })
    chart.current = c
    const ro = new ResizeObserver(() => c.resize())
    ro.observe(el)
    return () => {
      ro.disconnect()
      c.dispose()
      chart.current = null
    }
  }, [])

  useEffect(() => {
    chart.current?.setOption(build(readPalette()))
  }, [build, mode])

  return <div ref={box} role="img" aria-label={label} className={cx('min-w-0', className)} />
}

/** tooltipStyle 让悬停提示跟 HeroUI 的浮层一个样子。 */
export function tooltipStyle(p: Palette): TooltipComponentOption {
  return {
    trigger: 'axis',
    backgroundColor: p.overlay,
    borderColor: p.separator,
    borderWidth: 1,
    padding: [8, 10],
    textStyle: { color: p.foreground, fontSize: 12, fontFamily: p.font },
    extraCssText: 'border-radius: 12px; box-shadow: 0 4px 16px rgba(0,0,0,.12);',
    axisPointer: { type: 'shadow', shadowStyle: { color: p.separator, opacity: 0.35 } },
  }
}
