# 栖云运维手册

## 发布前检查

1. 使用长随机值替换 `.env` 中的数据库和 MinIO 密码。
2. 公网部署时设置 `PUBLIC_BASE_URL=https://你的域名`、`SITE_ADDRESS=你的域名`、`COOKIE_SECURE=true`。
3. `S3_PUBLIC_ENDPOINT` 必须是浏览器可访问的 HTTPS 对象存储地址；API 使用的 `S3_ENDPOINT` 仍保持容器内地址。
4. 执行 `docker compose -f compose.yaml -f compose.production.yaml config --quiet`，确认环境变量完整。
5. 执行 `docker compose -f compose.yaml -f compose.production.yaml up --build -d`，等待 `api`、`web`、`postgres` 变为健康。
6. 检查 `/health/live` 与 `/health/ready`；前者只代表进程存活，后者还会验证 PostgreSQL 和 MinIO。

## MinIO 版本策略

MinIO 社区版已转为仅提供源代码，旧的官方容器镜像不再持续收到修复。基础 `compose.yaml` 固定旧镜像，仅用于本机开发；生产环境必须叠加 `compose.production.yaml`，它会从官方安全修复版本源码构建二进制并替换旧镜像中的运行文件。

升级 `MINIO_SOURCE_VERSION` 前，应先在测试环境恢复最近一次备份并完成上传、下载、分片续传、相册缩略图和分享下载回归。不要直接改动 MinIO 数据卷内的对象或元数据文件。

## 一致性备份

PowerShell 中运行：

```powershell
.\scripts\backup.ps1
```

脚本会短暂停止 API 与 Worker，分别生成 PostgreSQL 自定义格式转储、MinIO 数据卷压缩包和带 SHA-256 的清单，然后自动恢复服务。Web 页面在这段时间仍可打开，但写操作会暂时不可用。

建议至少保留三套异地备份，并定期在独立的 Compose 项目名和新数据卷中演练恢复。不要直接覆盖唯一一套生产数据；先恢复到隔离环境，核对用户、目录、对象数量和抽样文件哈希，再切换入口。

恢复顺序：先在新 PostgreSQL 实例中使用 `pg_restore --clean --if-exists --no-owner` 导入 `postgres.dump`，再把 `minio-data.tar.gz` 解压到全新的 MinIO 数据卷，最后启动 API 让迁移补齐并检查 readiness。数据库和对象存储必须来自同一份备份目录。

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
- 浏览器无法直传：`S3_PUBLIC_ENDPOINT` 的主机名、证书和 MinIO CORS 必须能从浏览器访问。
- readiness 失败：分别检查 PostgreSQL、MinIO 健康端点和 API 日志，避免只重启前端掩盖依赖故障。
