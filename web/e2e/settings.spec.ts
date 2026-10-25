import { expect, test } from '@playwright/test'

// 数字框以前要失焦或回车才算改动，底部保存条要等离开输入框才出现。现在边输边算：
// 逐个键入时不跳字，输到合法的数就出现保存条；“撤销修改”恢复原值。只看不存，不改实例的设置。

test('收款参数一输入就出现保存条，撤销后恢复', async ({ page }) => {
  test.skip(test.info().project.name !== 'data-desktop-light', '只看交互，跑一个项目就够')
  await page.goto('/admin/settings')
  const input = page.getByRole('textbox', { name: '订单有效期', exact: true })
  const bar = page.getByRole('toolbar', { name: '保存收款参数' })
  await expect(input).not.toHaveValue('')
  const original = await input.inputValue()
  const next = original === '600' ? '900' : '600'
  await expect(bar).toBeHidden()

  await input.fill('')
  await input.pressSequentially(next)
  await expect(input).toHaveValue(next)
  await expect(input).toBeFocused()
  await expect(bar).toBeVisible()

  await bar.getByRole('button', { name: '撤销修改' }).click()
  await expect(input).toHaveValue(original)
  await expect(bar).toBeHidden()
})
