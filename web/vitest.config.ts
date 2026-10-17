import { defineConfig } from 'vitest/config'

// 前端只测纯逻辑（金额和时间的显示、查询参数、本机付款记录的过滤），不渲染组件，
// 所以用 node 环境，也不加载 vite.config.ts 里的插件。
export default defineConfig({
  test: { include: ['src/**/*.test.ts'], environment: 'node' },
})
