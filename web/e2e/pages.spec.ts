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
  ['数据归档', '/admin/operations'],
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

  test('付款自助查已付订单', async ({ page }) => {
    test.skip(instance() !== 'data', '只有有数据的实例有订单')
    await check(page, `/help?trade_no=${seed().paid}`)
  })
})
