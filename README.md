# 栖云家庭网盘

栖云是一个 Go + React + PostgreSQL + MinIO 实现的自托管家庭网盘。每位成员拥有管理员也无法浏览的私有空间，同时可以在家庭空间内按目录授予查看、编辑或管理权限。

## 已实现能力

- 用户名密码登录、首次初始化、一次性成员邀请、管理员密码重置和登录设备管理
- 私有空间与家庭空间隔离，家庭目录权限继承及中断继承
- 文件夹、文件上传、下载、移动、重命名、批量操作、回收站和空间配额
- 浏览器直传 MinIO，小文件预签名 PUT，大文件 Multipart 分片上传
- 文件夹上传层级保留、批量同名保留两份、上传进度和失败重试状态
- JPEG、PNG、WebP、GIF、HEIC/HEIF 照片时间线、EXIF 和两档 WebP 缩略图
- 相册详情、按拍摄时间排列的照片时间线、照片备注、密码分享、有效期、撤销和审计记录
- 图片、音视频、文本及 PDF 在线预览，未知格式安全回退到下载
- 个人资料与密码修改、成员角色调整、家庭空间配额和文件/相册权限编辑
- 流式 ZIP 文件夹下载，不在 API 容器落盘
- 响应式桌面/手机界面

## 快速启动

1. 复制 `.env.example` 为 `.env`，替换 PostgreSQL 与 MinIO 的示例密码。
2. 在项目目录执行：

   ```bash
   docker compose up --build -d
   ```

3. 打开 `http://localhost`，首次访问会引导创建家庭与所有者账号。

PostgreSQL、MinIO API 和管理控制台默认仅绑定到 `127.0.0.1`，不会暴露到局域网。MinIO API 位于 `http://localhost:9000`，管理控制台位于 `http://localhost:9001`。

基础 compose 固定旧的官方 MinIO 容器以获得可复现的本机开发环境。它不应直接用于公网生产环境。

## 公网部署

- 将 `PUBLIC_BASE_URL` 改为实际 HTTPS 地址，`SITE_ADDRESS` 改为域名，并设置 `COOKIE_SECURE=true`。
- 给 Web/API 域名及 `S3_PUBLIC_ENDPOINT` 对应的对象存储域名配置 TLS。
- MinIO CORS 仅允许 `PUBLIC_BASE_URL`；不要开放公共桶权限。
- 使用生产覆盖文件从 MinIO 官方安全修复版本源码构建对象存储：

  ```bash
  docker compose -f compose.yaml -f compose.production.yaml up --build -d
  ```

- 升级 `MINIO_SOURCE_VERSION` 前先在隔离环境完成备份恢复和上传下载回归。

预签名 URL 中的主机名必须能被用户浏览器访问，所以 `S3_ENDPOINT` 使用容器内地址，而 `S3_PUBLIC_ENDPOINT` 使用浏览器可访问的地址。

## 本地开发

前端：

```bash
npm install
npm run dev
```

后端需要 Go 1.25 或更高版本，以及可访问的 PostgreSQL 和 MinIO：

```bash
cd backend
go run ./cmd/api
go run ./cmd/worker
```

主要配置参见 `backend/internal/config/config.go`。数据库迁移会由 API 在启动时自动执行；无效布尔值、时长、URL、安全 Cookie 或占位密钥会让进程明确拒绝启动。

项目已为国内网络默认启用 npm 镜像（`.npmrc`）和 Go 模块代理（本地 `GOPROXY` 及后端镜像构建参数），重新安装依赖或构建容器时会自动使用；如需改回官方源，可覆盖对应的 registry 或 `GOPROXY`。

## 数据与备份

需要同时备份 PostgreSQL 与 MinIO 数据：

- PostgreSQL 保存账号、权限、目录、相册、分享和对象元数据。
- MinIO 保存原始文件和派生缩略图。
- 回收站内容仍计入配额，默认 30 天后由 Worker 永久清理。

Windows/PowerShell 可执行 `.\scripts\backup.ps1` 生成一致性备份和 SHA-256 清单。恢复时应先恢复 PostgreSQL，再恢复同一备份目录中的 MinIO 数据，并先在隔离环境验证。

完整的发布检查、备份恢复、监控和故障处理见 [运维手册](docs/operations.md)。

## 质量检查

前端完整检查：

```bash
npm run check
```

后端完整检查：

```bash
cd backend
go test ./...
go vet ./...
```

GitHub Actions 会额外启动一次性 PostgreSQL 与 MinIO，运行真实 API 权限、会话、分享和批量列表集成测试。

## 安全边界

- MinIO 桶始终私有，只有 API/Worker 持有长期密钥。
- 文件访问必须先通过空间与 ACL 权限判断，再签发短期 URL。
- 管理员权限只覆盖成员管理和家庭空间，不覆盖成员私有空间。
- 密码使用 Argon2id，登录会话和分享令牌只保存 SHA-256 哈希。
- 未授权资源返回 404，减少资源 ID 探测。

当前 v1 不包含原生同步客户端、文件版本、视频转码、AI 人脸识别、Office 在线编辑和端到端加密。
