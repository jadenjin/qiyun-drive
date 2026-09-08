# 栖云运维手册

## 发布前检查

1. 使用长随机值替换 `.env` 中的数据库和 RustFS 密码。
2. 公网部署时设置 `PUBLIC_BASE_URL=https://你的域名`、`SITE_ADDRESS=你的域名`、`COOKIE_SECURE=true`。
3. `S3_PUBLIC_ENDPOINT` 必须是浏览器可访问的 HTTPS 对象存储地址；API 使用的 `S3_ENDPOINT` 仍保持容器内地址。
4. 执行 `docker compose -f compose.yaml -f compose.production.yaml config --quiet`，确认环境变量完整。
5. 执行 `docker compose -f compose.yaml -f compose.production.yaml up --build -d`，等待 `api`、`web`、`postgres` 变为健康。
6. 检查 `/health/live` 与 `/health/ready`；前者只代表进程存活，后者还会验证 PostgreSQL 和 RustFS。

## RustFS 版本策略

基础 `compose.yaml` 与 `compose.production.yaml` 都固定经过验证的 RustFS 发布版本，避免 `latest` 在无人值守重启时改变存储实现。升级版本前先阅读官方发布说明。

升级 `RUSTFS_VERSION` 前，应先在测试环境恢复最近一次备份并完成上传、下载、分片续传、相册缩略图和分享下载回归。不要直接改动 RustFS 数据卷内的对象或元数据文件。

## 一致性备份

设备上的原生脚本为 `scripts/backup.sh`。它保存六个服务的镜像、配置、数据库和对象数据，校验完成后更新最新备份指针，并保留最近 7 份完整快照（同时保护 latest 指向的快照）。`deploy/` 提供每日 03:30 的 `qiyun-backup.timer`、每周日 06:00 的 `qiyun-restore.timer`（均带随机延迟），以及每分钟采集磁盘和容器状态的 `qiyun-health.timer`。确认服务文件中的安装路径后，将六个 service/timer 文件安装到 `/etc/systemd/system/`，执行 `systemctl daemon-reload` 和 `systemctl enable --now qiyun-backup.timer qiyun-restore.timer qiyun-health.timer`。提交代码本身不会启用这些任务。

恢复演练通过 `scripts/restore-latest.sh` 获取备份锁，在 `/var/lib/qiyun-drive-restore` 建立独立容器和网络，仅绑定回环测试端口；从备份恢复六个服务并抽样比较文件 SHA-256，结束后清理测试资源。系统盘必须与生产对象目录使用不同文件系统。缺少历史文件摘要的旧备份不能通过完整哈希验收。

Windows 使用 `scripts/install-offhost-backup.ps1` 安装 `Qiyun off-host backup` 任务，每日 05:30 和当前用户登录时运行。安装时提供 Python、包含 Paramiko 的依赖目录、副本目标目录和已信任的 SSH 主机密钥文件；初次配置从 `QIYUN_SSH_PASSWORD` 进程环境变量读取密码。当前采集脚本适配 `192.168.1.19` 的 root 账户及 `/opt/qiyun-drive`，其他设备需先修改 `scripts/collect-backup.py` 的连接和远端路径。任务验证全部清单文件并保留最近 14 份。凭据以当前 Windows 账户 DPAPI 加密存于 `%LOCALAPPDATA%\QiyunBackup\credential.xml`，设置与副本目录 ACL 仅允许当前用户；仓库没有保存明文密码。电脑必须开机且该用户已登录，离线期间不能产生新异机副本。项目或 Python 路径移动后需重新执行安装脚本更新任务配置。

每次部署后应检查任务退出码与 `ops/` 中的最新状态，并实际运行恢复演练。恢复环境仅为 Caddy 增加网关网络并绑定回环端口，其他服务保持内部网络。具体备份编号、文件数量与设备验收结果保留在本地记录中。

PowerShell 中运行：

```powershell
.\scripts\backup.ps1
```

脚本会短暂停止入口、API、Worker 和 RustFS（已签发的直传 URL 也需要停止写入），分别生成 PostgreSQL 自定义格式转储、RustFS 实际数据挂载压缩包和带 SHA-256 的清单，然后自动恢复服务。备份期间网盘入口暂时不可用。

建议至少保留三套异地备份，并定期在独立的 Compose 项目名和新数据卷中演练恢复。不要直接覆盖唯一一套生产数据；先恢复到隔离环境，核对用户、目录、对象数量和抽样文件哈希，再切换入口。

恢复顺序：先在新 PostgreSQL 实例中使用 `pg_restore --clean --if-exists --no-owner` 导入 `postgres.dump`，再把 `rustfs-data.tar.gz` 解压到全新的 RustFS 数据卷，最后启动 API 让迁移补齐并检查 readiness。数据库和对象存储必须来自同一份备份目录。

`backup.ps1` 从实际 RustFS 容器继承只读数据挂载，因此也支持设备 bind mount。脚本必须连接正确的 Docker 主机，备份目标路径必须能被该 Docker 主机挂载；不要把 Windows 本地路径直接传给 Linux 远程 Docker。该脚本生成 v1 数据备份；需要包含配置、镜像并供完整恢复演练使用的 v2 快照时，应在设备运行 `backup.sh`。同一设备上的备份不能替代异地备份。

## 日常观察

- `docker compose ps`：服务与健康状态。
- `docker compose logs --since 30m api worker`：请求错误、迁移、后台任务重试。
- `/health/ready`：数据库和对象存储是否可用。
- 家庭管理页“最近活动”：分享、权限、删除和密码重置操作。
- Worker 连续失败 6 次的任务不会无限重试，应检查 `jobs.last_error` 后处理依赖问题。

## 故障处理

- 上传失败但空间仍被占用：等待 Worker 的过期上传清理；确认 Worker 正在运行。
- 图片无缩略图：确认容器内 `vips` 与 `exiftool` 可用，并查看 `index_photo` 任务错误。
- 登录循环：核对公网 URL、允许来源与 `COOKIE_SECURE`；HTTPS 地址必须使用安全 Cookie。
- 浏览器无法直传：`S3_PUBLIC_ENDPOINT` 的主机名、证书和 RustFS 桶 CORS 必须能从浏览器访问。
- readiness 失败：分别检查 PostgreSQL、RustFS 健康端点和 API 日志，避免只重启前端掩盖依赖故障。

## 单端口入口与 FRP

内网设备示例：`PUBLIC_BASE_URL=http://192.168.1.19`、`SITE_ADDRESS=http://192.168.1.19`、`S3_PUBLIC_ENDPOINT=http://192.168.1.19`，对象端口仅监听 `127.0.0.1`。Caddy 保留原始 Host 供 S3 签名校验，外部路径为 `/pan-objects/...`；对象响应强制沙箱，拒绝 DELETE/POST 等无需公开的方法。

以后切换地址必须同时更新上述三个值，再重建容器使环境变量生效。只编辑 `.env` 或重载 Caddyfile 不会修改容器环境。可运行 `python scripts/security-probe.py http://192.168.1.19` 检查入口，健康检查会验证 JSON 内容，防止把不匹配 Host 的空白 200 当作正常服务。

公网入口需单独配置并验证。公网使用 HTTPS 时设置 `COOKIE_SECURE=true`；现有校验拒绝公网 HTTP。
