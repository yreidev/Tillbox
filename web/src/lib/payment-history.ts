import { useEffect, useState } from 'react'

export interface RecentPayment {
  trade_no: string
  pay_amount: string
  state: string
  created_at: number
  visited_at: number
}

const key = 'tillbox-payment-history-v1'
const changed = 'tillbox-payment-history-changed'
const lifetime = 30 * 24 * 60 * 60 * 1000
const states = new Set(['pending', 'paid', 'expired', 'closed', 'disputed'])

// Store only orders this browser has opened or successfully queried. Order
// numbers stay strings: converting the 19 digits to Number loses precision.
export function recentPayments(): RecentPayment[] {
  try {
    return cleanPayments(JSON.parse(localStorage.getItem(key) || '[]'), Date.now())
  } catch {
    return []
  }
}

// cleanPayments 过滤本机存的记录：格式不对、重复、过期的都丢掉，最近的在前，最多 20 条。
export function cleanPayments(value: unknown, now: number): RecentPayment[] {
  if (!Array.isArray(value)) return []
  const seen = new Set<string>()
  return value
    .filter((item): item is RecentPayment => {
      if (!item || typeof item !== 'object') return false
      const p = item as RecentPayment
      if (
        typeof p.trade_no !== 'string' ||
        !/^\d{19}$/.test(p.trade_no) ||
        seen.has(p.trade_no) ||
        typeof p.pay_amount !== 'string' ||
        !/^\d{1,7}\.\d{2}$/.test(p.pay_amount) ||
        !states.has(p.state) ||
        !Number.isFinite(p.created_at) ||
        !Number.isFinite(p.visited_at) ||
        p.visited_at < now - lifetime ||
        p.visited_at > now + 60_000
      )
        return false
      seen.add(p.trade_no)
      return true
    })
    .sort((a, b) => b.visited_at - a.visited_at)
    .slice(0, 20)
}

export function rememberPayment(payment: Omit<RecentPayment, 'visited_at'>): boolean {
  try {
    const items = [
      { ...payment, visited_at: Date.now() },
      ...recentPayments().filter((p) => p.trade_no !== payment.trade_no),
    ].slice(0, 20)
    localStorage.setItem(key, JSON.stringify(items))
    window.dispatchEvent(new Event(changed))
    return true
  } catch {
    return false
  }
}

export function clearPayments(): boolean {
  try {
    localStorage.removeItem(key)
    window.dispatchEvent(new Event(changed))
    return true
  } catch {
    return false
  }
}

export function usePaymentHistory() {
  const [items, setItems] = useState(recentPayments)
  useEffect(() => {
    const refresh = () => setItems(recentPayments())
    window.addEventListener('storage', refresh)
    window.addEventListener(changed, refresh)
    return () => {
      window.removeEventListener('storage', refresh)
      window.removeEventListener(changed, refresh)
    }
  }, [])
  return items
}

export interface SavedReceipt {
  receipt: string
  saved_at: number
}

const receiptKey = 'tillbox-support-receipts-v1'
const legacyReceiptKey = 'tillbox-support-receipt' // 旧版只存一张凭证
const receiptPattern = /^[a-f0-9]{64}$/

// 本机保存的申诉查询凭证，最近的在前，最多 10 张。旧版存的那一张也带上。
export function savedReceipts(): SavedReceipt[] {
  try {
    const value: unknown = JSON.parse(localStorage.getItem(receiptKey) || '[]')
    const items = (Array.isArray(value) ? value : []).filter((item): item is SavedReceipt => {
      const r = item as SavedReceipt
      return (
        !!r &&
        typeof r === 'object' &&
        typeof r.receipt === 'string' &&
        receiptPattern.test(r.receipt) &&
        Number.isFinite(r.saved_at)
      )
    })
    const legacy = localStorage.getItem(legacyReceiptKey) || ''
    if (receiptPattern.test(legacy) && !items.some((r) => r.receipt === legacy))
      items.push({ receipt: legacy, saved_at: 0 })
    return items.sort((a, b) => b.saved_at - a.saved_at).slice(0, 10)
  } catch {
    return []
  }
}

export function rememberReceipt(receipt: string): boolean {
  try {
    const items = [{ receipt, saved_at: Date.now() }, ...savedReceipts().filter((r) => r.receipt !== receipt)].slice(
      0,
      10,
    )
    localStorage.setItem(receiptKey, JSON.stringify(items))
    localStorage.removeItem(legacyReceiptKey)
    return true
  } catch {
    return false
  }
}
