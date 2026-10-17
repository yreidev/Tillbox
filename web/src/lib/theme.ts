// 深浅色模式：默认跟随系统，后台右上角可以手动改，存在本浏览器里。index.html 里的内联脚本在首次渲染前做同样的判断。
// 不用 HeroUI 自带的 useTheme：它的状态在每个组件里各存一份，收银台这类不放切换按钮的页面也要按偏好显示。
import { useSyncExternalStore } from 'react'

export type ThemePref = 'system' | 'light' | 'dark'

const key = 'tillbox-theme'
const changed = 'tillbox-theme-changed'
const media = window.matchMedia('(prefers-color-scheme: dark)')

function readPref(): ThemePref {
  try {
    const v = localStorage.getItem(key)
    return v === 'light' || v === 'dark' ? v : 'system'
  } catch {
    return 'system'
  }
}

// HeroUI 同时认 html 上的 dark / light 类和 data-theme，两个都写上并保持一致。
function apply(pref: ThemePref) {
  const mode = pref === 'dark' || (pref === 'system' && media.matches) ? 'dark' : 'light'
  const root = document.documentElement
  root.classList.remove(mode === 'dark' ? 'light' : 'dark')
  root.classList.add(mode)
  root.dataset.theme = mode
  // 手机浏览器地址栏的颜色跟着页面底色走
  const meta = document.querySelector('meta[name="theme-color"]')
  meta?.setAttribute(
    'content',
    getComputedStyle(document.body).backgroundColor || (mode === 'dark' ? '#060607' : '#f5f5f5'),
  )
}

/** initTheme 在启动时调用一次：应用当前偏好，并在系统切换深浅色、其他标签页改了偏好时跟着变。 */
export function initTheme() {
  apply(current)
  media.addEventListener('change', () => apply(current))
  window.addEventListener('storage', (e) => {
    if (e.key === key) {
      current = readPref()
      apply(current)
      window.dispatchEvent(new Event(changed))
    }
  })
}

export function setThemePref(pref: ThemePref) {
  try {
    if (pref === 'system') localStorage.removeItem(key)
    else localStorage.setItem(key, pref)
  } catch {
    // 存不下（无痕模式等）时只在本页生效
  }
  apply(pref)
  current = pref
  window.dispatchEvent(new Event(changed))
}

let current = readPref()

function subscribe(cb: () => void) {
  window.addEventListener(changed, cb)
  return () => window.removeEventListener(changed, cb)
}

/** useThemePref 返回当前的偏好（跟随系统、浅色、深色）。 */
export function useThemePref(): ThemePref {
  return useSyncExternalStore(subscribe, () => current)
}
