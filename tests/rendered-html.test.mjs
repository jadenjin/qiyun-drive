import assert from "node:assert/strict";
import { access, readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../", import.meta.url);

async function render(path = "/") {
  const workerUrl = new URL("../dist/server/index.js", import.meta.url);
  workerUrl.searchParams.set("test", `${process.pid}-${Date.now()}`);
  const { default: worker } = await import(workerUrl.href);
  return worker.fetch(
    new Request(`http://localhost${path}`, { headers: { accept: "text/html" } }),
    { ASSETS: { fetch: async () => new Response("Not found", { status: 404 }) } },
    { waitUntil() {}, passThroughOnException() {} },
  );
}

test("server renders the Qiyun application shell and product metadata", async () => {
  const response = await render();
  assert.equal(response.status, 200);
  assert.match(response.headers.get("content-type") ?? "", /^text\/html\b/i);
  const html = await response.text();
  assert.match(html, /<title>栖云 · 家庭私人云盘<\/title>/);
  assert.match(html, /正在打开你的空间/);
  assert.match(html, /og\.png/);
  assert.doesNotMatch(html, /codex-preview|Your site is taking shape|react-loading-skeleton/i);
});

test("starter preview is removed and production assets are present", async () => {
  await assert.rejects(access(new URL("app/_sites-preview", root)));
  await access(new URL("public/og.png", root));
  const [page, layout, packageJson] = await Promise.all([
    readFile(new URL("app/page.tsx", root), "utf8"),
    readFile(new URL("app/layout.tsx", root), "utf8"),
    readFile(new URL("package.json", root), "utf8"),
  ]);
  assert.match(page, /<CloudDrive \/>/);
  assert.match(layout, /栖云 · 家庭私人云盘/);
  assert.doesNotMatch(packageJson, /react-loading-skeleton/);
});

test("README documents core features and includes sanitized product screenshots", async () => {
  const readme = await readFile(new URL("README.md", root), "utf8");
  for (const image of ["files.png", "albums.png", "shares.png"]) {
    await access(new URL(`docs/screenshots/${image}`, root));
    assert.match(readme, new RegExp(`docs/screenshots/${image.replace(".", "\\.")}`));
  }
  assert.match(readme, /## 功能介绍/);
  assert.match(readme, /### 文件与上传/);
  assert.match(readme, /### 照片与相册/);
  assert.match(readme, /### 分享、权限与账户/);
});

test("invite, reset, and public-share routes render", async () => {
  for (const path of ["/invite/test-token", "/reset-password/test-token", "/s/test-token"]) {
    const response = await render(path);
    assert.equal(response.status, 200, path);
  }
});

test("drive UI uses real trash/share data and exposes folder and album uploads", async () => {
  const [drive, uploader] = await Promise.all([
    readFile(new URL("app/cloud-drive.tsx", root), "utf8"),
    readFile(new URL("app/upload-client.ts", root), "utf8"),
  ]);
  assert.match(drive, /request<\{ items: TrashItem\[\] \}>\("\/trash"\)/);
  assert.match(drive, /activeShareCount > 0/);
  assert.doesNotMatch(drive, /className="nav-count">3</);
  assert.match(drive, /上传文件夹/);
  assert.match(drive, /上传照片/);
  assert.match(drive, /`\/albums\/\$\{albumId\}\/items`/);
  assert.match(uploader, /return session\.nodeId/);
  assert.match(uploader, /albumId\?: string/);
  assert.match(uploader, /albumId: options\.albumId/);
  assert.match(uploader, /section: options\.section \|\| "files"/);
  assert.match(drive, /section === "photos" \? null : currentParent/);
  assert.match(drive, /view === "photos"[\s\S]{0,300}aria-label="上传照片"/);
  assert.match(drive, /resumable\.albumId/);
  assert.match(uploader, /ownerUserId: string/);
  assert.match(drive, /loadResumableUploads\(currentUser\.id\)/);
  assert.match(drive, /await clearAllResumeState\(\)/);
});

test("drive UI exposes complete album, file-management, permission, and account flows", async () => {
  const [drive, routes, nodes, acl, auth, worker, permissions, photos, publicFlows, styles] = await Promise.all([
    readFile(new URL("app/cloud-drive.tsx", root), "utf8"),
    readFile(new URL("backend/internal/httpapi/server.go", root), "utf8"),
    readFile(new URL("backend/internal/httpapi/nodes.go", root), "utf8"),
    readFile(new URL("backend/internal/httpapi/acl.go", root), "utf8"),
    readFile(new URL("backend/internal/httpapi/auth_handlers.go", root), "utf8"),
    readFile(new URL("backend/cmd/worker/main.go", root), "utf8"),
    readFile(new URL("backend/internal/httpapi/permissions.go", root), "utf8"),
    readFile(new URL("backend/internal/httpapi/photos.go", root), "utf8"),
    readFile(new URL("app/token-flows.tsx", root), "utf8"),
    readFile(new URL("app/globals.css", root), "utf8"),
  ]);
  assert.match(drive, /function AlbumDetail/);
  assert.match(drive, /className="album-card-open"[\s\S]{0,120}onClick=\{\(\) => onOpen\(album\)\}/);
  assert.match(drive, /className="notification-wrap sidebar-notification"/);
  assert.doesNotMatch(drive, /search-box|searchInput|Ctrl K/);
  assert.match(drive, /function PhotoViewer/);
  assert.match(drive, /savePhotoRemark/);
  assert.match(drive, /function MoveDialog/);
  assert.match(drive, /function PermissionDialog/);
  assert.match(drive, /function AccountDialog/);
  assert.match(drive, /function ActionLinkDialog/);
  assert.match(drive, /function FilePreviewDialog/);
  assert.match(drive, /sandbox="" referrerPolicy="no-referrer"/);
  assert.match(drive, /function ShareCreatedDialog/);
  assert.match(drive, /canInviteAdmin/);
  assert.match(drive, /登录设备/);
  assert.match(drive, /访问密码至少需要 4 个字符/);
  assert.match(drive, /creatorName/);
  assert.match(drive, /refreshController\.current\?\.abort\(\)/);
  assert.match(drive, /role="status" aria-live="polite"/);
  assert.match(drive, /loading="lazy" decoding="async"/);
  assert.match(drive, /文件排序方式/);
  assert.match(drive, /api<\{ items: Member\[\] \}>\("\/members"\)/);
  assert.match(drive, /最近活动/);
  assert.match(drive, /有效分享/);
  assert.match(drive, /历史记录/);
  assert.match(routes, /Get\("\/folders\/tree", s\.listFolderTree\)/);
  assert.match(routes, /Post\("\/nodes\/\{id\}\/move", s\.write\(\(\*Server\)\.moveNode\)\)/);
  assert.match(routes, /Get\("\/nodes\/\{id\}\/permissions", s\.getNodePermissions\)/);
  assert.match(routes, /Post\("\/me\/password", s\.write\(\(\*Server\)\.changePassword\)\)/);
  assert.match(routes, /Get\("\/me\/sessions", s\.listSessions\)/);
  assert.match(routes, /Delete\("\/me\/sessions\/\{id\}", s\.write\(\(\*Server\)\.revokeSession\)\)/);
  assert.match(nodes, /func \(s \*Server\) moveNode/);
  assert.match(acl, /func \(s \*Server\) getNodePermissions/);
  assert.match(auth, /func \(s \*Server\) updateMemberRole/);
  assert.match(auth, /func \(s \*Server\) listSessions/);
  assert.match(auth, /func \(s \*Server\) revokeSession/);
  assert.match(routes, /func \(s \*Server\) actorForUser/);
  assert.match(permissions, /func \(s \*Server\) nodePermissions/);
  assert.match(permissions, /func \(s \*Server\) albumPermissions/);
  assert.match(nodes, /s\.nodePermissions\(r\.Context\(\), a, spaceID, ids\)/);
  assert.match(nodes, /n\.section='files'/);
  assert.match(photos, /n\.section='photos'/);
  assert.match(photos, /s\.albumPermissions\(r\.Context\(\), a, spaceID, ids\)/);
  assert.match(publicFlows, /loading="lazy" decoding="async"/);
  assert.match(publicFlows, /form method="post" action=\{data\.archiveUrl\}/);
  assert.match(publicFlows, /name="access_token" value=\{archiveAccessToken\}/);
  assert.doesNotMatch(routes, /Get\("\/public\/shares\/\{token\}\/archive"/);
  assert.match(routes, /Post\("\/public\/shares\/\{token\}\/archive", s\.publicShareArchive\)/);
  assert.match(styles, /\.heading-actions \.folder-upload-button \{ display: inline-flex; \}/);
  assert.match(styles, /\.album-card-open \{ position: absolute; inset: 0; z-index: 1;/);
  assert.match(styles, /\.sidebar-notification \.notification-menu \{ top: auto;/);
  assert.match(styles, /\.topbar \{ display: none; \}/);
  assert.match(styles, /\.modal \{ max-height: calc\(100dvh - 20px\); overflow-y: auto; \}/);
  assert.match(worker, /DELETE FROM invitations WHERE \(accepted_at IS NOT NULL OR expires_at<now\(\)\).*30 days/);
  assert.match(worker, /DELETE FROM password_resets WHERE \(used_at IS NOT NULL OR expires_at<now\(\)\).*30 days/);
  assert.match(worker, /UPDATE nodes n SET purge_job_id=NULL WHERE n\.purge_job_id IS NOT NULL/);
  const deleteNode = worker.indexOf("DELETE FROM nodes WHERE id=$1");
  const deleteAsset = worker.indexOf("DELETE FROM assets WHERE id=$1", deleteNode);
  assert.ok(deleteNode >= 0 && deleteAsset > deleteNode, "purge must delete the node tree before its assets");
});

test("release artifacts enforce health, isolation, recovery, and CI checks", async () => {
  const [
    compose,
    device,
    production,
    webDockerfile,
    backendDockerfile,
    caddyDockerfile,
    postgresDockerfile,
    rustfsDockerfile,
    operations,
    backup,
    workflow,
    config,
    packageJson,
  ] = await Promise.all([
    readFile(new URL("compose.yaml", root), "utf8"),
    readFile(new URL("compose.device.yaml", root), "utf8"),
    readFile(new URL("compose.production.yaml", root), "utf8"),
    readFile(new URL("Dockerfile", root), "utf8"),
    readFile(new URL("backend/Dockerfile", root), "utf8"),
    readFile(new URL("deploy/Dockerfile.caddy", root), "utf8"),
    readFile(new URL("deploy/Dockerfile.postgres", root), "utf8"),
    readFile(new URL("deploy/Dockerfile.rustfs", root), "utf8"),
    readFile(new URL("docs/operations.md", root), "utf8"),
    readFile(new URL("scripts/backup.ps1", root), "utf8"),
    readFile(new URL(".github/workflows/ci.yml", root), "utf8"),
    readFile(new URL("backend/internal/config/config.go", root), "utf8"),
    readFile(new URL("package.json", root), "utf8"),
  ]);
  assert.match(compose, /rustfs:[\s\S]*service_healthy/);
  assert.match(compose, /POSTGRES_BIND_ADDRESS:-127\.0\.0\.1/);
  assert.match(compose, /RUSTFS_BIND_ADDRESS:-127\.0\.0\.1/);
  assert.match(compose, /x-logging: &default_logging[\s\S]*max-size: 10m/);
  assert.match(compose, /worker:[\s\S]*api:[\s\S]*service_healthy/);
  assert.match(device, /ports: !override[\s\S]*9100:9000/);
  assert.match(device, /volumes: !override[\s\S]*\/srv\/qiyun-drive-rustfs/);
  assert.match(production, /rustfs\/rustfs/);
  assert.match(production, /1\.0\.0-rc\.2/);
  assert.match(webDockerfile, /USER node/);
  assert.match(webDockerfile, /apk upgrade --no-cache/);
  assert.match(webDockerfile, /rm -rf \/usr\/local\/lib\/node_modules\/npm/);
  assert.match(backendDockerfile, /USER pan/);
  assert.match(backendDockerfile, /golang:1\.26\.6-alpine/);
  assert.match(backendDockerfile, /apk upgrade --no-cache/);
  assert.match(caddyDockerfile, /golang:1\.26\.6-alpine/);
  assert.match(caddyDockerfile, /CADDY_VERSION=v2\.11\.4/);
  assert.match(caddyDockerfile, /CADDY_COMMIT=e2eee6a7fce366321294c9c2a79f3146891dcbdf/);
  assert.match(caddyDockerfile, /CADDY_CEL_V2_COMMIT=b2693fb63a30e6d7be0972c3645e9a2c0a500e93/);
  assert.match(caddyDockerfile, /github\.com\/google\/cel-go@v0\.30\.0/);
  assert.match(caddyDockerfile, /github\.com\/klauspost\/compress@v1\.18\.7/);
  assert.match(caddyDockerfile, /github\.com\/go-chi\/chi\/v5@v5\.3\.0/);
  assert.match(caddyDockerfile, /golang\.org\/x\/crypto@v0\.56\.0/);
  assert.match(caddyDockerfile, /USER caddy/);
  assert.match(postgresDockerfile, /POSTGRES_IMAGE=postgres:17\.11-alpine/);
  assert.match(postgresDockerfile, /rm -f \/usr\/local\/bin\/gosu/);
  assert.match(postgresDockerfile, /USER postgres/);
  assert.match(rustfsDockerfile, /apk upgrade --no-cache/);
  assert.match(rustfsDockerfile, /USER rustfs/);
  assert.match(compose, /caddy:[\s\S]*dockerfile: deploy\/Dockerfile\.caddy[\s\S]*read_only: true/);
  assert.match(compose, /net\.ipv4\.ip_unprivileged_port_start: 0/);
  assert.match(compose, /caddy_data_v2:\/data/);
  assert.match(compose, /\/config:rw,noexec,nosuid,nodev,size=16777216,uid=10002,gid=10002/);
  assert.match(compose, /web:[\s\S]*read_only: true[\s\S]*\/tmp:rw,noexec,nosuid,nodev,size=67108864,uid=1000,gid=1000/);
  assert.match(compose, /postgres:[\s\S]*dockerfile: deploy\/Dockerfile\.postgres[\s\S]*read_only: true/);
  assert.match(compose, /rustfs:[\s\S]*dockerfile: deploy\/Dockerfile\.rustfs[\s\S]*read_only: true/);
  assert.match(compose, /security_opt:[\s\S]*no-new-privileges:true/);
  assert.match(compose, /cap_drop:[\s\S]*- ALL/);
  assert.match(compose, /worker:[\s\S]*read_only: true[\s\S]*tmpfs:[\s\S]*noexec,nosuid,nodev/);
  assert.match(operations, /health\/ready/);
  assert.match(operations, /backup\.ps1/);
  assert.match(backup, /pg_dump/);
  assert.match(backup, /SHA256/);
  assert.match(backup, /runningServices[\s\S]*servicesToResume/);
  assert.match(workflow, /PAN_LIVE_INTEGRATION:[\s\S]*TestLiveAccountAndAuthorizationBoundaries/);
  assert.match(workflow, /docker compose config --quiet/);
  assert.match(workflow, /docker compose build caddy web api worker/);
  assert.match(config, /PUBLIC_BASE_URL must use HTTPS outside localhost or a private network/);
  assert.match(config, /S3_PUBLIC_ENDPOINT must use HTTPS outside localhost or a private network/);
  assert.match(compose, /TRUST_PROXY: "true"/);
  assert.equal(JSON.parse(packageJson).scripts.check, "npm run lint && npm test");
});
