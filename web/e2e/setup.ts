import { writeFile } from 'node:fs/promises'
import { expect, test as setup, type APIRequestContext } from '@playwright/test'
import { ok, signMD5, type Merchant } from './epay'
import { baseURL, instances, seedFile, type Seed } from './instances'

// 两个实例都登录并保存 Cookie；有数据的实例再通过接口造一批数据：两个商户、待付和已付订单、
// 两条对不上订单的流水异常（一笔少付、一笔金额谁也对不上）、一条付款申诉。商户通知地址连不上，通知页会有重试中的通知。
// 另建一个第三方易支付渠道，对接的第三方平台就是这个实例自己（用“演示论坛”的商户号，它只开了支付宝，向它预下单微信会被拒）；
// “演示商城”的微信支付走这个渠道，于是有一笔没带支付方式、等付款人在收银台选的订单。

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

// submitOrder 跳转下单，不带支付方式（接口下单必须带），返回跳到的收银台的订单号。
async function submitOrder(api: APIRequestContext, m: Merchant, money: string) {
  seq++
  const p = {
    pid: m.pid,
    out_trade_no: `E2E${Date.now()}${seq}`,
    notify_url: 'http://127.0.0.1:9/notify',
    return_url: 'http://127.0.0.1:9/return',
    name: `测试商品 ${seq}`,
    money,
  }
  const res = await api.post('/submit.php', {
    form: { ...p, sign: signMD5(p, m.key), sign_type: 'MD5' },
    maxRedirects: 0,
  })
  const to = res.headers()['location'] ?? ''
  expect(to, `${res.status()} ${await res.text()}`).toMatch(/\/cashier\/\d{19}$/)
  return to.slice(-19)
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

async function seed(api: APIRequestContext, origin: string): Promise<Seed> {
  const merchants: Merchant[] = []
  for (const name of ['演示商城', '演示论坛']) {
    const res = await ok<{ merchant: { id: number }; key: string }>(
      api.post('/web-api/admin/merchants', { data: { name, enabled: true } }),
    )
    merchants.push({ pid: String(res.merchant.id), key: res.key })
  }
  const [shop, forum] = merchants
  const { items } = await ok<{ items: { id: number; type: string }[] }>(api.get('/web-api/admin/channels'))
  const demo = items.find((c) => c.type === 'demo')!.id
  const third = await ok<{ channel: { id: number } }>(
    api.post('/web-api/admin/channels', {
      data: {
        type: 'epay',
        name: '演示第三方',
        enabled: true,
        config: { api_url: `${origin}/`, version: 1, pid: forum.pid, key: forum.key, types: ['alipay', 'wxpay'] },
      },
    }),
  )
  await ok(
    api.put(`/web-api/admin/merchants/${shop.pid}`, {
      data: { name: '演示商城', enabled: true, routes: { alipay: demo, wxpay: third.channel.id } },
    }),
  )
  const choosing = await submitOrder(api, shop, '9.90')
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
  return { pending, paid, choosing }
}

for (const inst of instances) {
  setup(`登录${inst.label}实例`, async ({ playwright }) => {
    setup.setTimeout(180_000)
    const api = await playwright.request.newContext({ baseURL: baseURL(inst), extraHTTPHeaders: { 'X-Tillbox': '1' } })
    const password = process.env[inst.passwordEnv]
    expect(password, '启动日志里没找到管理员密码').toBeTruthy()
    await ok(api.post('/web-api/auth/login', { data: { password } }))
    if (inst.name === 'data') await writeFile(seedFile, JSON.stringify(await seed(api, baseURL(inst))))
    await api.storageState({ path: inst.storageState })
    await api.dispose()
  })
}
