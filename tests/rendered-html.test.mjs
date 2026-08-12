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
  assert.match(drive, /api<\{ items: TrashItem\[\] \}>\("\/trash"\)/);
  assert.match(drive, /activeShareCount > 0/);
  assert.doesNotMatch(drive, /className="nav-count">3</);
  assert.match(drive, /上传文件夹/);
  assert.match(drive, /上传照片/);
  assert.match(drive, /`\/albums\/\$\{albumId\}\/items`/);
  assert.match(uploader, /return session\.nodeId/);
  assert.match(uploader, /albumId\?: string/);
  assert.match(uploader, /albumId: options\.albumId/);
  assert.match(drive, /resumable\.albumId/);
});

test("drive UI exposes complete album, file-management, permission, and account flows", async () => {
  const [drive, routes, nodes, acl, auth, worker] = await Promise.all([
    readFile(new URL("app/cloud-drive.tsx", root), "utf8"),
    readFile(new URL("backend/internal/httpapi/server.go", root), "utf8"),
    readFile(new URL("backend/internal/httpapi/nodes.go", root), "utf8"),
    readFile(new URL("backend/internal/httpapi/acl.go", root), "utf8"),
    readFile(new URL("backend/internal/httpapi/auth_handlers.go", root), "utf8"),
    readFile(new URL("backend/cmd/worker/main.go", root), "utf8"),
  ]);
  assert.match(drive, /function AlbumDetail/);
  assert.match(drive, /function PhotoViewer/);
  assert.match(drive, /savePhotoRemark/);
  assert.match(drive, /function MoveDialog/);
  assert.match(drive, /function PermissionDialog/);
  assert.match(drive, /function AccountDialog/);
  assert.match(drive, /function FilePreviewDialog/);
  assert.match(drive, /function ShareCreatedDialog/);
  assert.match(drive, /文件排序方式/);
  assert.match(drive, /api<\{ items: Member\[\] \}>\("\/members"\)/);
  assert.match(drive, /最近活动/);
  assert.match(drive, /有效分享/);
  assert.match(drive, /历史记录/);
  assert.match(routes, /Get\("\/folders\/tree", s\.listFolderTree\)/);
  assert.match(routes, /Post\("\/nodes\/\{id\}\/move", s\.moveNode\)/);
  assert.match(routes, /Get\("\/nodes\/\{id\}\/permissions", s\.getNodePermissions\)/);
  assert.match(routes, /Post\("\/me\/password", s\.changePassword\)/);
  assert.match(nodes, /func \(s \*Server\) moveNode/);
  assert.match(acl, /func \(s \*Server\) getNodePermissions/);
  assert.match(auth, /func \(s \*Server\) updateMemberRole/);
  const deleteNode = worker.indexOf("DELETE FROM nodes WHERE id=$1");
  const deleteAsset = worker.indexOf("DELETE FROM assets WHERE id=$1", deleteNode);
  assert.ok(deleteNode >= 0 && deleteAsset > deleteNode, "purge must delete the node tree before its assets");
});
