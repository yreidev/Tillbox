import { expect, test, type Page } from '@playwright/test'

// 保存冲突：页面打开后在别处（这里直接调接口，不带 If-Match）改了站点名称，页面再保存时应提示冲突、保留草稿；
// “载入最新”换成服务器上的内容，“用我的修改覆盖”不带版本再存一次。
// 改的是实例共用的设置，只在一个项目里跑，跑完改回原来的名字。

const headers = { 'X-Tillbox': '1' }

const siteName = async (page: Page) => {
  const r = await page.request.get('/web-api/admin/settings', { headers })
  return ((await r.json()) as { site: { name: string } }).site.name
}

const rename = async (page: Page, name: string) => {
  const r = await page.request.put('/web-api/admin/settings/site', { headers, data: { name } })
  expect(r.status(), '在别处改名应成功').toBe(200)
}

test('保存冲突：载入最新和用我的修改覆盖', async ({ page }) => {
  test.skip(test.info().project.name !== 'data-desktop-light', '改的是共用的设置，跑一个项目就够')
  const original = await siteName(page)
  try {
    await page.goto('/admin/settings?tab=site')
    const input = page.getByRole('textbox', { name: '站点名称' })
    const form = page.locator('form', { has: input })
    const notice = page.getByText('站点名称在你打开之后已经在别处改过')
    await expect(input).toHaveValue(original)

    // 别处先改，页面上的草稿保存不了，草稿留着。
    await rename(page, '别处改的名字')
    await input.fill('我的草稿')
    await form.getByRole('button', { name: '保存' }).click()
    await expect(notice).toBeVisible()
    await expect(input).toHaveValue('我的草稿')
    expect(await siteName(page)).toBe('别处改的名字')

    // 载入最新：换成服务器上的内容，提示消失，接着改能直接保存。
    await page.getByRole('button', { name: '载入最新（放弃我的修改）' }).click()
    await expect(input).toHaveValue('别处改的名字')
    await expect(notice).toBeHidden()

    // 再来一次，这回用我的修改覆盖。
    await rename(page, '又被别处改了')
    await input.fill('我的草稿')
    await form.getByRole('button', { name: '保存' }).click()
    await expect(notice).toBeVisible()
    await page.getByRole('button', { name: '用我的修改覆盖' }).click()
    await expect(notice).toBeHidden()
    await expect.poll(() => siteName(page)).toBe('我的草稿')
  } finally {
    await rename(page, original)
  }
})
