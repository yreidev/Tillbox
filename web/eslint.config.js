import js from '@eslint/js'
import { defineConfig, globalIgnores } from 'eslint/config'
import reactHooks from 'eslint-plugin-react-hooks'
import globals from 'globals'
import tseslint from 'typescript-eslint'

export default defineConfig([
  // 路由表由 TanStack Router 生成；浏览器检查的报告和演示实例的数据目录是运行时生成的
  globalIgnores(['src/routeTree.gen.ts', '.tanstack/', 'test-results/', 'playwright-report/', 'e2e/.data/']),
  {
    files: ['**/*.{ts,tsx}'],
    extends: [js.configs.recommended, tseslint.configs.recommendedTypeChecked, reactHooks.configs.flat.recommended],
    languageOptions: {
      globals: globals.browser,
      // 带类型信息检查，能发现可空值没兜底、Promise 没处理这类问题
      parserOptions: { projectService: true, tsconfigRootDir: import.meta.dirname },
    },
    rules: {
      // TanStack Router 用 throw redirect(...) 跳转，redirect 不是 Error
      '@typescript-eslint/only-throw-error': [
        'error',
        { allow: [{ from: 'package', package: '@tanstack/router-core', name: 'Redirect' }] },
      ],
      // onPress、onSubmit 可以直接给 async 函数；约定在函数里 catch 错误并提示用户
      '@typescript-eslint/no-misused-promises': ['error', { checksVoidReturn: { attributes: false } }],
    },
  },
])
