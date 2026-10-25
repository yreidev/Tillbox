import { readFileSync } from 'node:fs'
import { expect, test, type Page } from '@playwright/test'
import { seedFile, type Seed } from './instances'

// 逐页打开，检查：没有脚本异常和控制台错误、没落到路由的出错页、页面不横向滚动。
// 设 E2E_SCREENSHOTS=1 时每页另存一张整页截图到 test-results，方便人工过一遍。

const adminPages: [string, string][] = [
  ['概览', '/admin'],
  ['订单', '/admin/orders'],
  ['待处理异常', '/admin/ledger'],
  ['已处理异常', '/admin/ledger?view=resolved'],
  ['流水', '/admin/ledger?view=transactions'],
  ['通知', '/admin/notifications'],
  ['AI 与付款申诉', '/admin/assistance'],
  ['商户', '/admin/merchants'],
  ['渠道', '/admin/channels'],
  ['监控', '/admin/monitor'],
  ['备份与归档', '/admin/operations'],
  ['支付测试', '/admin/payment-test'],
  ['设置·收款', '/admin/settings'],
  ['设置·平台密钥', '/admin/settings?tab=platform'],
  ['设置·站点', '/admin/settings?tab=site'],
  ['设置·管理员通知', '/admin/settings?tab=notify'],
  ['设置·审计日志', '/admin/settings?tab=audit'],
]

const instance = () => (test.info().project.metadata as { instance: 'empty' | 'data' }).instance

const seed = (): Seed => JSON.parse(readFileSync(seedFile, 'utf8')) as Seed

async function check(page: Page, path: string) {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(`脚本异常：${e.message}`))
  page.on('console', (m) => {
    if (m.type() === 'error') errors.push(`控制台错误：${m.text()}`)
  })
  const res = await page.goto(path)
  expect(res?.status(), '页面应能打开').toBeLessThan(400)
  await page.waitForLoadState('networkidle')
  // 路由出错时显示 TanStack Router 默认的出错页，它会吞掉异常，不触发 pageerror
  await expect(page.getByText('Something went wrong')).toHaveCount(0)
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
  expect(overflow, '页面不能横向滚动').toBeLessThanOrEqual(0)
  expect(errors).toEqual([])
  if (process.env.E2E_SCREENSHOTS) await page.screenshot({ path: test.info().outputPath('page.png'), fullPage: true })
}

test.describe('后台', () => {
  for (const [name, path] of adminPages) {
    test(name, async ({ page }) => {
      await check(page, path)
      await expect(page).toHaveURL(new RegExp(`${path.split('?')[0]}(\\?|$)`))
    })
  }
})

test.describe('对外页面', () => {
  test.use({ storageState: { cookies: [], origins: [] } })

  test('首页', async ({ page }) => check(page, '/'))
  test('付款自助', async ({ page }) => check(page, '/help'))
  test('登录', async ({ page }) => {
    await check(page, '/login')
    await expect(page.getByRole('button', { name: /登录/ })).toBeVisible()
  })

  test('收银台', async ({ page }) => {
    test.skip(instance() !== 'data', '只有有数据的实例有订单')
    await check(page, `/cashier/${seed().pending}`)
    await expect(page.getByText('我已付款').first()).toBeVisible()
  })

  test('收银台选支付方式', async ({ page }) => {
    test.skip(instance() !== 'data', '只有有数据的实例有订单')
    await check(page, `/cashier/${seed().choosing}`)
    await expect(page.getByText('选择支付方式')).toBeVisible()
    await expect(page.getByRole('button', { name: '微信支付' })).toBeVisible()
  })

  test('付款自助查已付订单', async ({ page }) => {
    test.skip(instance() !== 'data', '只有有数据的实例有订单')
    await check(page, `/help?trade_no=${seed().paid}`)
  })
})

// 付款人的手机可能调大了字号，也可能横着拿。收银台的卡片是 overflow-hidden，内容挤出去会被裁掉而不会让页面横向滚动，
// 所以逐个元素看有没有超出屏幕；还要确认主要按钮能滚动到、点得到。只在一个项目里跑，视口和字号由用例自己设。
// 根字号放大后 rem 写的边距也跟着放大：320 宽 150% 大致相当于安卓最大字号或 iOS 页面缩放 150%。
const tight = [
  { name: '窄屏大字号', viewport: { width: 320, height: 640 }, fontSize: '150%' },
  { name: '横屏矮屏幕', viewport: { width: 640, height: 320 }, fontSize: '100%' },
]

async function fits(page: Page, path: string, fontSize: string, ready?: string) {
  await check(page, path)
  if (ready) await expect(page.getByText(ready).first()).toBeVisible()
  await page.addStyleTag({ content: `html { font-size: ${fontSize} !important; }` })
  if (process.env.E2E_SCREENSHOTS) await page.screenshot({ path: test.info().outputPath('tight.png'), fullPage: true })
  const out = await page.evaluate(() => {
    const w = document.documentElement.clientWidth
    // 给读屏用的视觉隐藏元素（例如组件库藏起来的原生 select）本来就放在屏幕外，不算。
    const hidden = (el: Element | null): boolean => {
      for (; el && el !== document.body; el = el.parentElement) {
        const cs = getComputedStyle(el)
        if (cs.clipPath !== 'none' || cs.clip !== 'auto') return true
      }
      return false
    }
    return [...document.querySelectorAll('main *')]
      .filter((el) => {
        const r = el.getBoundingClientRect()
        return r.width > 0 && r.height > 0 && (r.right > w + 0.5 || r.left < -0.5) && !hidden(el)
      })
      .map(
        (el) =>
          `<${el.tagName.toLowerCase()} class="${el.getAttribute('class') ?? ''}"> ${el.textContent?.slice(0, 30) ?? ''}`,
      )
  })
  expect(out, '内容不能超出屏幕').toEqual([])
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
  expect(overflow, '页面不能横向滚动').toBeLessThanOrEqual(0)
}

test.describe('付款人页面：窄屏大字号、横屏矮屏幕', () => {
  test.use({ storageState: { cookies: [], origins: [] } })
  test.beforeEach(() => {
    test.skip(test.info().project.name !== 'data-mobile-light', '视口和字号由用例自己设，跑一个项目就够')
  })
  for (const t of tight) {
    test(`${t.name}：待付款的收银台`, async ({ page }) => {
      await page.setViewportSize(t.viewport)
      await fits(page, `/cashier/${seed().pending}`, t.fontSize)
      const paid = page.getByRole('button', { name: '我已付款' })
      await paid.scrollIntoViewIfNeeded()
      await expect(paid).toBeInViewport()
    })
    test(`${t.name}：选支付方式的收银台`, async ({ page }) => {
      await page.setViewportSize(t.viewport)
      await fits(page, `/cashier/${seed().choosing}`, t.fontSize, '选择支付方式')
    })
    test(`${t.name}：已支付的收银台`, async ({ page }) => {
      await page.setViewportSize(t.viewport)
      await fits(page, `/cashier/${seed().paid}`, t.fontSize)
    })
    test(`${t.name}：付款自助`, async ({ page }) => {
      await page.setViewportSize(t.viewport)
      await fits(page, '/help', t.fontSize)
    })
    test(`${t.name}：付款自助查到订单`, async ({ page }) => {
      await page.setViewportSize(t.viewport)
      // 先开一次收银台，订单记进本机的“最近支付”，自助页上查询结果和最近支付两块都有内容。
      await page.goto(`/cashier/${seed().pending}`)
      await expect(page.getByText('订单已保存在此浏览器', { exact: false })).toBeVisible()
      await fits(page, `/help?trade_no=${seed().pending}`, t.fontSize, '这笔付款有问题')
      await expect(page.getByRole('button', { name: `查询订单 ${seed().pending}` })).toBeAttached()
    })
  }
})
