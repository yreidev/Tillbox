import { createHash } from 'node:crypto'
import { expect, type APIResponse } from '@playwright/test'

// 浏览器检查里造数据、验签用的易支付工具。

export interface Merchant {
  pid: string
  key: string
}

/** ok 要求接口返回 2xx，返回解析后的 JSON。 */
export async function ok<T>(res: Promise<APIResponse>): Promise<T> {
  const r = await res
  expect(r.ok(), `${r.url()}：${r.status()} ${await r.text()}`).toBeTruthy()
  return (await r.json()) as T
}

/** 易支付 V1 的 MD5 签名：去掉 sign、sign_type 和空值，按参数名排序拼成 a=1&b=2，接上商户密钥取 MD5。 */
export function signMD5(p: Record<string, string>, key: string) {
  const content = Object.keys(p)
    .filter((k) => k !== 'sign' && k !== 'sign_type' && p[k] !== '')
    .sort()
    .map((k) => `${k}=${p[k]}`)
    .join('&')
  return createHash('md5')
    .update(content + key)
    .digest('hex')
}
