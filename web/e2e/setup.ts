import { writeFile } from 'node:fs/promises'
import { expect, test as setup, type APIRequestContext } from '@playwright/test'
import { ok, signMD5, type Merchant } from './epay'
import { baseURL, instances, seedFile, type Seed } from './instances'

// 两个实例都登录并保存 Cookie；有数据的实例再通过接口造一批数据：两个商户、待付和已付订单、
// 两条对不上订单的流水异常（一笔少付、一笔金额谁也对不上）、一条付款申诉。商户通知地址连不上，通知页会有重试中的通知。

let seq = 0

async function createOrder(api: APIRequestContext, m: Merchant, money: string) {
  seq++
  const p = {
    pid: m.pid,
    type: 'alipay',
    out_trade_no: `E2E${Date.now()}${seq}`,
    notify_url: 'http://127.0.0.1:9/notify',
    return_url: 'http://127.0.0.1:9/return',
    name: `测试商品 ${seq}`,
    money,
    clientip: '127.0.0.1',
  }
  const res = await ok<{ code: number; trade_no: string }>(
    api.post('/mapi.php', { form: { ...p, sign: signMD5(p, m.key), sign_type: 'MD5' } }),
  )
  expect(res.code, JSON.stringify(res)).toBe(1)
  return res.trade_no
}

async function waitStatus(api: APIRequestContext, tradeNo: string, status: string) {
  await expect
    .poll(
      async () => {
        await api.post('/web-api/admin/ledger/poll', { data: {} })
        const res = await ok<{ items: { status: string }[] }>(api.get(`/web-api/admin/orders?q=${tradeNo}`))
        return res.items[0]?.status
      },
      { timeout: 60_000, intervals: [2_000] },
    )
    .toBe(status)
}

async function seed(api: APIRequestContext): Promise<Seed> {
  const merchants: Merchant[] = []
  for (const name of ['演示商城', '演示论坛']) {
    const res = await ok<{ merchant: { id: number }; key: string }>(
      api.post('/web-api/admin/merchants', { data: { name, enabled: true } }),
    )
    merchants.push({ pid: String(res.merchant.id), key: res.key })
  }
  const [shop, forum] = merchants
  const paid = await createOrder(api, shop, '12.00')
  const underpaid = await createOrder(api, forum, '30.00')
  const pending = await createOrder(api, shop, '5.00')
  await createOrder(api, forum, '1.00')

  await ok(api.post('/web-api/dev/pay', { data: { trade_no: paid } }))
  await ok(api.post('/web-api/dev/pay', { data: { trade_no: underpaid, amount: '29.00' } }))
  await ok(api.post('/web-api/dev/pay', { data: { amount: '8.88', payer: '陌生付款人' } }))
  await waitStatus(api, paid, 'paid')
  await expect
    .poll(async () => (await ok<{ total: number }>(api.get('/web-api/admin/anomalies?status=open'))).total, {
      timeout: 60_000,
      intervals: [2_000],
    })
    .toBeGreaterThanOrEqual(2)

  await ok(
    api.post('/web-api/self-service/claims', {
      data: {
        trade_no: pending,
        amount: '5.00',
        kind: 'not_received',
        payment_ref: '2026102022001400000000000001',
        paid_at: Date.now() - 60_000,
        message: '付了款页面还没变',
        contact: '',
      },
    }),
  )
  return { pending, paid }
}

for (const inst of instances) {
  setup(`登录${inst.label}实例`, async ({ playwright }) => {
    setup.setTimeout(180_000)
    const api = await playwright.request.newContext({ baseURL: baseURL(inst), extraHTTPHeaders: { 'X-Tillbox': '1' } })
    const password = process.env[inst.passwordEnv]
    expect(password, '启动日志里没找到管理员密码').toBeTruthy()
    await ok(api.post('/web-api/auth/login', { data: { password } }))
    if (inst.name === 'data') await writeFile(seedFile, JSON.stringify(await seed(api)))
    await api.storageState({ path: inst.storageState })
    await api.dispose()
  })
}
