import { createServer } from 'node:http'
import type { AddressInfo } from 'node:net'
import { expect, test, type Page } from '@playwright/test'
import { ok, signMD5, type Merchant } from './epay'

// 在浏览器里走完收款的几条主流程，每条都用自己的商户和订单，不碰别的检查要看的数据：
// 1. 商户网站跳转下单 → 收银台付款 → 回跳商户 → 通知商户 → 后台订单详情；
// 2. 付错金额进流水异常 → 后台人工关联订单 → 通知商户；
// 3. 付款人在自助页提交付款问题 → 拿到查询凭证 → 后台能看到这条申诉。
// 付款用演示渠道的开发接口模拟。只在有数据的实例上跑，桌面和手机宽度各一遍（浅色就够，深色只影响外观）。

const headers = { 'X-Tillbox': '1' }

test.beforeEach(() => {
  test.skip(!/^data-(desktop|mobile)-light$/.test(test.info().project.name), '造数据的流程，桌面和手机各跑一遍就够')
  test.setTimeout(120_000)
})

/** merchantSite 是测试里起的“商户网站”：收通知（回 success）和付款完成后的回跳。 */
async function merchantSite() {
  const notices: Record<string, string>[] = []
  const server = createServer((req, res) => {
    const url = new URL(req.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/notify') {
      notices.push(Object.fromEntries(url.searchParams))
      res.end('success')
      return
    }
    res.setHeader('Content-Type', 'text/html; charset=utf-8')
    res.end('<!doctype html><title>商户页面</title><h1>商户页面</h1>')
  })
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  const origin = `http://127.0.0.1:${(server.address() as AddressInfo).port}`
  return {
    origin,
    notices,
    close: () =>
      new Promise<void>((resolve) => {
        server.closeAllConnections()
        server.close(() => resolve())
      }),
  }
}

type Site = Awaited<ReturnType<typeof merchantSite>>

async function newMerchant(page: Page, name: string): Promise<Merchant> {
  const r = await ok<{ merchant: { id: number }; key: string }>(
    page.request.post('/web-api/admin/merchants', { headers, data: { name, enabled: true } }),
  )
  return { pid: String(r.merchant.id), key: r.key }
}

let seq = 0

/** orderParams 是商户网站发给 submit.php / mapi.php 的下单参数（已签名）。 */
function orderParams(m: Merchant, site: Site, money: string, extra: Record<string, string> = {}) {
  seq++
  const p = {
    pid: m.pid,
    type: 'alipay',
    out_trade_no: `E2E${Date.now()}${seq}`,
    notify_url: `${site.origin}/notify`,
    return_url: `${site.origin}/return`,
    name: `浏览器测试商品 ${seq}`,
    money,
    ...extra,
  }
  return { ...p, sign: signMD5(p, m.key), sign_type: 'MD5' }
}

async function createOrder(page: Page, m: Merchant, site: Site, money: string) {
  const res = await ok<{ code: number; trade_no: string }>(
    page.request.post('/mapi.php', { form: orderParams(m, site, money, { clientip: '127.0.0.1' }) }),
  )
  expect(res.code, JSON.stringify(res)).toBe(1)
  return res.trade_no
}

/** expectNotified 等商户网站收到这笔订单的成功通知，并按商户密钥验签。 */
async function expectNotified(site: Site, m: Merchant, tradeNo: string, money: string) {
  await expect
    .poll(() => site.notices.find((n) => n.trade_no === tradeNo), { timeout: 60_000, message: '商户应收到通知' })
    .toBeTruthy()
  const n = site.notices.find((x) => x.trade_no === tradeNo)!
  expect(n.trade_status).toBe('TRADE_SUCCESS')
  expect(n.money).toBe(money)
  expect(n.sign, '通知签名应能用商户密钥验过').toBe(signMD5(n, m.key))
}

/** expectOrderDone 在后台订单页按订单号搜到这笔订单，详情里是已支付、商户已确认。 */
async function expectOrderDone(page: Page, tradeNo: string) {
  await page.goto(`/admin/orders?q=${tradeNo}`)
  await page.getByText(tradeNo).first().click()
  const drawer = page.getByRole('dialog')
  await expect(drawer.getByText('已支付').first()).toBeVisible()
  await expect(drawer.getByText('商户已确认').first()).toBeVisible()
}

test('付款全流程：收银台付款、回跳商户、通知商户', async ({ page }) => {
  const site = await merchantSite()
  try {
    const m = await newMerchant(page, `浏览器测试商户 ${test.info().project.name}`)
    // 商户网站把付款人带到 submit.php，跳到收银台。
    await page.goto(`/submit.php?${new URLSearchParams(orderParams(m, site, '3.00'))}`)
    await page.waitForURL(/\/cashier\/\d{19}$/)
    const tradeNo = page.url().match(/(\d{19})$/)![1]
    await expect(page.getByText('请付款')).toBeVisible()

    // 付款人扫码付款后点“我已付款”，收银台等到账确认。
    await ok(page.request.post('/web-api/dev/pay', { headers, data: { trade_no: tradeNo, payer: '浏览器测试' } }))
    await page.getByRole('button', { name: '我已付款' }).click()
    await expect(page.getByText('支付成功')).toBeVisible({ timeout: 60_000 })

    // 3 秒后自动回到商户网站，回跳参数带签名。
    await page.waitForURL((u) => u.origin === site.origin, { timeout: 15_000 })
    const ret = Object.fromEntries(new URL(page.url()).searchParams)
    expect(ret.trade_no).toBe(tradeNo)
    expect(ret.trade_status).toBe('TRADE_SUCCESS')
    expect(ret.sign, '回跳签名应能用商户密钥验过').toBe(signMD5(ret, m.key))

    await expectNotified(site, m, tradeNo, '3.00')
    await expectOrderDone(page, tradeNo)
  } finally {
    await site.close()
  }
})

test('付错金额：流水异常里人工关联订单后通知商户', async ({ page }) => {
  const site = await merchantSite()
  try {
    const m = await newMerchant(page, `浏览器测试商户 ${test.info().project.name}`)
    const tradeNo = await createOrder(page, m, site, '6.00')
    // 两个项目并行跑，各用一个别处没有的金额，在异常列表里找得到自己那笔。
    const paid = test.info().project.name.startsWith('data-desktop') ? '4.37' : '4.73'
    await ok(page.request.post('/web-api/dev/pay', { headers, data: { trade_no: tradeNo, amount: paid } }))
    await expect
      .poll(
        async () => {
          await page.request.post('/web-api/admin/ledger/poll', { headers, data: {} })
          const r = await ok<{ items: { transaction?: { amount: number } }[] }>(
            page.request.get('/web-api/admin/anomalies?status=open', { headers }),
          )
          return r.items.some((a) => a.transaction?.amount === Math.round(Number(paid) * 100))
        },
        { timeout: 60_000, intervals: [2_000], message: '付错金额应进流水异常' },
      )
      .toBe(true)

    await page.goto('/admin/ledger')
    const card = page
      .locator('div', { has: page.getByText(`¥${paid}`) })
      .filter({ has: page.getByRole('button', { name: '关联订单' }) })
      .last()
    await card.getByRole('button', { name: '关联订单' }).click()
    const dialog = page.getByRole('dialog')
    await dialog.getByRole('textbox', { name: '平台订单号' }).fill(tradeNo)
    await dialog.getByRole('textbox', { name: '理由' }).fill('付款人少付，已电话确认补差价')
    await dialog.getByRole('button', { name: '关联并标为已支付' }).click()
    await expect(page.getByText('已关联，订单已标记为已支付')).toBeVisible()
    await expect(page.getByText(`¥${paid}`)).toBeHidden()

    await expectNotified(site, m, tradeNo, '6.00')
    await expectOrderDone(page, tradeNo)
  } finally {
    await site.close()
  }
})

test('付款人在自助页提交付款问题，后台能看到', async ({ page }) => {
  const site = await merchantSite()
  try {
    const m = await newMerchant(page, `浏览器测试商户 ${test.info().project.name}`)
    const tradeNo = await createOrder(page, m, site, '7.00')
    const ref = `E2E${Date.now()}${test.info().project.name.startsWith('data-desktop') ? 'D' : 'M'}`

    // 从收银台的自助入口过来，页面自动查这笔订单。
    await page.goto(`/help?trade_no=${tradeNo}`)
    await page.getByRole('button', { name: '这笔付款有问题' }).click()
    await expect(page.getByRole('textbox', { name: '问题订单号' })).toHaveValue(tradeNo)
    await page.getByRole('textbox', { name: '支付宝交易号' }).fill(ref)
    await page.getByRole('textbox', { name: '实际付款金额（元）' }).fill('7.00')
    await page.getByRole('textbox', { name: '补充说明（可选）' }).fill('付了款页面一直没变')
    await page.getByRole('button', { name: '提交并获取查询凭证' }).click()
    await expect(page.getByText(/^[a-f0-9]{64}$/).first()).toBeVisible()

    // 后台“AI 与付款申诉”的全部列表里有这条，详情里是付款人填的交易号。
    const cases = await ok<{ items: { id: number; payment_ref: string }[] }>(
      page.request.get('/web-api/admin/assistance?page_size=100', { headers }),
    )
    const c = cases.items.find((x) => x.payment_ref === ref)
    expect(c, '后台应有这条申诉').toBeTruthy()
    await page.goto('/admin/assistance')
    await page.getByRole('tab', { name: '全部' }).click()
    await page.getByText(`#${c!.id} ·`).first().click()
    await expect(page.getByRole('dialog').getByText(ref)).toBeVisible()
  } finally {
    await site.close()
  }
})
