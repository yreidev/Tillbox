// 后台和收银台接口的请求封装。修改类请求都带 X-Tillbox 头（配合 SameSite=Strict 的 cookie 防 CSRF）。

import type { ChannelView, Me, Overview } from './types'

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

type Method = 'GET' | 'POST' | 'PUT' | 'DELETE'

export async function request<T>(method: Method, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { 'X-Tillbox': '1', Accept: 'application/json' }
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  let res: Response
  try {
    res = await fetch(path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: 'same-origin',
    })
  } catch {
    throw new ApiError(0, '连不上服务器，请检查网络')
  }
  const text = await res.text()
  let data: unknown = undefined
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = undefined
    }
  }
  if (!res.ok) {
    const msg = (data as { error?: string } | undefined)?.error ?? `请求失败（HTTP ${res.status}）`
    throw new ApiError(res.status, msg)
  }
  return data as T
}

export const api = {
  get: <T>(path: string) => request<T>('GET', path),
  post: <T>(path: string, body?: unknown) => request<T>('POST', path, body ?? {}),
  put: <T>(path: string, body: unknown) => request<T>('PUT', path, body),
  del: <T>(path: string) => request<T>('DELETE', path),
}

// query 把筛选条件拼成查询串，空值不带。
export function query(params: Record<string, string | number | undefined | null>): string {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === '') continue
    q.set(k, String(v))
  }
  const s = q.toString()
  return s ? `?${s}` : ''
}

// parseSearchValue 解析地址栏查询参数的值。平台订单号为 19 位，不能先转成 JS number，否则会丢精度。
export function parseSearchValue(value: string): unknown {
  return /^-?\d{16,}$/.test(value) ? value : (JSON.parse(value) as unknown)
}

// meQuery 是当前登录的管理员和站点信息，后台各页共用。
export const meQuery = { queryKey: ['me'], queryFn: () => api.get<Me>('/web-api/auth/me'), staleTime: 60_000 }

// channelsQuery 是支付渠道列表（带运行状态），渠道页、商户页、订单和流水筛选共用。
export const channelsQuery = {
  queryKey: ['channels'],
  queryFn: () => api.get<{ items: ChannelView[]; demo: boolean }>('/web-api/admin/channels'),
}

// overviewQuery 是概览数据（近 days 天）。侧栏角标、概览页和订单页的待支付数共用缓存。
export const overviewQuery = (days: number) => ({
  queryKey: ['overview', days],
  queryFn: () => api.get<Overview>(`/web-api/admin/overview?days=${days}`),
})

// casesCountQuery 是各状态的申诉数（侧栏角标和概览页用）。
export const casesCountQuery = {
  queryKey: ['assistance', 'counts'],
  queryFn: () => api.get<{ counts: Record<string, number> }>('/web-api/admin/assistance?status=manual&page=1'),
}
