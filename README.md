# 栖云家庭网盘

栖云是一个 Go + React + PostgreSQL + MinIO 实现的自托管家庭网盘。每位成员拥有管理员也无法浏览的私有空间，同时可以在家庭空间内按目录授予查看、编辑或管理权限。

## 已实现能力

- 用户名密码登录、首次初始化、一次性成员邀请和管理员密码重置
- 私有空间与家庭空间隔离，家庭目录权限继承及中断继承
- 文件夹、文件上传、下载、移动、重命名、批量操作、回收站和空间配额
- 浏览器直传 MinIO，小文件预签名 PUT，大文件 Multipart 分片上传
- 文件夹上传层级保留、批量同名保留两份、上传进度和失败重试状态
- JPEG、PNG、WebP、GIF、HEIC/HEIF 照片时间线、EXIF 和两档 WebP 缩略图
- 相册详情、按拍摄时间排列的照片时间线、照片备注、密码分享、有效期、撤销和审计记录
- 个人资料与密码修改、成员角色调整、成员配额和文件/相册权限编辑
- 流式 ZIP 文件夹下载，不在 API 容器落盘
- 响应式桌面/手机界面

## 快速启动

1. 复制 `.env.example` 为 `.env`，至少修改 PostgreSQL 与 MinIO 密码。
2. 在项目目录执行：

   ```bash
   docker compose up --build -d
   ```

3. 打开 `http://localhost`，首次访问会引导创建家庭与所有者账号。

MinIO API 默认位于 `http://localhost:9000`，管理控制台位于 `http://localhost:9001`。不要将管理控制台直接暴露到公网。

## 公网部署

- 将 `PUBLIC_BASE_URL` 改为实际 HTTPS 地址，并设置 `COOKIE_SECURE=true`。
- 给 Web/API 域名及 `S3_PUBLIC_ENDPOINT` 对应的对象存储域名配置 TLS。
- MinIO CORS 仅允许 `PUBLIC_BASE_URL`；不要开放公共桶权限。
- 在升级前固定并验证 MinIO 镜像版本，不要长期依赖 `latest`。

预签名 URL 中的主机名必须能被用户浏览器访问，所以 `S3_ENDPOINT` 使用容器内地址，而 `S3_PUBLIC_ENDPOINT` 使用浏览器可访问的地址。

## 本地开发

前端：

```bash
npm install
npm run dev
```

后端需要 Go 1.23 或更高版本，以及可访问的 PostgreSQL 和 MinIO：

```bash
cd backend
go run ./cmd/api
go run ./cmd/worker
```

主要配置参见 `backend/internal/config/config.go`。数据库迁移会由 API 在启动时自动执行。

项目已为国内网络默认启用 npm 镜像（`.npmrc`）和 Go 模块代理（本地 `GOPROXY` 及后端镜像构建参数），重新安装依赖或构建容器时会自动使用；如需改回官方源，可覆盖对应的 registry 或 `GOPROXY`。

## 数据与备份

需要同时备份 PostgreSQL 与 MinIO 数据：

- PostgreSQL 保存账号、权限、目录、相册、分享和对象元数据。
- MinIO 保存原始文件和派生缩略图。
- 回收站内容仍计入配额，默认 30 天后由 Worker 永久清理。

恢复时应先恢复 PostgreSQL，再恢复对应时间点的 MinIO 数据。建议在备份期间暂停上传，避免元数据和对象时间点不一致。

## 安全边界

- MinIO 桶始终私有，只有 API/Worker 持有长期密钥。
- 文件访问必须先通过空间与 ACL 权限判断，再签发短期 URL。
- 管理员权限只覆盖成员管理和家庭空间，不覆盖成员私有空间。
- 密码使用 Argon2id，登录会话和分享令牌只保存 SHA-256 哈希。
- 未授权资源返回 404，减少资源 ID 探测。

当前 v1 不包含原生同步客户端、文件版本、视频转码、AI 人脸识别、Office/PDF 在线预览和端到端加密。
