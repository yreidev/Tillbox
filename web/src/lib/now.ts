import { useSyncExternalStore } from 'react'

// 渲染时不能直接调 Date.now()：同一份数据每次渲染的结果不一样。
// 要显示“几分钟前”“下一轮还有多久”的组件用 useNow，大家共用一个每 5 秒走一次的时钟。
const step = 5_000
let now = Date.now()
let timer: ReturnType<typeof setInterval> | undefined
const listeners = new Set<() => void>()

function tick() {
  now = Date.now()
  listeners.forEach((l) => l())
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  if (!timer) {
    timer = setInterval(tick, step)
    tick() // 没人用时时钟是停的，重新开始先校准一次
  }
  return () => {
    listeners.delete(listener)
    if (!listeners.size) {
      clearInterval(timer)
      timer = undefined
    }
  }
}

export function useNow() {
  return useSyncExternalStore(subscribe, () => now)
}
