/** @type {import('prettier').Config} */
export default {
  semi: false,
  singleQuote: true,
  printWidth: 120,
  plugins: ['prettier-plugin-tailwindcss'],
  // Tailwind v4 从样式入口读主题，按它排序类名；cx() 里的类名也排
  tailwindStylesheet: './src/styles/theme.css',
  tailwindFunctions: ['cx'],
}
