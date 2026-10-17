# 支付测试、巡检与归档

## AI 自动处理与付款人申诉

后台“AI 与付款申诉”可以配置模型、启用自动查账（上游渠道的订单改为向上游查单）、补发通知及证据核验后的到账补认（只限经营码渠道）；付款人通过收银台链接或 `/help` 提交问题。配置、自动执行条件、查询凭证与数据范围见 [ai-support.md](ai-support.md)。巡检监测 AI 运行失败、停滞及待人工项。

## 后台支付测试

打开 `/admin/payment-test`，选择已启用商户、金额和 V1/MD5 或 V2/RSA，创建订单后打开收银台。测试单走所选商户的支付渠道，页面会显示该渠道能不能收款。经营码和上游渠道都需要真实付款（上游渠道按原价付款，在收银台选支付方式）；页面依次显示建单、确认付款和通知验签结果，每 3 秒刷新。未支付测试单可以关闭。

测试单共用所属渠道的正常金额池、查账或查单流程，`is_test=true`，不计入概览营业统计、商户易支付统计或订单列表 API。它只向本站固定测试接收端发送通知，不会给业务站充值；订单列表和 CSV 带测试标识。接收端只能确认已经由流水匹配或上游确认到账的测试单，不能标记订单已付。重复有效通知只增加接收次数。测试单并不能替代业务站实际充值验收。

检查渠道本身的连接用“支付渠道”页的“测试连接”：经营码渠道查一次账务明细，上游渠道调上游的商户信息接口，都不下单。模拟付款接口只在演示模式注册。

“支付渠道”页的“运行情况”一列显示每个渠道的状态：经营码渠道是查账进度和错误；上游渠道是上次查单、预下单错误、上次回调和不合格的回调。

## 宿主机巡检

`scripts/tillbox-monitor.py` 适用于 Kubernetes（例如 k3s）上用 SQLite 的部署：在宿主机上由 systemd 定时器每 5 分钟执行一次，用 kubectl 进容器只读查询数据库，再用 Telegram 发提醒。新异常提醒一次，持续异常每小时提醒，恢复提醒一次，发送失败不推进去重状态。

| 检查 | 条件 |
| --- | --- |
| 主窗口 / 补扫 | 任一启用的经营码渠道连续失败至少 3 次 |
| 查账停摆 | 任一启用的经营码渠道距最后成功超过 `max(5 分钟, 3 × 常规查账间隔)` |
| 通知 | 存在死信 |
| 归档 | 执行失败，或启用后 48 小时无成功执行 |
| 备份 | CronJob 暂停、26 小时无成功执行，或最近失败尚未恢复 |
| 磁盘 | 数据所在文件系统可用不足 1 GiB 或 10% |
| 实例与入口 | 无可用实例，或 HTTPS `/readyz` 检查失败 |

`/healthz` 是进程存活检查；`/readyz` 以两秒超时检查数据库。查账故障只告警，避免通过重启反复打断退避。后台“运维与归档”按渠道显示查账问题（只看启用的经营码渠道）、上游预下单和查单连续失败、最近 24 小时不合格的上游回调，以及通知和归档状态；宿主机检查同时覆盖备份、磁盘与公网入口。上游渠道的状态只在进程内存里，宿主机巡检看不到，要看后台。

### 安装

1. 把 `scripts/tillbox-monitor.py` 复制到 `/usr/local/lib/tillbox-monitor/`，把 `scripts/tillbox-monitor.service` 和 `tillbox-monitor.timer` 复制到 `/etc/systemd/system/`。
2. 新建 `/etc/tillbox-monitor.env`，权限设成 600，按下表填写。值里有空格时用单引号括起来，systemd 和 shell 都能读。
3. 运行 `systemctl daemon-reload` 和 `systemctl enable --now tillbox-monitor.timer`。

| 变量 | 说明 | 默认值 |
| --- | --- | --- |
| `TILLBOX_MONITOR_URL` | 公网就绪检查地址，例如 `https://pay.example.com/readyz` | 必填 |
| `TILLBOX_MONITOR_KUBECTL` | kubectl 命令 | `'k3s kubectl'` |
| `TILLBOX_MONITOR_NAMESPACE` | 命名空间 | `tillbox` |
| `TILLBOX_MONITOR_DEPLOYMENT` | Deployment 名称 | `tillbox` |
| `TILLBOX_MONITOR_DB` | 容器里的 SQLite 路径 | `/data/tillbox.db` |
| `TILLBOX_MONITOR_BACKUP_CRONJOB` | 备份 CronJob 名称；设成空值就不检查备份 | `tillbox-backup` |
| `TILLBOX_MONITOR_TELEGRAM_SECRET` | 存提醒目标的 Secret，写成 `命名空间/名称`；Secret 里的键是 `telegram-token` 和 `telegram-chat-ids`（JSON 数组） | 无 |
| `TILLBOX_MONITOR_TELEGRAM_TOKEN`、`TILLBOX_MONITOR_TELEGRAM_CHAT_IDS` | 不用 Secret 时直接写机器人令牌和会话 ID（多个用逗号分隔） | 无 |

脚本放在别的路径时，不要改仓库里的 service 文件：用 `systemctl edit tillbox-monitor.service` 写一个覆盖文件，先写一行空的 `ExecStart=` 清掉原值，再写新的 `ExecStart=`。

手动检查：

```sh
set -a; . /etc/tillbox-monitor.env; set +a
python3 /usr/local/lib/tillbox-monitor/tillbox-monitor.py --dry-run
systemctl list-timers tillbox-monitor.timer
journalctl -u tillbox-monitor.service
```

dry run 只读、不发送通知、不更新去重状态。机器整体断电仍需机器外监控。

## 记录保留和恢复

后台 `/admin/operations` 默认每日自动检查一次：流水原始响应和通知尝试在线保留 90 天，审计日志 365 天，可修改或暂停。每次最多处理 15,000 条，后续运行继续。所有订单、金额、流水编号、指纹、关联、通知最终状态及最后一次通知尝试长期保留。未完成通知、争议订单及存在未解决异常的流水证据不清理。

过期记录先压缩为 JSONL.GZ，再用配套主密钥加密存入 `/data/archives/*.jsonl.gz.enc`，文件权限 600、目录 700；文件与目录同步落盘后，才在同一数据库事务内登记 SHA256 清单并清理在线副本。文件写入或事务失败时保留在线记录；事务回滚可能留下没有清单的加密副本，保留待核查即可。归档文件不自动删除，磁盘巡检负责容量提醒。

备份要包含数据库、`master.key` 和整个 `archives/`。恢复时三者配套恢复，先校验备份包内 SHA256，再检查数据库。不要只恢复数据库或只保留最新数据库而删除归档目录。

后台可按批次下载：服务先检查密文 SHA256，解密后返回 `.jsonl.gz`。每行含 `version:1`、`table`、`id`、`data`；表名为 `transactions.raw`、`notify_attempts` 或 `audit_logs`。可以解压检索历史证据。需要重新导回在线库时，先停止服务并备份：`transactions.raw` 按 ID 恢复 `raw`，另两表按原 ID 导入完整 `data`（已有同 ID 记录不得覆盖）；在事务内核对条数，恢复后保留原归档与清单。通知历史恢复不改变订单或通知队列状态，不会触发重新充值。

## 版本与升级

`VERSION` 定义版本，`tillbox version` 同时显示提交 SHA；镜像 OCI 标签包含版本与提交。`make image IMAGE=tillbox:0.1.0` 仅构建 linux/amd64。发版用 `make release VERSION=x.y.z`；CI 和本机 `make ci` 的检查内容、版本标签触发的镜像归档（不自动公开发布）和完整步骤见 [release.md](release.md)。

升级前执行一次备份并确认上传校验成功；再部署新镜像；Kubernetes 下更新 Deployment，保持单实例 Recreate。启动时自动执行 goose 增量迁移。已发布的 `00001_init.sql` 冻结，后续变更新增迁移；本次 `00002_operations.sql` 为现有订单补 `is_test=false` 并增加接收与归档表。升级前快照可在停机后恢复配套旧镜像；回滚前先处理升级后新增订单与通知，不能让两套实例同时查同一支付宝账户。
