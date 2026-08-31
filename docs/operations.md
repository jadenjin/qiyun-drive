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

PowerShell 中运行：

```powershell
.\scripts\backup.ps1
```

脚本会短暂停止 API 与 Worker，分别生成 PostgreSQL 自定义格式转储、RustFS 数据卷压缩包和带 SHA-256 的清单，然后自动恢复服务。Web 页面在这段时间仍可打开，但写操作会暂时不可用。

建议至少保留三套异地备份，并定期在独立的 Compose 项目名和新数据卷中演练恢复。不要直接覆盖唯一一套生产数据；先恢复到隔离环境，核对用户、目录、对象数量和抽样文件哈希，再切换入口。

恢复顺序：先在新 PostgreSQL 实例中使用 `pg_restore --clean --if-exists --no-owner` 导入 `postgres.dump`，再把 `rustfs-data.tar.gz` 解压到全新的 RustFS 数据卷，最后启动 API 让迁移补齐并检查 readiness。数据库和对象存储必须来自同一份备份目录。

使用 `compose.device.yaml` 时，`backup.ps1` 不会备份 `/srv/qiyun-drive-rustfs`（其后端镜像为 `/mnt/data/.qiyun-drive-storage.img`）。应在暂停 API 与 Worker 写入后，对该文件系统制作快照，或使用 S3 兼容工具把栖云桶同步到另一块物理介质；不要把同一块机械盘上的另一个目录当成唯一备份。

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
