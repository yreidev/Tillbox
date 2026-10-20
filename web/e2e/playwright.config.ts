import { defineConfig, devices } from '@playwright/test'
import { baseURL, instances } from './instances'

// 浏览器逐页检查：起两个演示模式的 Tillbox（空数据、有数据），每个实例按桌面和手机宽度、
// 浅色和深色各打开一遍所有页面。要先 make build 生成 bin/tillbox，用法见 docs/development.md。
const viewports = {
  desktop: { viewport: { width: 1280, height: 800 } },
  mobile: devices['Pixel 7'],
}
const schemes = ['light', 'dark'] as const

export default defineConfig({
  testDir: '.',
  outputDir: '../test-results',
  forbidOnly: !!process.env.CI,
  // 浏览器吃内存，本机和 CI 都只开两个
  workers: 2,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never', outputFolder: '../playwright-report' }]] : 'list',
  use: {
    locale: 'zh-CN',
    timezoneId: 'Asia/Shanghai',
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
  },
  webServer: instances.map((i) => ({
    name: i.name,
    command: `rm -rf '${i.dataDir}' && exec ../../bin/tillbox`,
    env: {
      TILLBOX_DEMO: 'true',
      TILLBOX_LISTEN: `127.0.0.1:${i.port}`,
      TILLBOX_DATA_DIR: i.dataDir,
      TILLBOX_LOG_LEVEL: 'warn',
    },
    url: `${baseURL(i)}/readyz`,
    // 首次启动会在日志里打印管理员密码（base64url），命名分组的值会放进同名（大写）环境变量
    wait: { stderr: new RegExp(`password=(?<${i.passwordEnv.toLowerCase()}>[\\w-]+)`) },
    reuseExistingServer: false,
    gracefulShutdown: { signal: 'SIGTERM', timeout: 5000 },
    stderr: process.env.CI ? 'pipe' : 'ignore',
  })),
  projects: [
    { name: 'setup', testMatch: /setup\.ts$/ },
    ...instances.flatMap((i) =>
      Object.entries(viewports).flatMap(([vp, device]) =>
        schemes.map((colorScheme) => ({
          name: `${i.name}-${vp}-${colorScheme}`,
          testMatch: /\.spec\.ts$/,
          dependencies: ['setup'],
          metadata: { instance: i.name },
          use: { ...device, colorScheme, baseURL: baseURL(i), storageState: i.storageState },
        })),
      ),
    ),
  ],
})
