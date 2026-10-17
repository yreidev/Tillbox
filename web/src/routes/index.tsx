import { createFileRoute, redirect } from '@tanstack/react-router'

export const Route = createFileRoute('/')({
  validateSearch: (s: Record<string, unknown>) => ({ trade_no: typeof s.trade_no === 'string' ? s.trade_no : '' }),
  beforeLoad: ({ search }) => {
    throw redirect({ to: '/help', search, replace: true })
  },
})
