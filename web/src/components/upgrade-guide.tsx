import { useState, type ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Button, Link, ScrollShadow, Stepper, Surface, Tabs, Typography } from '@yreidev/ui'
import { Copy } from '@gravity-ui/icons'
import { api } from '../lib/api'
import { time } from '../lib/format'
import { Code, Panel, Pill, SettingsRow, copyText, enter, toastError, toastOK, type Tone } from '../lib/ui'

type DeployMode = 'docker' | 'kubernetes' | 'binary'

export interface UpdateView {
  current: string
  latest: string
  newer: boolean
  notes_url: string
  checked_at: number
  error: string
  check: boolean
  revision: number
  /** 比当前新的各版更新内容，新的在前；没取到时为空 */
  releases: { version: string; date: string; items: string[] }[]
  /** 升级要执行几项数据库迁移，-1 是没查到 */
  migrations: number
  deploy: { mode: DeployMode; namespace: string }
  image: string
}

export const updateQuery = { queryKey: ['update'], queryFn: () => api.get<UpdateView>('/web-api/admin/update') }

/** 升级前备份要看的那一部分，和“备份与归档”页共用一个查询 */
interface Operations {
  backup: { supported: boolean; state: { last_ok_at: number; last_error: string } }
}

const operationsQuery = {
  queryKey: ['operations'],
  queryFn: () => api.get<Operations>('/web-api/admin/operations'),
}

const modeText: Record<DeployMode, string> = { docker: 'Docker', kubernetes: 'Kubernetes', binary: '二进制' }

/**
 * UpgradeGuide 是有新版本时的升级指引：更新内容、有没有数据库变更、按部署方式给出的命令。
 * Tillbox 不会自己升级，命令由管理员在服务器上执行。没有新版本时不显示。
 */
export function UpgradeGuide() {
  const q = useQuery(updateQuery)
  const u = q.data
  if (!u?.newer) return null
  return (
    <Panel
      title={`升级到 ${u.latest}`}
      description={`正在运行 ${u.current}。Tillbox 不会自己升级，照下面的步骤在服务器上操作；升级时服务要重启，收银台会有几秒到几十秒打不开，尽量挑没人付款的时候。`}
      className={enter}
    >
      <div className="divide-y divide-separator">
        <SettingsRow
          label="更新内容"
          description={
            <>
              {u.releases.length > 1 && `${u.releases.length} 个版本，新的在前。`}
              <Link href={u.notes_url} target="_blank" rel="noreferrer">
                完整的更新说明
                <Link.Icon />
              </Link>
            </>
          }
        >
          {u.releases.length ? (
            <ScrollShadow className="max-h-96">
              <div className="flex flex-col gap-4">
                {u.releases.map((r) => (
                  <section key={r.version} className="flex flex-col gap-1.5">
                    <Typography.Heading level={3} className="text-sm">
                      <span className="tb-num">{r.version}</span>
                      {r.date && <span className="tb-num ms-2 font-normal text-muted">{r.date}</span>}
                    </Typography.Heading>
                    <ul className="flex list-disc flex-col gap-1 ps-5 text-sm leading-6 text-muted marker:text-separator">
                      {r.items.map((it, i) => (
                        <li key={i}>
                          <Rich text={it} />
                        </li>
                      ))}
                    </ul>
                  </section>
                ))}
              </div>
            </ScrollShadow>
          ) : (
            <Typography.Paragraph size="sm" color="muted">
              没取到更新内容，点左边的“完整的更新说明”去 GitHub 看。
            </Typography.Paragraph>
          )}
        </SettingsRow>
        <SettingsRow label="数据库变更">
          <Migrations n={u.migrations} />
        </SettingsRow>
        <SettingsRow
          label="升级步骤"
          description={
            u.deploy.mode === 'binary'
              ? '没检测到容器，按二进制部署给出命令；实际用的是别的方式就切换标签。'
              : `检测到运行在 ${modeText[u.deploy.mode]} 里。`
          }
        >
          <Steps u={u} />
        </SettingsRow>
        <SettingsRow label="升级之后">
          <ul className="flex list-disc flex-col gap-1 ps-5 text-sm leading-6 text-muted marker:text-separator">
            <li>
              打开 <Code>{`${window.location.origin}/readyz`}</Code>，返回 <Code>ok</Code> 说明已经启动好。
            </li>
            <li>刷新后台，左下角管理员菜单里的版本号变成 {u.latest}，这条升级指引也会消失。</li>
            <li>到概览页看各渠道的查账状态正常，再下一笔小额测试单确认能收款。</li>
          </ul>
        </SettingsRow>
      </div>
    </Panel>
  )
}

function Migrations({ n }: { n: number }) {
  const [tone, label, text]: [Tone, string, string] =
    n > 0
      ? [
          'warning',
          `有 ${n} 项数据库变更`,
          '新版本第一次启动时自动执行。执行后旧版本不一定还能用这个数据库，要退回旧版本只能恢复升级前的备份，备份之后的新订单和到账要另外核对。',
        ]
      : n === 0
        ? ['success', '没有数据库变更', '新版本出了问题可以直接换回旧版本。']
        : ['default', '没查到', '没取到新版本的迁移列表，按有数据库变更对待：升级前一定要备份。']
  return (
    <div className="flex flex-col items-start gap-2">
      <Pill tone={tone}>{label}</Pill>
      <Typography.Paragraph size="sm" color="muted">
        {text}
      </Typography.Paragraph>
    </div>
  )
}

function Steps({ u }: { u: UpdateView }) {
  const [mode, setMode] = useState<DeployMode>(u.deploy.mode)
  // PostgreSQL 时 tillbox backup 会直接报不支持，备份这一步只留 BackupNow 里的说明，不给命令。
  const pg = useQuery(operationsQuery).data?.backup.supported === false
  const backup = (command: string) => (pg ? undefined : command)
  const image = `${u.image}:${u.latest}`
  const ns = u.deploy.namespace || '<命名空间>'
  const steps: Record<DeployMode, { title: string; text?: ReactNode; command?: string }[]> = {
    docker: [
      { title: '先备份', text: <BackupNow />, command: backup('docker compose exec tillbox tillbox backup') },
      {
        title: '换成新版本',
        text: (
          <>
            把 <Code>compose.yaml</Code> 里的 <Code>image</Code> 改成 <Code>{image}</Code>
            ，再拉取并重建容器。从源码构建的，先
            <Code>git fetch --tags && git checkout v{u.latest}</Code>，再 <Code>docker compose up -d --build</Code>。
          </>
        ),
        command: 'docker compose pull tillbox\ndocker compose up -d tillbox',
      },
      { title: '看启动日志', text: '确认数据库迁移完成、没有报错。', command: 'docker compose logs --tail=50 tillbox' },
    ],
    kubernetes: [
      {
        title: '先备份',
        text: <BackupNow />,
        command: backup(`kubectl -n ${ns} exec deploy/tillbox -- tillbox backup`),
      },
      {
        title: '换成新版本',
        text: (
          <>
            把清单里所有 <Code>{u.image}</Code> 的镜像（包括 <Code>initContainers</Code>）改成 <Code>{image}</Code>
            ，固定了镜像摘要（<Code>@sha256:…</Code>）的，摘要也换成新版本的，再 apply。改清单而不是直接改线上的
            Deployment，免得下次 apply 又换回旧版本。
          </>
        ),
        command: 'kubectl apply -f <清单文件>',
      },
      {
        title: '等新实例就绪',
        text: '确认数据库迁移完成、没有报错。',
        command: `kubectl -n ${ns} rollout status deploy/tillbox\nkubectl -n ${ns} logs deploy/tillbox --tail=50`,
      },
    ],
    binary: [
      {
        title: '先备份',
        text: pg ? (
          <BackupNow />
        ) : (
          <>
            <BackupNow />
            <span className="mt-1 block">在服务器上执行时，环境变量（数据目录、数据库）要和服务一样。</span>
          </>
        ),
        command: backup('./bin/tillbox backup'),
      },
      {
        title: '构建新版本',
        text: '在源码目录里切到新版本的标签再构建，前端依赖里有私有包，要先导出能读它的 GITHUB_TOKEN（见开发文档）。',
        command: `git fetch --tags\ngit checkout v${u.latest}\nmake build VERSION=${u.latest}`,
      },
      { title: '重启服务', text: '用进程管理器重启，例如 systemd：', command: 'sudo systemctl restart tillbox' },
    ],
  }
  return (
    <Tabs variant="secondary" selectedKey={mode} onSelectionChange={(k) => setMode(String(k) as DeployMode)}>
      <Tabs.ListContainer className="-mx-1 overflow-x-auto px-1">
        <Tabs.List aria-label="部署方式" className="w-max min-w-full">
          {(Object.keys(modeText) as DeployMode[]).map((m) => (
            <Tabs.Tab key={m} id={m} className="whitespace-nowrap">
              {modeText[m]}
              <Tabs.Indicator />
            </Tabs.Tab>
          ))}
        </Tabs.List>
      </Tabs.ListContainer>
      <Tabs.Panel id={mode} className="pt-4 outline-none">
        {/* 只是一张步骤表，不跟踪进度（currentStep=-1）；未开始的步骤默认变淡，这里照常显示 */}
        <Stepper aria-label={`${modeText[mode]} 的升级步骤`} orientation="vertical" currentStep={-1}>
          {steps[mode].map((s) => (
            <Stepper.Step key={s.title}>
              <Stepper.Indicator className="tb-num" />
              <Stepper.Content className="min-w-0 flex-1">
                <Stepper.Title className="text-sm leading-7 text-foreground">{s.title}</Stepper.Title>
                <Stepper.Description className="mt-1 flex min-w-0 flex-col gap-2 self-stretch text-sm leading-6 opacity-100">
                  {s.text && <span>{s.text}</span>}
                  {s.command && <Command text={s.command} />}
                </Stepper.Description>
              </Stepper.Content>
              <Stepper.Separator />
            </Stepper.Step>
          ))}
        </Stepper>
      </Tabs.Panel>
    </Tabs>
  )
}

/** BackupNow 是升级前的备份：SQLite 可以直接在后台点“立即备份”，PostgreSQL 提示用数据库自己的工具。 */
function BackupNow() {
  const qc = useQueryClient()
  const q = useQuery(operationsQuery)
  const run = useMutation({
    mutationFn: () => api.post('/web-api/admin/operations/backup'),
    onSuccess: () => toastOK('已备份'),
    onError: toastError,
    onSettled: () => qc.invalidateQueries({ queryKey: ['operations'] }),
  })
  const b = q.data?.backup
  if (!b) return null
  if (!b.supported) {
    return (
      <>
        数据库是 PostgreSQL，用 <Code>pg_dump</Code> 或云数据库自带的备份先备份一份，主密钥照常另外保存。
      </>
    )
  }
  return (
    <span className="flex flex-col items-start gap-2">
      <span>
        新版本启动时会先改数据库，所以要在正在运行的这个版本里备份：点下面的按钮，或在服务器上执行命令。
        {b.state.last_ok_at > 0 && (
          <>
            上次备份 <span className="tb-num">{time(b.state.last_ok_at)}</span>。
          </>
        )}
      </span>
      <Button size="sm" variant="secondary" isPending={run.isPending} onPress={() => run.mutate()}>
        立即备份
      </Button>
    </span>
  )
}

/** Command 是一段可以整段复制的命令，太长时在框里横向滚动。 */
function Command({ text }: { text: string }) {
  return (
    <Surface variant="secondary" className="relative min-w-0 rounded-xl py-2 ps-3 pe-11">
      <pre className="tb-mono overflow-x-auto py-0.5 text-xs leading-6 text-foreground">{text}</pre>
      <Button
        isIconOnly
        size="sm"
        variant="ghost"
        aria-label="复制命令"
        className="absolute end-1.5 top-1.5 size-7 min-w-7 text-muted"
        onPress={() => void copyText(text)}
      >
        <Copy className="size-3.5" />
      </Button>
    </Surface>
  )
}

/** Rich 把更新日志里反引号括起来的部分显示成代码。 */
function Rich({ text }: { text: string }) {
  return text.split('`').map((part, i) => (i % 2 ? <Code key={i}>{part}</Code> : part))
}
