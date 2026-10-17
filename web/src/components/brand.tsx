/** siteName 是“设置 → 站点”里的站点名。后端返回页面时写进了应用名，登录前的页面也能用。 */
export function siteName() {
  return document.querySelector<HTMLMetaElement>('meta[name="application-name"]')?.content || 'Tillbox'
}

// 标志“小票加对勾”：主色圆角方块上一张白色收据，中间一个对勾，意思是“已收款”。和 web/icons/tillbox.svg 是同一个图形。
export function Logo({ size = 28, title }: { size?: number; title?: string }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 64 64"
      role={title ? 'img' : undefined}
      aria-hidden={title ? undefined : true}
      className="block shrink-0"
    >
      {title && <title>{title}</title>}
      <rect width="64" height="64" rx="15" fill="var(--accent)" />
      <path d="M20 11h24a3 3 0 0 1 3 3v39l-5-4-5 4-5-4-5 4-5-4-5 4V14a3 3 0 0 1 3-3z" fill="#fff" />
      <path
        d="M24 30.5l5.5 5.5L40.5 25"
        fill="none"
        stroke="var(--accent)"
        strokeWidth="4.5"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  )
}
