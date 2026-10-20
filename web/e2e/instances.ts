import path from 'node:path'

// 浏览器检查用的两个演示模式实例：空数据的看空列表兜底，有数据的看各种状态。
// 每次运行前数据目录清空重建；管理员密码从首次启动的日志里取（见 playwright.config.ts 的 wait）。
export interface Instance {
  name: 'empty' | 'data'
  label: string
  port: number
  dataDir: string
  /** 启动日志里的管理员密码存进这个环境变量 */
  passwordEnv: string
  /** 登录后的 Cookie，后台页面的检查用它 */
  storageState: string
}

const dir = path.join(import.meta.dirname, '.data')

export const instances: Instance[] = [
  { name: 'empty', label: '空数据', port: 18591 },
  { name: 'data', label: '有数据', port: 18592 },
].map((i) => ({
  ...(i as Pick<Instance, 'name' | 'label' | 'port'>),
  dataDir: path.join(dir, i.name),
  passwordEnv: `TILLBOX_E2E_${i.name.toUpperCase()}_PASSWORD`,
  storageState: path.join(dir, `${i.name}-auth.json`),
}))

export const baseURL = (i: Pick<Instance, 'port'>) => `http://127.0.0.1:${i.port}`

/** 有数据的实例造好的订单号，页面检查要打开它们的收银台和自助查询 */
export const seedFile = path.join(dir, 'seed.json')

export interface Seed {
  pending: string
  paid: string
}
