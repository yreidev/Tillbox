import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { tanstackRouter } from '@tanstack/router-plugin/vite'

export default defineConfig(({ mode }) => {
  // 开发时把接口转发到本机后端（默认 127.0.0.1:8080，可用环境变量 TILLBOX_DEV_BACKEND 改）。
  const backend = loadEnv(mode, '.', 'TILLBOX_').TILLBOX_DEV_BACKEND || 'http://127.0.0.1:8080'
  return {
    plugins: [
      // 按 src/routes/ 生成路由表，页面组件自动拆包：收银台不会下载后台页面的代码。必须排在 react() 前面。
      tanstackRouter({ target: 'react', autoCodeSplitting: true }),
      react(),
      // HeroUI v3 的样式基于 Tailwind CSS v4
      tailwindcss(),
    ],
    build: {
      // 构建到后端的嵌入目录，go build 时打进二进制
      outDir: '../internal/webui/dist',
      emptyOutDir: true,
      chunkSizeWarningLimit: 1500,
    },
    server: {
      proxy: Object.fromEntries(
        ['/web-api', '/submit.php', '/mapi.php', '/api.php', '/api/', '/healthz'].map((p) => [p, backend]),
      ),
    },
  }
})
