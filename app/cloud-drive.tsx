"use client";
/* eslint-disable @next/next/no-img-element */
/* User-owned media may not include a separate captions track. */
/* eslint-disable jsx-a11y/media-has-caption */

import {
  Archive,
  ArrowLeft,
  ArrowUpDown,
  Bell,
  Camera,
  ChevronDown,
  ChevronRight,
  Clock3,
  Cloud,
  Copy,
  Download,
  Edit3,
  Eye,
  ExternalLink,
  File,
  FileArchive,
  FileImage,
  FileText,
  Folder,
  FolderInput,
  FolderOpen,
  Grid2X2,
  HardDrive,
  Heart,
  Home,
  Image as ImageIcon,
  Images,
  Link2,
  List,
  LogOut,
  Menu,
  KeyRound,
  Pause,
  Play,
  Plus,
  Search,
  Save,
  Settings,
  Share2,
  Square,
  CheckSquare,
  ShieldCheck,
  Sparkles,
  Trash2,
  UploadCloud,
  UserPlus,
  UserRound,
  Users,
  X,
} from "lucide-react";
import { FormEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, clearResumeState, createFolderBatch, loadResumableUploads, resumeMultipartUpload, uploadFile, type UploadSection, type UploadTask } from "./upload-client";

type View = "files" | "photos" | "albums" | "shares" | "trash" | "family";
type Space = { id: string; kind: "personal" | "family"; name: string; quotaBytes: number; usedBytes: number; reservedBytes: number; permission: string };
type NodeItem = { id: string; spaceId: string; parentId?: string; kind: "file" | "folder"; name: string; sizeBytes: number; mimeType: string; status: string; permission: string; updatedAt: string };
type TrashItem = NodeItem & { deletedAt: string; purgeAt: string };
type PhotoItem = { nodeId: string; name: string; thumbUrl?: string; previewUrl?: string; takenAt?: string; createdAt: string; remark: string; width?: number; height?: number; camera?: string; tone?: string };
type Album = { id: string; name: string; description: string; itemCount: number; createdAt: string; coverUrl?: string; permission?: string };
type Member = { id: string; username: string; displayName: string; role: string; createdAt: string };
type CurrentUser = { id: string; username: string; displayName: string; role: string; householdId: string };
type ShareItem = { id: string; resourceType: string; resourceId: string; resourceName: string; hasPassword: boolean; allowDownload: boolean; expiresAt?: string; revokedAt?: string; createdAt: string; creatorName?: string; own?: boolean };
type SessionItem = { id: string; current: boolean; createdAt: string; lastSeenAt: string; expiresAt: string };
type CreatedActionLink = { title: string; description: string; url: string };

function clientUUID() {
  const secureCrypto = globalThis.crypto;
  if (typeof secureCrypto?.randomUUID === "function") return secureCrypto.randomUUID();
  if (typeof secureCrypto?.getRandomValues !== "function") {
    return `client-${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}-${Math.random().toString(36).slice(2)}`;
  }
  const bytes = secureCrypto.getRandomValues(new Uint8Array(16));
  bytes[6] = (bytes[6] & 0x0f) | 0x40;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const hex = Array.from(bytes, (value) => value.toString(16).padStart(2, "0"));
  return `${hex.slice(0, 4).join("")}-${hex.slice(4, 6).join("")}-${hex.slice(6, 8).join("")}-${hex.slice(8, 10).join("")}-${hex.slice(10).join("")}`;
}
type FolderOption = { id: string; parentId?: string; name: string; permission: string; updatedAt: string };
type PermissionEntry = { userId: string; username?: string; displayName?: string; permission: "none" | "viewer" | "editor" | "manager" };
type PermissionEditorState = { resourceType: "node" | "album"; id: string; name: string; inherit: boolean; entries: PermissionEntry[] };
type AuditItem = { id: string; action: string; resourceType?: string; resourceId?: string; metadata: string; actorName?: string; createdAt: string };
type FilePreviewState = { item: NodeItem; url: string; mimeType: string };

const previewSpaces: Space[] = [
  { id: "personal", kind: "personal", name: "陈谨的空间", quotaBytes: 500 * 1024 ** 3, usedBytes: 128.6 * 1024 ** 3, reservedBytes: 0, permission: "manager" },
  { id: "family", kind: "family", name: "我们的家", quotaBytes: 2 * 1024 ** 4, usedBytes: 684.2 * 1024 ** 3, reservedBytes: 0, permission: "manager" },
];

const previewNodes: NodeItem[] = [
  { id: "f1", spaceId: "personal", kind: "folder", name: "工作文档", sizeBytes: 0, mimeType: "", status: "ready", permission: "manager", updatedAt: "2026-08-11T09:20:00Z" },
  { id: "f2", spaceId: "personal", kind: "folder", name: "旅行与生活", sizeBytes: 0, mimeType: "", status: "ready", permission: "manager", updatedAt: "2026-08-10T18:20:00Z" },
  { id: "f3", spaceId: "personal", kind: "folder", name: "家庭资料", sizeBytes: 0, mimeType: "", status: "ready", permission: "manager", updatedAt: "2026-08-08T08:00:00Z" },
  { id: "d1", spaceId: "personal", kind: "file", name: "2026 家庭旅行计划.pdf", sizeBytes: 4.8 * 1024 ** 2, mimeType: "application/pdf", status: "ready", permission: "manager", updatedAt: "2026-08-11T06:12:00Z" },
  { id: "d2", spaceId: "personal", kind: "file", name: "证件资料备份.zip", sizeBytes: 28.3 * 1024 ** 2, mimeType: "application/zip", status: "ready", permission: "manager", updatedAt: "2026-08-09T06:12:00Z" },
  { id: "d3", spaceId: "personal", kind: "file", name: "搬家清单.xlsx", sizeBytes: 824 * 1024, mimeType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", status: "ready", permission: "manager", updatedAt: "2026-08-04T06:12:00Z" },
];

const previewPhotos: PhotoItem[] = [
  { nodeId: "p1", name: "山间清晨.heic", createdAt: "2026-08-10", takenAt: "2026-08-10", remark: "雨停后的第一束光", tone: "mountain" },
  { nodeId: "p2", name: "海边日落.jpg", createdAt: "2026-08-10", takenAt: "2026-08-10", remark: "", tone: "sunset" },
  { nodeId: "p3", name: "夏日餐桌.jpg", createdAt: "2026-08-08", takenAt: "2026-08-08", remark: "周末晚餐", tone: "table" },
  { nodeId: "p4", name: "林间小路.jpg", createdAt: "2026-08-08", takenAt: "2026-08-08", remark: "", tone: "forest" },
  { nodeId: "p5", name: "城市夜色.jpg", createdAt: "2026-08-03", takenAt: "2026-08-03", remark: "", tone: "city" },
  { nodeId: "p6", name: "家里的花.jpg", createdAt: "2026-08-03", takenAt: "2026-08-03", remark: "开得正好", tone: "flower" },
];

const previewAlbums: Album[] = [
  { id: "a1", name: "川西 · 2026", description: "雪山、草甸与一路的好天气", itemCount: 128, createdAt: "2026-08-10" },
  { id: "a2", name: "家的四季", description: "平常日子里值得留下的小事", itemCount: 86, createdAt: "2026-07-18" },
  { id: "a3", name: "父母的老照片", description: "正在慢慢整理和补充备注", itemCount: 214, createdAt: "2026-06-02" },
];

const navItems: { id: View; label: string; icon: typeof Home }[] = [
  { id: "files", label: "文件", icon: FolderOpen },
  { id: "photos", label: "照片", icon: Images },
  { id: "albums", label: "相册", icon: Heart },
  { id: "shares", label: "分享", icon: Share2 },
  { id: "trash", label: "回收站", icon: Trash2 },
];

const viewMeta: Record<View, { title: string; kicker: string }> = {
  files: { title: "所有文件", kicker: "把重要的东西，都放在安心的地方" },
  photos: { title: "照片时光", kicker: "按拍摄时间，重新遇见每个瞬间" },
  albums: { title: "我的相册", kicker: "把故事整理成册" },
  shares: { title: "分享管理", kicker: "清楚掌握每一条对外分享" },
  trash: { title: "回收站", kicker: "已删除内容将在 30 天后自动清理" },
  family: { title: "家庭管理", kicker: "成员、权限与空间用量" },
};

function formatBytes(value: number) {
  if (!Number.isFinite(value) || value < 0) return "—";
  if (value === 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1);
  return `${(value / 1024 ** index).toFixed(index > 2 ? 1 : 0)} ${units[index]}`;
}

function relativeDate(value?: string) {
  if (!value) return "刚刚";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat("zh-CN", { month: "short", day: "numeric" }).format(date);
}

function fileIcon(item: NodeItem) {
  if (item.kind === "folder") return <Folder size={22} />;
  if (item.mimeType?.startsWith("image/")) return <FileImage size={21} />;
  if (item.mimeType?.includes("zip")) return <FileArchive size={21} />;
  if (item.mimeType?.includes("pdf") || item.mimeType?.includes("sheet")) return <FileText size={21} />;
  return <File size={21} />;
}

export function CloudDrive() {
  const [status, setStatus] = useState<"loading" | "preview" | "setup" | "login" | "ready">("loading");
  const [view, setView] = useState<View>("files");
  const [spaces, setSpaces] = useState<Space[]>(previewSpaces);
  const [spaceId, setSpaceId] = useState("personal");
  const [nodes, setNodes] = useState<NodeItem[]>(previewNodes);
  const [photos, setPhotos] = useState<PhotoItem[]>(previewPhotos);
  const [albums, setAlbums] = useState<Album[]>(previewAlbums);
  const [selectedAlbum, setSelectedAlbum] = useState<Album | null>(null);
  const [albumPhotos, setAlbumPhotos] = useState<PhotoItem[]>([]);
  const [photoViewer, setPhotoViewer] = useState<{ photo: PhotoItem; albumId?: string } | null>(null);
  const [filePreview, setFilePreview] = useState<FilePreviewState | null>(null);
  const [albumEditor, setAlbumEditor] = useState<Album | null>(null);
  const [nodeEditor, setNodeEditor] = useState<NodeItem | null>(null);
  const [members, setMembers] = useState<Member[]>([
    { id: "m1", username: "chenjin", displayName: "陈谨", role: "owner", createdAt: "2026-06-01" },
    { id: "m2", username: "lin", displayName: "林夕", role: "member", createdAt: "2026-06-03" },
    { id: "m3", username: "mum", displayName: "妈妈", role: "member", createdAt: "2026-06-08" },
  ]);
  const [auditItems, setAuditItems] = useState<AuditItem[]>([]);
  const [currentUser, setCurrentUser] = useState<CurrentUser | null>(null);
  const [accountOpen, setAccountOpen] = useState(false);
  const [accountDialogOpen, setAccountDialogOpen] = useState(false);
  const [sessions, setSessions] = useState<SessionItem[]>([]);
  const [sessionsLoading, setSessionsLoading] = useState(false);
  const [notificationsOpen, setNotificationsOpen] = useState(false);
  const [permissionGuideOpen, setPermissionGuideOpen] = useState(false);
  const [permissionEditor, setPermissionEditor] = useState<PermissionEditorState | null>(null);
  const [moveEditor, setMoveEditor] = useState<{ items: NodeItem[]; folders: FolderOption[] } | null>(null);
  const [quotaEditor, setQuotaEditor] = useState<Space | null>(null);
  const [shares, setShares] = useState<ShareItem[]>([]);
  const [trashNodes, setTrashNodes] = useState<TrashItem[]>([]);
  const [shareTarget, setShareTarget] = useState<{ id: string; kind: "file" | "folder" | "album"; name: string } | null>(null);
  const [createdShareURL, setCreatedShareURL] = useState("");
  const [createdActionLink, setCreatedActionLink] = useState<CreatedActionLink | null>(null);
  const [currentParent, setCurrentParent] = useState<string | null>(null);
  const [breadcrumbs, setBreadcrumbs] = useState<{ id: string | null; name: string }[]>([{ id: null, name: "我的空间" }]);
  const [grid, setGrid] = useState(false);
  const [search, setSearch] = useState("");
  const [menuOpen, setMenuOpen] = useState(false);
  const [spaceOpen, setSpaceOpen] = useState(false);
  const [dialog, setDialog] = useState<null | "folder" | "album" | "invite">(null);
  const [toast, setToast] = useState("");
  const [uploads, setUploads] = useState<UploadTask[]>([]);
  const [setupForm, setSetupForm] = useState({ householdName: "我们的家", timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "Asia/Shanghai", username: "", displayName: "", password: "" });
  const [loginForm, setLoginForm] = useState({ username: "", password: "" });
  const fileInput = useRef<HTMLInputElement>(null);
  const folderInput = useRef<HTMLInputElement>(null);
  const photoInput = useRef<HTMLInputElement>(null);
  const albumInput = useRef<HTMLInputElement>(null);
  const searchInput = useRef<HTMLInputElement>(null);
  const albumUploadTarget = useRef<Album | null>(null);
  const resumeAttempted = useRef(false);
  const uploadControllers = useRef(new Map<string, AbortController>());
  const refreshController = useRef<AbortController | null>(null);

  const selectedSpace = spaces.find((space) => space.id === spaceId) || spaces[0];
  const activeModal = dialog ? `name:${dialog}` : shareTarget ? "share" : createdShareURL ? "created-share" : createdActionLink ? "created-action" : photoViewer ? "photo" : filePreview ? "file-preview" : albumEditor ? "album-edit" : nodeEditor ? "rename" : moveEditor ? "move" : permissionEditor ? "permission" : accountDialogOpen ? "account" : permissionGuideOpen ? "permission-guide" : quotaEditor ? "quota" : "";

  const loadWorkspace = useCallback(async () => {
    const [spaceResponse, shareResponse, me, memberResponse] = await Promise.all([
      api<{ items: Space[] }>("/spaces"),
      api<{ items: ShareItem[] }>("/shares"),
      api<CurrentUser>("/me"),
      api<{ items: Member[] }>("/members"),
    ]);
    setSpaces(spaceResponse.items);
    setShares(shareResponse.items);
    setCurrentUser(me);
    setMembers(memberResponse.items);
    const preferred = spaceResponse.items[0];
    if (preferred) setSpaceId(preferred.id);
    setStatus("ready");
  }, []);

  const loadAlbumItems = useCallback(async (albumId: string) => {
    const result = await api<{ items: PhotoItem[] }>(`/albums/${albumId}/items`);
    setAlbumPhotos(result.items);
    return result.items;
  }, []);

  useEffect(() => {
    (async () => {
      try {
        const bootstrap = await api<{ initialized: boolean }>("/bootstrap");
        if (!bootstrap.initialized) {
          setStatus("setup");
          return;
        }
        try {
          await api("/me");
          await loadWorkspace();
        } catch {
          setStatus("login");
        }
      } catch {
        setStatus("preview");
      }
    })();
  }, [loadWorkspace]);

  const refreshView = useCallback(async () => {
    if (status !== "ready" || !spaceId) return;
    refreshController.current?.abort();
    const controller = new AbortController();
    refreshController.current = controller;
    const request = <T,>(path: string) => api<T>(path, { signal: controller.signal });
    try {
      if (view === "files") {
        const result = await request<{ items: NodeItem[] }>(`/nodes?spaceId=${spaceId}${currentParent ? `&parentId=${currentParent}` : ""}`);
        if (!controller.signal.aborted) setNodes(result.items);
      } else if (view === "photos") {
        const result = await request<{ items: PhotoItem[] }>(`/photos?spaceId=${spaceId}`);
        if (!controller.signal.aborted) setPhotos(result.items);
      } else if (view === "albums") {
        const result = await request<{ items: Album[] }>(`/albums?spaceId=${spaceId}`);
        if (!controller.signal.aborted) setAlbums(result.items);
      } else if (view === "family") {
        const memberRequest = request<{ items: Member[] }>("/members");
        const canAudit = currentUser?.role === "owner" || currentUser?.role === "admin";
        const [result, audit] = await Promise.all([memberRequest, canAudit ? request<{ items: AuditItem[] }>("/admin/audit") : Promise.resolve({ items: [] as AuditItem[] })]);
        if (!controller.signal.aborted) {
          setMembers(result.items);
          setAuditItems(audit.items);
        }
      } else if (view === "shares") {
        const result = await request<{ items: ShareItem[] }>("/shares");
        if (!controller.signal.aborted) setShares(result.items);
      } else if (view === "trash") {
        const result = await request<{ items: TrashItem[] }>("/trash");
        if (!controller.signal.aborted) setTrashNodes(result.items);
      }
    } catch (error) {
      if (!(error instanceof DOMException && error.name === "AbortError")) showToast(error instanceof Error ? error.message : "加载失败");
    } finally {
      if (refreshController.current === controller) refreshController.current = null;
    }
  }, [currentParent, currentUser?.role, spaceId, status, view]);

  useEffect(() => { refreshView(); }, [refreshView]);

  useEffect(() => () => refreshController.current?.abort(), []);

  useEffect(() => {
    if (!activeModal) return;
    const previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const dialogs = document.querySelectorAll<HTMLElement>('[aria-modal="true"],.modal-backdrop > .modal');
    const modal = dialogs.item(dialogs.length - 1);
    if (!modal) return;
    modal.setAttribute("role", "dialog");
    modal.setAttribute("aria-modal", "true");
    if (!modal.hasAttribute("aria-label") && !modal.hasAttribute("aria-labelledby")) {
      const title = modal.querySelector<HTMLElement>("h2");
      if (title) {
        title.id ||= "active-modal-title";
        modal.setAttribute("aria-labelledby", title.id);
      }
    }
    if (!modal.hasAttribute("tabindex")) modal.tabIndex = -1;
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    const focusable = () => Array.from(modal.querySelectorAll<HTMLElement>('button:not([disabled]),a[href],input:not([disabled]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])')).filter((element) => element.getClientRects().length > 0);
    const focusTimer = window.setTimeout(() => (focusable()[0] || modal).focus(), 0);
    const close = () => {
      if (activeModal.startsWith("name:")) setDialog(null);
      else if (activeModal === "share") setShareTarget(null);
      else if (activeModal === "created-share") setCreatedShareURL("");
      else if (activeModal === "created-action") setCreatedActionLink(null);
      else if (activeModal === "photo") setPhotoViewer(null);
      else if (activeModal === "file-preview") setFilePreview(null);
      else if (activeModal === "album-edit") setAlbumEditor(null);
      else if (activeModal === "rename") setNodeEditor(null);
      else if (activeModal === "move") setMoveEditor(null);
      else if (activeModal === "permission") setPermissionEditor(null);
      else if (activeModal === "account") setAccountDialogOpen(false);
      else if (activeModal === "permission-guide") setPermissionGuideOpen(false);
      else if (activeModal === "quota") setQuotaEditor(null);
    };
    const handleKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        close();
        return;
      }
      if (event.key !== "Tab") return;
      const elements = focusable();
      if (!elements.length) {
        event.preventDefault();
        modal.focus();
        return;
      }
      const first = elements[0];
      const last = elements[elements.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener("keydown", handleKey);
    return () => {
      window.clearTimeout(focusTimer);
      document.removeEventListener("keydown", handleKey);
      document.body.style.overflow = previousOverflow;
      if (previousFocus?.isConnected) previousFocus.focus();
    };
  }, [activeModal]);

  useEffect(() => {
    const focusSearch = (event: KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        searchInput.current?.focus();
      }
    };
    window.addEventListener("keydown", focusSearch);
    return () => window.removeEventListener("keydown", focusSearch);
  }, []);

  useEffect(() => {
    if (status !== "ready" || resumeAttempted.current) return;
    resumeAttempted.current = true;
    (async () => {
      const pending = await loadResumableUploads();
      if (!pending.length) return;
      const tasks: UploadTask[] = pending.map((item) => ({ id: item.key, name: item.file.name, relativePath: item.file.webkitRelativePath || item.file.name, size: item.file.size, progress: 0, state: "uploading" }));
      setUploads((current) => [...tasks, ...current]);
      await Promise.all(pending.map(async (item) => {
        const controller = new AbortController();
        try {
          const nodeId = await resumeMultipartUpload(item, controller.signal, (progress) => setUploads((items) => items.map((task) => task.id === item.key ? { ...task, progress } : task)));
          if (item.albumId && nodeId) {
            try {
              await api(`/albums/${item.albumId}/items`, { method: "POST", body: JSON.stringify({ nodeIds: [nodeId] }) });
            } catch {
              // The upload itself is complete even if the original album was removed meanwhile.
            }
          }
          setUploads((items) => items.map((task) => task.id === item.key ? { ...task, progress: 1, state: "ready" } : task));
        } catch (error) {
          const message = error instanceof Error ? error.message : "恢复上传失败";
          setUploads((items) => items.map((task) => task.id === item.key ? { ...task, state: "failed", error: message } : task));
          if (/过期|不存在/.test(message)) await clearResumeState(item.key);
        }
      }));
      await refreshView();
    })();
  }, [refreshView, status]);

  const showToast = (message: string) => {
    setToast(message);
    window.setTimeout(() => setToast(""), 2600);
  };

  const changeView = (next: View) => {
    setView(next);
    setSelectedAlbum(null);
    setPhotoViewer(null);
    setFilePreview(null);
    setCreatedShareURL("");
    setCreatedActionLink(null);
    setSessions([]);
    setMenuOpen(false);
    setAccountOpen(false);
    setNotificationsOpen(false);
    setSpaceOpen(false);
    setCurrentParent(null);
    setBreadcrumbs([{ id: null, name: selectedSpace?.name || "空间" }]);
  };

  const submitLogin = async (event: FormEvent) => {
    event.preventDefault();
    try {
      await api("/auth/login", { method: "POST", body: JSON.stringify(loginForm) });
      await loadWorkspace();
    } catch (error) { showToast(error instanceof Error ? error.message : "登录失败"); }
  };

  const submitSetup = async (event: FormEvent) => {
    event.preventDefault();
    try {
      await api("/bootstrap", { method: "POST", body: JSON.stringify(setupForm) });
      await loadWorkspace();
    } catch (error) { showToast(error instanceof Error ? error.message : "初始化失败"); }
  };

  const logout = async () => {
    try { await api("/auth/logout", { method: "POST" }); } catch { /* cookie is cleared by a successful server response only */ }
    setAccountOpen(false);
    setCurrentUser(null);
    setStatus("login");
    setView("files");
    setSelectedAlbum(null);
    setPhotoViewer(null);
    setFilePreview(null);
  };

  const updateProfile = async (displayName: string) => {
    if (!currentUser) return;
    try {
      const updated = await api<CurrentUser>("/me", { method: "PATCH", body: JSON.stringify({ displayName }) });
      setCurrentUser(updated);
      setAccountDialogOpen(false);
      showToast("个人资料已更新");
    } catch (error) { showToast(error instanceof Error ? error.message : "个人资料更新失败"); }
  };

  const changeOwnPassword = async (currentPassword: string, newPassword: string) => {
    try {
      await api("/me/password", { method: "POST", body: JSON.stringify({ currentPassword, newPassword }) });
      setAccountDialogOpen(false);
      showToast("密码已更新，其他设备的会话已退出");
    } catch (error) { showToast(error instanceof Error ? error.message : "密码更新失败"); }
  };

  const openAccountSettings = async () => {
    setAccountOpen(false);
    setAccountDialogOpen(true);
    if (status === "preview") {
      const now = new Date().toISOString();
      setSessions([{ id: "preview-session", current: true, createdAt: now, lastSeenAt: now, expiresAt: new Date(Date.now() + 30 * 86400000).toISOString() }]);
      return;
    }
    setSessionsLoading(true);
    try {
      const result = await api<{ items: SessionItem[] }>("/me/sessions");
      setSessions(result.items);
    } catch (error) {
      showToast(error instanceof Error ? error.message : "无法加载登录设备");
    } finally {
      setSessionsLoading(false);
    }
  };

  const revokeOwnSession = async (session: SessionItem) => {
    if (session.current) return;
    try {
      await api(`/me/sessions/${session.id}`, { method: "DELETE" });
      setSessions((items) => items.filter((item) => item.id !== session.id));
      showToast("该登录会话已退出");
    } catch (error) {
      showToast(error instanceof Error ? error.message : "退出会话失败");
    }
  };

  const openFolder = (item: NodeItem) => {
    if (item.kind !== "folder") return;
    setCurrentParent(item.id);
    setBreadcrumbs((items) => [...items, { id: item.id, name: item.name }]);
  };

  const openNode = async (item: NodeItem) => {
    if (item.kind === "folder") {
      openFolder(item);
      return;
    }
    try {
      const preview = await api<{ url: string; mimeType: string }>(`/nodes/${item.id}/preview`);
      setFilePreview({ item, url: preview.url, mimeType: preview.mimeType || item.mimeType });
    } catch (error) {
      showToast(error instanceof Error ? error.message : "无法预览此文件");
    }
  };

  const goBreadcrumb = (index: number) => {
    const item = breadcrumbs[index];
    setCurrentParent(item.id);
    setBreadcrumbs((items) => items.slice(0, index + 1));
  };

  const createNamedItem = async (name: string) => {
    if (!name.trim()) return;
    if (status === "preview") {
      if (dialog === "folder") setNodes((items) => [{ id: clientUUID(), spaceId, kind: "folder", name, sizeBytes: 0, mimeType: "", status: "ready", permission: "manager", updatedAt: new Date().toISOString() }, ...items]);
      if (dialog === "album") setAlbums((items) => [{ id: clientUUID(), name, description: "刚刚创建的相册", itemCount: 0, createdAt: new Date().toISOString() }, ...items]);
      showToast(dialog === "folder" ? "文件夹已创建" : "相册已创建");
    } else {
      try {
        if (dialog === "folder") await api("/folders", { method: "POST", body: JSON.stringify({ spaceId, parentId: currentParent, name }) });
        if (dialog === "album") await api("/albums", { method: "POST", body: JSON.stringify({ spaceId, name, description: "" }) });
        await refreshView();
        showToast(dialog === "folder" ? "文件夹已创建" : "相册已创建");
      } catch (error) { showToast(error instanceof Error ? error.message : "创建失败"); }
    }
    setDialog(null);
  };

  const handleFiles = async (selected: File[], albumId?: string, section: UploadSection = "files") => {
    if (!selected.length) return;
    if (status === "preview") {
      setUploads(selected.map((file) => ({ id: clientUUID(), name: file.name, relativePath: file.webkitRelativePath || file.name, size: file.size, progress: 1, state: "ready" })));
      showToast(`已加入 ${selected.length} 个${section === "photos" ? "照片" : "文件"}（界面预览）`);
      return;
    }
    let batchId: string | undefined;
    if (section === "files") {
      try { batchId = await createFolderBatch(spaceId, currentParent, selected); } catch (error) { showToast(error instanceof Error ? error.message : "无法创建上传任务"); return; }
    }
    const tasks = selected.map((file) => ({ id: clientUUID(), name: file.name, relativePath: file.webkitRelativePath || file.name, size: file.size, progress: 0, state: "queued" as const }));
    setUploads((current) => [...tasks, ...current]);
    const queue = [...selected.entries()];
    const uploadedNodeIds: string[] = [];
    const worker = async () => {
      while (queue.length) {
        const next = queue.shift();
        if (!next) return;
        const [index, file] = next;
        const task = tasks[index];
        const controller = new AbortController();
        uploadControllers.current.set(task.id, controller);
        setUploads((items) => items.map((item) => item.id === task.id ? { ...item, state: "uploading" } : item));
        try {
          const nodeId = await uploadFile(file, { spaceId, parentId: section === "photos" ? null : currentParent, batchId, albumId, section, resumeKey: task.id, signal: controller.signal, onProgress: (progress) => setUploads((items) => items.map((item) => item.id === task.id ? { ...item, progress } : item)) });
          uploadedNodeIds.push(nodeId);
          setUploads((items) => items.map((item) => item.id === task.id ? { ...item, progress: 1, state: "ready" } : item));
        } catch (error) {
          const paused = error instanceof DOMException && error.name === "AbortError";
          setUploads((items) => items.map((item) => item.id === task.id ? { ...item, state: paused ? "paused" : "failed", error: paused ? undefined : error instanceof Error ? error.message : "上传失败" } : item));
        } finally {
          uploadControllers.current.delete(task.id);
        }
      }
    };
    await Promise.all(Array.from({ length: Math.min(3, selected.length) }, worker));
    if (albumId && uploadedNodeIds.length) {
      const result = await api<{ added: number }>(`/albums/${albumId}/items`, { method: "POST", body: JSON.stringify({ nodeIds: uploadedNodeIds }) });
      showToast(`已上传并加入相册：${result.added} 张照片`);
      await loadAlbumItems(albumId);
    }
    await refreshView();
  };

  const chooseAlbumPhotos = (album: Album) => {
    albumUploadTarget.current = album;
    albumInput.current?.click();
  };

  const handlePhotos = async (selected: File[], albumId?: string) => {
    if (!selected.length) return;
    const images = selected.filter((file) => file.type.startsWith("image/") || /\.(jpe?g|png|webp|gif|heic|heif)$/i.test(file.name));
    if (!images.length) {
      showToast("请选择照片文件");
      return;
    }
    if (images.length !== selected.length) showToast(`已忽略 ${selected.length - images.length} 个非照片文件`);
    await handleFiles(images, albumId, "photos");
  };

  const handleAlbumPhotos = async (selected: File[]) => {
    const album = albumUploadTarget.current;
    albumUploadTarget.current = null;
    if (!album || !selected.length) return;
    await handlePhotos(selected, album.id);
  };

  const openAlbum = async (album: Album) => {
    setSelectedAlbum(album);
    try {
      await loadAlbumItems(album.id);
    } catch (error) {
      setSelectedAlbum(null);
      showToast(error instanceof Error ? error.message : "相册加载失败");
    }
  };

  const savePhotoRemark = async (photo: PhotoItem, remark: string) => {
    try {
      await api(`/photos/${photo.nodeId}`, { method: "PATCH", body: JSON.stringify({ remark }) });
      const update = (item: PhotoItem) => item.nodeId === photo.nodeId ? { ...item, remark } : item;
      setPhotos((items) => items.map(update));
      setAlbumPhotos((items) => items.map(update));
      setPhotoViewer((current) => current?.photo.nodeId === photo.nodeId ? { ...current, photo: { ...current.photo, remark } } : current);
      showToast("照片备注已保存");
    } catch (error) { showToast(error instanceof Error ? error.message : "备注保存失败"); }
  };

  const removePhotoFromAlbum = async (albumId: string, nodeId: string) => {
    try {
      await api(`/albums/${albumId}/items/${nodeId}`, { method: "DELETE" });
      setAlbumPhotos((items) => items.filter((item) => item.nodeId !== nodeId));
      setSelectedAlbum((album) => album ? { ...album, itemCount: Math.max(0, album.itemCount - 1) } : album);
      setPhotoViewer(null);
      await refreshView();
      showToast("已从相册移除，原照片仍保留在照片时间线中");
    } catch (error) { showToast(error instanceof Error ? error.message : "移除失败"); }
  };

  const updateAlbum = async (album: Album, values: { name: string; description: string }) => {
    try {
      await api(`/albums/${album.id}`, { method: "PATCH", body: JSON.stringify(values) });
      const updated = { ...album, ...values };
      setSelectedAlbum((current) => current?.id === album.id ? updated : current);
      setAlbums((items) => items.map((item) => item.id === album.id ? updated : item));
      setAlbumEditor(null);
      showToast("相册信息已更新");
    } catch (error) { showToast(error instanceof Error ? error.message : "相册更新失败"); }
  };

  const deleteAlbum = async (album: Album) => {
    if (!window.confirm(`删除相册「${album.name}」？照片原文件仍会保留。`)) return;
    try {
      await api(`/albums/${album.id}`, { method: "DELETE" });
      setAlbums((items) => items.filter((item) => item.id !== album.id));
      setSelectedAlbum(null);
      setAlbumPhotos([]);
      showToast("相册已删除，照片原文件未受影响");
    } catch (error) { showToast(error instanceof Error ? error.message : "删除相册失败"); }
  };

  const downloadPhoto = async (photo: PhotoItem) => {
    try {
      const result = await api<{ url: string }>(`/nodes/${photo.nodeId}/download`);
      window.location.href = result.url;
    } catch (error) { showToast(error instanceof Error ? error.message : "下载失败"); }
  };

  const resumeTask = async (taskId: string) => {
    const pending = await loadResumableUploads();
    const resumable = pending.find((item) => item.key === taskId || taskId.includes(item.session.id));
    if (!resumable) { showToast("上传会话已失效，请重新选择文件"); return; }
    const controller = new AbortController();
    uploadControllers.current.set(taskId, controller);
    setUploads((items) => items.map((item) => item.id === taskId ? { ...item, state: "uploading", error: undefined } : item));
    try {
      const nodeId = await resumeMultipartUpload(resumable, controller.signal, (progress) => setUploads((items) => items.map((item) => item.id === taskId ? { ...item, progress } : item)));
      if (resumable.albumId && nodeId) {
        try {
          await api(`/albums/${resumable.albumId}/items`, { method: "POST", body: JSON.stringify({ nodeIds: [nodeId] }) });
          if (selectedAlbum?.id === resumable.albumId) await loadAlbumItems(resumable.albumId);
        } catch {
          showToast("文件已上传，但原相册不可用，已保留在文件中");
        }
      }
      setUploads((items) => items.map((item) => item.id === taskId ? { ...item, progress: 1, state: "ready" } : item));
      await refreshView();
    } catch (error) {
      const paused = error instanceof DOMException && error.name === "AbortError";
      setUploads((items) => items.map((item) => item.id === taskId ? { ...item, state: paused ? "paused" : "failed", error: paused ? undefined : error instanceof Error ? error.message : "恢复失败" } : item));
    } finally { uploadControllers.current.delete(taskId); }
  };

  const downloadNode = async (item: NodeItem) => {
    if (status === "preview") { showToast(`准备下载 ${item.name}`); return; }
    try {
      if (item.kind === "folder") {
        window.location.href = `${process.env.NEXT_PUBLIC_API_BASE || "/api/v1"}/nodes/${item.id}/archive`;
      } else {
        const result = await api<{ url: string }>(`/nodes/${item.id}/download`);
        window.location.href = result.url;
      }
    } catch (error) { showToast(error instanceof Error ? error.message : "下载失败"); }
  };

  const renameNode = async (item: NodeItem, name: string) => {
    try {
      await api(`/nodes/${item.id}`, { method: "PATCH", body: JSON.stringify({ name }) });
      setNodeEditor(null);
      await refreshView();
      showToast("名称已更新");
    } catch (error) { showToast(error instanceof Error ? error.message : "重命名失败"); }
  };

  const trashNode = async (item: NodeItem) => {
    if (!window.confirm(`将「${item.name}」移到回收站？`)) return;
    try {
      await api(`/nodes/${item.id}`, { method: "DELETE" });
      setNodes((items) => items.filter((current) => current.id !== item.id));
      showToast("已移到回收站，可在 30 天内恢复");
    } catch (error) { showToast(error instanceof Error ? error.message : "删除失败"); }
  };

  const trashNodeBatch = async (items: NodeItem[]) => {
    if (!items.length || !window.confirm(`将选中的 ${items.length} 个项目移到回收站？`)) return;
    if (status === "preview") {
      const selected = new Set(items.map((item) => item.id));
      setNodes((current) => current.filter((item) => !selected.has(item.id)));
      showToast("已移到回收站（界面预览）");
      return;
    }
    try {
      await Promise.all(items.map((item) => api(`/nodes/${item.id}`, { method: "DELETE" })));
      const selected = new Set(items.map((item) => item.id));
      setNodes((current) => current.filter((item) => !selected.has(item.id)));
      showToast(`已将 ${items.length} 个项目移到回收站`);
    } catch (error) { await refreshView(); showToast(error instanceof Error ? error.message : "批量删除失败"); }
  };

  const openMoveEditor = async (items: NodeItem[]) => {
    if (!items.length) return;
    try {
      const folders = status === "preview"
        ? nodes.filter((item): item is NodeItem => item.kind === "folder").map((item) => ({ id: item.id, parentId: item.parentId, name: item.name, permission: item.permission, updatedAt: item.updatedAt }))
        : (await api<{ items: FolderOption[] }>(`/folders/tree?spaceId=${spaceId}`)).items;
      setMoveEditor({ items, folders });
    } catch (error) { showToast(error instanceof Error ? error.message : "无法加载文件夹列表"); }
  };

  const moveNodes = async (items: NodeItem[], parentId: string | null) => {
    try {
      if (status !== "preview") await Promise.all(items.map((item) => api(`/nodes/${item.id}/move`, { method: "POST", body: JSON.stringify({ parentId }) })));
      setMoveEditor(null);
      await refreshView();
      showToast(items.length > 1 ? `已移动 ${items.length} 个项目` : "项目已移动");
    } catch (error) { showToast(error instanceof Error ? error.message : "移动失败"); }
  };

  const openPermissionEditor = async (resourceType: "node" | "album", id: string, name: string) => {
    try {
      const result = await api<{ inherit: boolean; entries: PermissionEntry[] }>(`/${resourceType === "node" ? "nodes" : "albums"}/${id}/permissions`);
      setPermissionEditor({ resourceType, id, name, inherit: result.inherit, entries: result.entries });
    } catch (error) { showToast(error instanceof Error ? error.message : "权限设置加载失败"); }
  };

  const savePermissions = async (value: PermissionEditorState) => {
    try {
      await api(`/${value.resourceType === "node" ? "nodes" : "albums"}/${value.id}/permissions`, {
        method: "PUT",
        body: JSON.stringify({ inherit: value.inherit, entries: value.entries.filter((entry) => entry.permission !== "none").map(({ userId, permission }) => ({ userId, permission })) }),
      });
      setPermissionEditor(null);
      showToast("访问权限已更新");
    } catch (error) { showToast(error instanceof Error ? error.message : "权限更新失败"); }
  };

  const createShareLink = async (options: { password: string; days: number; allowDownload: boolean }) => {
    if (!shareTarget) return;
    if (status === "preview") {
      setCreatedShareURL("https://cloud.example/s/preview-link");
      setShareTarget(null);
      return;
    }
    try {
      const expiresAt = options.days > 0 ? new Date(Date.now() + options.days * 86400000).toISOString() : null;
      const result = await api<{ id: string; url: string }>("/shares", { method: "POST", body: JSON.stringify({ resourceType: shareTarget.kind, resourceId: shareTarget.id, password: options.password, expiresAt, allowDownload: options.allowDownload }) });
      setCreatedShareURL(result.url);
      setShareTarget(null);
      try {
        const updated = await api<{ items: ShareItem[] }>("/shares");
        setShares(updated.items);
      } catch {
        setShares((items) => [...items, { id: result.id, resourceType: shareTarget.kind, resourceId: shareTarget.id, resourceName: shareTarget.name, hasPassword: Boolean(options.password), allowDownload: options.allowDownload, expiresAt: expiresAt || undefined, createdAt: new Date().toISOString(), creatorName: currentUser?.displayName, own: true }]);
      }
      try {
        await navigator.clipboard.writeText(result.url);
        showToast("分享链接已创建并复制");
      } catch {
        showToast("分享链接已创建，请在窗口中手动复制");
      }
    } catch (error) { showToast(error instanceof Error ? error.message : "创建分享失败"); }
  };

  const copyCreatedShare = async () => {
    try {
      await navigator.clipboard.writeText(createdShareURL);
      showToast("分享链接已复制");
    } catch {
      showToast("无法访问剪贴板，请选中链接手动复制");
    }
  };

  const copyActionLink = async () => {
    if (!createdActionLink) return;
    try {
      await navigator.clipboard.writeText(createdActionLink.url);
      showToast("链接已复制");
    } catch {
      showToast("无法访问剪贴板，请选中链接手动复制");
    }
  };

  const createInvitationLink = async (role: "member" | "admin") => {
    if (status === "preview") {
      setCreatedActionLink({ title: "邀请链接已创建", description: "链接 7 天内有效且只能使用一次。", url: "https://cloud.example/invite/preview-link" });
      setDialog(null);
      return;
    }
    try {
      const result = await api<{ url: string }>("/invitations", { method: "POST", body: JSON.stringify({ role }) });
      setCreatedActionLink({ title: "邀请链接已创建", description: `新成员将以${role === "admin" ? "家庭管理员" : "家庭成员"}身份加入；链接 7 天内有效且只能使用一次。`, url: result.url });
      setDialog(null);
      try {
        await navigator.clipboard.writeText(result.url);
        showToast("邀请链接已复制");
      } catch {
        showToast("邀请已创建，请在窗口中手动复制链接");
      }
    } catch (error) {
      showToast(error instanceof Error ? error.message : "邀请失败");
    }
  };

  const revokeShare = async (id: string) => {
    if (status === "preview") { setShares((items) => items.filter((item) => item.id !== id)); showToast("分享已撤销"); return; }
    try {
      await api(`/shares/${id}`, { method: "DELETE" });
      const updated = await api<{ items: ShareItem[] }>("/shares");
      setShares(updated.items);
      showToast("分享已撤销");
    }
    catch (error) { showToast(error instanceof Error ? error.message : "撤销失败"); }
  };

  const restoreTrashNode = async (id: string) => {
    try {
      await api(`/trash/${id}/restore`, { method: "POST" });
      await refreshView();
      showToast("文件已恢复");
    } catch (error) { showToast(error instanceof Error ? error.message : "恢复失败"); }
  };

  const purgeTrashNode = async (id: string) => {
    if (!window.confirm("永久删除后无法恢复，确定继续吗？")) return;
    try {
      await api(`/trash/${id}`, { method: "DELETE" });
      setTrashNodes((items) => items.filter((item) => item.id !== id));
      showToast("已提交永久删除");
    } catch (error) { showToast(error instanceof Error ? error.message : "删除失败"); }
  };

  const emptyTrash = async () => {
    if (!trashNodes.length || !window.confirm(`确定永久删除回收站中的 ${trashNodes.length} 个项目吗？`)) return;
    try {
      await Promise.all(trashNodes.map((item) => api(`/trash/${item.id}`, { method: "DELETE" })));
      setTrashNodes([]);
      showToast("回收站已清空");
    } catch (error) { showToast(error instanceof Error ? error.message : "清空失败"); }
  };

  const createMemberPasswordReset = async (member: Member) => {
    try {
      const result = await api<{ url: string }>(`/members/${member.id}/password-reset`, { method: "POST" });
      setCreatedActionLink({ title: "密码重置链接已创建", description: `${member.displayName} 使用此链接后，旧会话和旧重置链接都会立即失效。链接 1 小时内有效。`, url: result.url });
      try {
        await navigator.clipboard.writeText(result.url);
        showToast(`已复制 ${member.displayName} 的密码重置链接`);
      } catch {
        showToast("重置链接已创建，请在窗口中手动复制");
      }
    } catch (error) { showToast(error instanceof Error ? error.message : "生成重置链接失败"); }
  };

  const updateMemberRole = async (member: Member, role: "admin" | "member") => {
    try {
      await api(`/members/${member.id}`, { method: "PATCH", body: JSON.stringify({ role }) });
      setMembers((items) => items.map((item) => item.id === member.id ? { ...item, role } : item));
      showToast(`${member.displayName} 已设为${role === "admin" ? "管理员" : "家庭成员"}`);
    } catch (error) { showToast(error instanceof Error ? error.message : "角色更新失败"); }
  };

  const updateSpaceQuota = async (space: Space, quotaBytes: number) => {
    try {
      await api(`/spaces/${space.id}/quota`, { method: "PATCH", body: JSON.stringify({ quotaBytes }) });
      setSpaces((items) => items.map((item) => item.id === space.id ? { ...item, quotaBytes } : item));
      setQuotaEditor(null);
      showToast(quotaBytes ? "空间配额已更新" : "空间配额已设为不限额");
    } catch (error) { showToast(error instanceof Error ? error.message : "配额更新失败"); }
  };

  if (status === "loading") return <LoadingScreen />;
  if (status === "setup") return <SetupScreen values={setupForm} onChange={setSetupForm} onSubmit={submitSetup} toast={toast} />;
  if (status === "login") return <LoginScreen values={loginForm} onChange={setLoginForm} onSubmit={submitLogin} toast={toast} />;

  const filteredNodes = nodes.filter((item) => item.name.toLowerCase().includes(search.toLowerCase()));
  const filteredPhotos = photos.filter((item) => `${item.name} ${item.remark}`.toLowerCase().includes(search.toLowerCase()));
  const filteredAlbums = albums.filter((item) => `${item.name} ${item.description}`.toLowerCase().includes(search.toLowerCase()));
  const activeShareCount = shares.filter((share) => !share.revokedAt && (!share.expiresAt || new Date(share.expiresAt).getTime() > Date.now())).length;
  const userInitial = currentUser?.displayName.trim().slice(0, 1) || "云";
  const roleLabel = currentUser?.role === "owner" ? "家庭所有者" : currentUser?.role === "admin" ? "家庭管理员" : "家庭成员";
  const usedPercent = selectedSpace?.quotaBytes ? Math.min(100, ((selectedSpace.usedBytes + selectedSpace.reservedBytes) / selectedSpace.quotaBytes) * 100) : 0;

  return (
    <div className="drive-shell">
      <aside className={`sidebar ${menuOpen ? "sidebar-open" : ""}`}>
        <div className="brand"><span className="brand-mark"><Cloud size={21} strokeWidth={2.4} /></span><span>栖云</span><button className="mobile-close" onClick={() => { setMenuOpen(false); setAccountOpen(false); setSpaceOpen(false); }} aria-label="关闭菜单"><X size={20} /></button></div>
        <div className="space-switcher-wrap">
          <button className="space-switcher" onClick={() => setSpaceOpen(!spaceOpen)}><span className={`space-avatar ${selectedSpace?.kind}`}><Home size={16} /></span><span><small>{selectedSpace?.kind === "family" ? "家庭空间" : "私有空间"}</small><strong>{selectedSpace?.name}</strong></span><ChevronDown size={16} /></button>
          {spaceOpen && <div className="space-menu">{spaces.map((space) => <button key={space.id} onClick={() => { setSpaceId(space.id); setSpaceOpen(false); setMenuOpen(false); setAccountOpen(false); setCurrentParent(null); setBreadcrumbs([{ id: null, name: space.name }]); }}><span className={`space-dot ${space.kind}`} /> <span>{space.name}<small>{space.kind === "personal" ? "仅你可见" : "家庭成员共享"}</small></span>{space.id === spaceId && <span className="selected-check">✓</span>}</button>)}</div>}
        </div>
        <nav className="side-nav">{navItems.map((item) => <button key={item.id} className={view === item.id ? "active" : ""} onClick={() => changeView(item.id)}><item.icon size={19} /><span>{item.label}</span>{item.id === "shares" && activeShareCount > 0 && <span className="nav-count">{activeShareCount}</span>}</button>)}</nav>
        <div className="nav-section-label">家庭</div>
        <nav className="side-nav"><button className={view === "family" ? "active" : ""} onClick={() => changeView("family")}><Users size={19} /><span>成员与权限</span></button></nav>
        <div className="storage-card"><div className="storage-head"><span><HardDrive size={16} /> 存储空间</span><strong>{Math.round(usedPercent)}%</strong></div><div className="storage-track"><span style={{ width: `${usedPercent}%` }} /></div><p>已用 {formatBytes(selectedSpace?.usedBytes || 0)}<br />共 {selectedSpace?.quotaBytes ? formatBytes(selectedSpace.quotaBytes) : "不限额"}</p></div>
        <div className="profile-wrap"><button className="profile-row" onClick={() => setAccountOpen((value) => !value)}><span className="profile-avatar">{userInitial}</span><span><strong>{currentUser?.displayName || "访客"}</strong><small>{status === "preview" ? "界面预览" : roleLabel}</small></span><Settings size={17} /></button>{accountOpen && <div className="account-menu"><div><strong>{currentUser?.displayName}</strong><small>@{currentUser?.username}</small></div><button onClick={() => void openAccountSettings()}><UserRound size={15} /> 账户设置</button><button onClick={logout}><LogOut size={15} /> 退出登录</button></div>}</div>
      </aside>

      <main className="main-panel">
        <header className="topbar"><button className="menu-button" onClick={() => setMenuOpen(true)} aria-label="打开菜单"><Menu size={21} /></button><div className="search-box"><Search size={18} /><input ref={searchInput} value={search} onChange={(event) => setSearch(event.target.value)} placeholder="搜索当前页面" aria-label="搜索" /><kbd>Ctrl K</kbd></div><div className="notification-wrap"><button className="icon-button" onClick={() => setNotificationsOpen((value) => !value)} aria-label="通知"><Bell size={19} />{uploads.some((item) => item.state === "failed" || item.state === "uploading") && <span className="notification-dot" />}</button>{notificationsOpen && <div className="notification-menu"><strong>最近上传</strong>{uploads.length ? uploads.slice(0, 5).map((item) => <span key={item.id}><File size={14} /><span>{item.name}<small>{item.state === "ready" ? "上传完成" : item.state === "failed" ? item.error || "上传失败" : item.state === "paused" ? "已暂停" : "上传中"}</small></span></span>) : <p>暂无通知</p>}</div>}</div><button className="avatar-button" onClick={() => { setMenuOpen(true); setAccountOpen(true); }} aria-label="账户菜单">{userInitial}</button></header>
        <div className="content">
          {status === "preview" && <div className="preview-banner"><Sparkles size={16} /><span>当前是界面预览。启动整套服务后，文件与照片会安全存入你的 RustFS。</span><button onClick={() => setStatus("setup")}>体验初始化</button></div>}
          <div className="page-heading"><div><p>{viewMeta[view].kicker}</p><h1>{viewMeta[view].title}</h1></div><div className="heading-actions">{view === "files" && <><button className="secondary-button" onClick={() => setDialog("folder")} aria-label="新建文件夹"><Plus size={17} /> 新建文件夹</button><button className="secondary-button folder-upload-button" onClick={() => folderInput.current?.click()} aria-label="上传文件夹"><UploadCloud size={17} /> 上传文件夹</button><button className="primary-button" onClick={() => fileInput.current?.click()} aria-label="上传文件"><UploadCloud size={18} /> 上传文件</button></>}{view === "photos" && <button className="primary-button" onClick={() => photoInput.current?.click()} aria-label="上传照片"><UploadCloud size={18} /> 上传照片</button>}{view === "albums" && <button className="primary-button" onClick={() => setDialog("album")}><Plus size={18} /> 新建相册</button>}{view === "family" && <button className="primary-button" onClick={() => setDialog("invite")}><UserPlus size={18} /> 邀请成员</button>}</div></div>
          <input ref={fileInput} type="file" multiple hidden onChange={(event) => { void handleFiles(Array.from(event.target.files || [])); event.currentTarget.value = ""; }} />
          <input ref={(node) => { folderInput.current = node; if (node) node.setAttribute("webkitdirectory", ""); }} type="file" multiple hidden onChange={(event) => { void handleFiles(Array.from(event.target.files || [])); event.currentTarget.value = ""; }} />
          <input ref={photoInput} type="file" multiple accept="image/jpeg,image/png,image/webp,image/gif,image/heic,image/heif,.heic,.heif" hidden onChange={(event) => { void handlePhotos(Array.from(event.target.files || [])); event.currentTarget.value = ""; }} />
          <input ref={albumInput} type="file" multiple accept="image/jpeg,image/png,image/webp,image/gif,image/heic,image/heif,.heic,.heif" hidden onChange={(event) => { void handleAlbumPhotos(Array.from(event.target.files || [])); event.currentTarget.value = ""; }} />
          {view === "files" && <FileView items={filteredNodes} grid={grid} onGrid={setGrid} breadcrumbs={breadcrumbs} onBreadcrumb={goBreadcrumb} onOpen={(item) => void openNode(item)} onDownload={downloadNode} onRename={setNodeEditor} onMove={(items) => void openMoveEditor(items)} onTrash={trashNode} onBatchTrash={(items) => void trashNodeBatch(items)} onPermissions={(item) => void openPermissionEditor("node", item.id, item.name)} canManagePermissions={selectedSpace?.kind === "family" && selectedSpace.permission === "manager"} onShare={(item) => setShareTarget({ id: item.id, kind: item.kind, name: item.name })} onUploadFolder={() => folderInput.current?.click()} />}
          {view === "photos" && <PhotoView photos={filteredPhotos} onOpen={(photo) => setPhotoViewer({ photo })} />}
          {view === "albums" && (selectedAlbum ? <AlbumDetail album={selectedAlbum} photos={albumPhotos.filter((item) => `${item.name} ${item.remark}`.toLowerCase().includes(search.toLowerCase()))} onBack={() => { setSelectedAlbum(null); setAlbumPhotos([]); }} onUpload={chooseAlbumPhotos} onOpen={(photo) => setPhotoViewer({ photo, albumId: selectedAlbum.id })} onEdit={setAlbumEditor} onPermissions={selectedSpace?.kind === "family" && selectedAlbum.permission === "manager" ? (album) => void openPermissionEditor("album", album.id, album.name) : undefined} onShare={(album) => setShareTarget({ id: album.id, kind: "album", name: album.name })} onDelete={deleteAlbum} /> : <AlbumView albums={filteredAlbums} onOpen={openAlbum} onUpload={chooseAlbumPhotos} onShare={(album) => setShareTarget({ id: album.id, kind: "album", name: album.name })} />)}
          {view === "shares" && <ShareView shares={status === "preview" ? undefined : shares} onRevoke={revokeShare} />}
          {view === "trash" && <TrashView nodes={status === "preview" ? previewNodes.slice(3, 5).map((item) => ({ ...item, deletedAt: new Date(Date.now() - 3 * 86400000).toISOString(), purgeAt: new Date(Date.now() + 27 * 86400000).toISOString() })) : trashNodes} onRestore={restoreTrashNode} onPurge={purgeTrashNode} onEmpty={emptyTrash} />}
          {view === "family" && <FamilyView members={members} auditItems={auditItems} space={spaces.find((item) => item.kind === "family") || selectedSpace} canManage={currentUser?.role === "owner" || currentUser?.role === "admin"} canEditRoles={currentUser?.role === "owner"} canEditQuota={currentUser?.role === "owner"} canResetMember={(member) => currentUser?.role === "owner" || member.role === "member"} onResetPassword={createMemberPasswordReset} onRoleChange={updateMemberRole} onPermissionGuide={() => setPermissionGuideOpen(true)} onEditQuota={(space) => setQuotaEditor(space)} />}
        </div>
      </main>
      <div className="mobile-nav">{navItems.slice(0, 4).map((item) => <button key={item.id} className={view === item.id ? "active" : ""} onClick={() => changeView(item.id)}><item.icon size={20} /><span>{item.label}</span></button>)}</div>
      {uploads.length > 0 && <UploadTray tasks={uploads} onClose={() => setUploads((items) => items.filter((item) => item.state !== "ready"))} onPause={(id) => uploadControllers.current.get(id)?.abort()} onResume={resumeTask} />}
      {dialog && <NameDialog type={dialog} canInviteAdmin={currentUser?.role === "owner"} onClose={() => setDialog(null)} onSubmit={createNamedItem} onInvite={createInvitationLink} />}
      {shareTarget && <ShareDialog target={shareTarget} onClose={() => setShareTarget(null)} onSubmit={createShareLink} />}
      {createdShareURL && <ShareCreatedDialog url={createdShareURL} onClose={() => setCreatedShareURL("")} onCopy={copyCreatedShare} />}
      {createdActionLink && <ActionLinkDialog value={createdActionLink} onClose={() => setCreatedActionLink(null)} onCopy={copyActionLink} />}
      {photoViewer && <PhotoViewer photo={photoViewer.photo} onClose={() => setPhotoViewer(null)} onSave={(remark) => savePhotoRemark(photoViewer.photo, remark)} onDownload={() => downloadPhoto(photoViewer.photo)} onRemove={photoViewer.albumId ? () => removePhotoFromAlbum(photoViewer.albumId!, photoViewer.photo.nodeId) : undefined} />}
      {filePreview && <FilePreviewDialog value={filePreview} onClose={() => setFilePreview(null)} onDownload={() => downloadNode(filePreview.item)} />}
      {albumEditor && <AlbumEditDialog album={albumEditor} onClose={() => setAlbumEditor(null)} onSubmit={(values) => updateAlbum(albumEditor, values)} />}
      {nodeEditor && <RenameDialog item={nodeEditor} onClose={() => setNodeEditor(null)} onSubmit={(name) => renameNode(nodeEditor, name)} />}
      {moveEditor && <MoveDialog items={moveEditor.items} folders={moveEditor.folders} rootName={selectedSpace?.name || "空间根目录"} onClose={() => setMoveEditor(null)} onSubmit={(parentId) => moveNodes(moveEditor.items, parentId)} />}
      {permissionEditor && <PermissionDialog value={permissionEditor} members={members} currentUserId={currentUser?.id} onClose={() => setPermissionEditor(null)} onSubmit={savePermissions} />}
      {accountDialogOpen && currentUser && <AccountDialog user={currentUser} sessions={sessions} sessionsLoading={sessionsLoading} onClose={() => setAccountDialogOpen(false)} onProfile={updateProfile} onPassword={changeOwnPassword} onRevokeSession={revokeOwnSession} />}
      {permissionGuideOpen && <PermissionGuideDialog onClose={() => setPermissionGuideOpen(false)} />}
      {quotaEditor && <QuotaDialog space={quotaEditor} onClose={() => setQuotaEditor(null)} onSubmit={(quotaBytes) => updateSpaceQuota(quotaEditor, quotaBytes)} />}
      {toast && <div className="toast" role="status" aria-live="polite"><ShieldCheck size={18} />{toast}</div>}
    </div>
  );
}

function FileView({ items, grid, onGrid, breadcrumbs, onBreadcrumb, onOpen, onDownload, onRename, onMove, onTrash, onBatchTrash, onPermissions, canManagePermissions, onShare, onUploadFolder }: { items: NodeItem[]; grid: boolean; onGrid: (value: boolean) => void; breadcrumbs: { id: string | null; name: string }[]; onBreadcrumb: (index: number) => void; onOpen: (item: NodeItem) => void; onDownload: (item: NodeItem) => void; onRename: (item: NodeItem) => void; onMove: (items: NodeItem[]) => void; onTrash: (item: NodeItem) => void; onBatchTrash: (items: NodeItem[]) => void; onPermissions: (item: NodeItem) => void; canManagePermissions: boolean; onShare: (item: NodeItem) => void; onUploadFolder: () => void }) {
  const [selected, setSelected] = useState<string[]>([]);
  const [sortKey, setSortKey] = useState<"name" | "size" | "updated">("name");
  const [sortDirection, setSortDirection] = useState<"asc" | "desc">("asc");
  const orderedItems = useMemo(() => [...items].sort((left, right) => {
    if (left.kind !== right.kind) return left.kind === "folder" ? -1 : 1;
    let compared = 0;
    if (sortKey === "size") compared = left.sizeBytes - right.sizeBytes;
    else if (sortKey === "updated") compared = new Date(left.updatedAt).getTime() - new Date(right.updatedAt).getTime();
    else compared = left.name.localeCompare(right.name, "zh-CN", { numeric: true, sensitivity: "base" });
    return sortDirection === "asc" ? compared : -compared;
  }), [items, sortDirection, sortKey]);
  const changeSort = (next: "name" | "size" | "updated") => {
    if (sortKey === next) setSortDirection((current) => current === "asc" ? "desc" : "asc");
    else { setSortKey(next); setSortDirection(next === "name" ? "asc" : "desc"); }
  };
  const visibleSelected = orderedItems.filter((item) => selected.includes(item.id));
  const toggleSelected = (id: string) => setSelected((current) => current.includes(id) ? current.filter((item) => item !== id) : [...current, id]);
  const allSelected = orderedItems.length > 0 && orderedItems.every((item) => selected.includes(item.id));
  return <section className="surface-card file-surface">
    <div className="file-toolbar"><div className="breadcrumbs">{breadcrumbs.map((item, index) => <span key={`${item.id}-${index}`}><button onClick={() => { setSelected([]); onBreadcrumb(index); }}>{item.name}</button>{index < breadcrumbs.length - 1 && <ChevronRight size={14} />}</span>)}</div><div className="view-toggle"><label className="sort-picker"><span>排序</span><select value={sortKey} onChange={(event) => { const next = event.target.value as "name" | "size" | "updated"; setSortKey(next); setSortDirection(next === "name" ? "asc" : "desc"); }} aria-label="文件排序方式"><option value="name">名称</option><option value="size">大小</option><option value="updated">更新时间</option></select></label><button onClick={() => changeSort(sortKey)} title={`当前按${sortKey === "name" ? "名称" : sortKey === "size" ? "大小" : "更新时间"}${sortDirection === "asc" ? "升序" : "降序"}`} aria-label="切换排序方向"><ArrowUpDown size={16} /></button><button onClick={() => setSelected(allSelected ? [] : orderedItems.map((item) => item.id))} title={allSelected ? "取消全选" : "全选"}>{allSelected ? <CheckSquare size={16} /> : <Square size={16} />}</button><button onClick={onUploadFolder} title="上传文件夹"><UploadCloud size={16} /></button><button className={!grid ? "active" : ""} onClick={() => onGrid(false)} aria-label="列表视图"><List size={17} /></button><button className={grid ? "active" : ""} onClick={() => onGrid(true)} aria-label="网格视图"><Grid2X2 size={16} /></button></div></div>
    {visibleSelected.length > 0 && <div className="selection-toolbar"><strong>已选 {visibleSelected.length} 项</strong><button onClick={() => onMove(visibleSelected)}><FolderInput size={15} /> 移动</button><button className="danger-text" onClick={() => onBatchTrash(visibleSelected)}><Trash2 size={15} /> 移到回收站</button><button onClick={() => setSelected([])}>取消选择</button></div>}
    {grid ? <div className="file-grid">{orderedItems.map((item) => <article className={`file-tile ${selected.includes(item.id) ? "selected" : ""}`} key={item.id}><button className="tile-select" onClick={() => toggleSelected(item.id)} aria-label={`${selected.includes(item.id) ? "取消选择" : "选择"} ${item.name}`}>{selected.includes(item.id) ? <CheckSquare size={17} /> : <Square size={17} />}</button><button className="file-tile-main" onClick={() => onOpen(item)}><span className={`file-icon large ${item.kind}`}>{fileIcon(item)}</span><strong>{item.name}</strong><small>{item.kind === "folder" ? "文件夹" : formatBytes(item.sizeBytes)} · {relativeDate(item.updatedAt)}</small></button><span className="tile-actions">{item.kind === "file" && <button onClick={() => onOpen(item)} aria-label={`预览 ${item.name}`}><Eye size={15} /></button>}<button onClick={() => onDownload(item)} aria-label={`下载 ${item.name}`}><Download size={15} /></button><button onClick={() => onMove([item])} aria-label={`移动 ${item.name}`}><FolderInput size={15} /></button><button onClick={() => onRename(item)} aria-label={`重命名 ${item.name}`}><Edit3 size={15} /></button>{canManagePermissions && item.permission === "manager" && <button onClick={() => onPermissions(item)} aria-label={`设置 ${item.name} 权限`}><ShieldCheck size={15} /></button>}<button onClick={() => onShare(item)} aria-label={`分享 ${item.name}`}><Share2 size={15} /></button><button onClick={() => onTrash(item)} aria-label={`删除 ${item.name}`}><Trash2 size={15} /></button></span></article>)}</div> : <div className="file-list"><div className="file-row table-head"><button className={sortKey === "name" ? "active" : ""} onClick={() => changeSort("name")}>名称 <ArrowUpDown size={13} /></button><button className={sortKey === "size" ? "active" : ""} onClick={() => changeSort("size")}>大小 <ArrowUpDown size={13} /></button><button className={sortKey === "updated" ? "active" : ""} onClick={() => changeSort("updated")}>更新时间 <ArrowUpDown size={13} /></button><span /></div>{orderedItems.map((item) => <div className={`file-row ${selected.includes(item.id) ? "selected" : ""}`} key={item.id}><button className="file-name" onClick={() => onOpen(item)}><span className={`file-icon ${item.kind}`}>{fileIcon(item)}</span><span><strong>{item.name}</strong><small>{item.kind === "folder" ? "文件夹" : item.mimeType?.split("/")[1]?.toUpperCase()}</small></span></button><span>{item.kind === "folder" ? "—" : formatBytes(item.sizeBytes)}</span><span>{relativeDate(item.updatedAt)}</span><span className="row-actions"><button onClick={() => toggleSelected(item.id)} aria-label={`${selected.includes(item.id) ? "取消选择" : "选择"} ${item.name}`}>{selected.includes(item.id) ? <CheckSquare size={17} /> : <Square size={17} />}</button>{item.kind === "file" && <button onClick={() => onOpen(item)} aria-label={`预览 ${item.name}`}><Eye size={16} /></button>}<button onClick={() => onDownload(item)} aria-label={`下载 ${item.name}`}><Download size={17} /></button><button onClick={() => onMove([item])} aria-label={`移动 ${item.name}`}><FolderInput size={16} /></button><button onClick={() => onRename(item)} aria-label={`重命名 ${item.name}`}><Edit3 size={16} /></button>{canManagePermissions && item.permission === "manager" && <button onClick={() => onPermissions(item)} aria-label={`设置 ${item.name} 权限`}><ShieldCheck size={16} /></button>}<button onClick={() => onShare(item)} aria-label={`分享 ${item.name}`}><Share2 size={17} /></button><button onClick={() => onTrash(item)} aria-label={`删除 ${item.name}`}><Trash2 size={16} /></button></span></div>)}{!orderedItems.length && <EmptyState icon={FolderOpen} title="这里还没有文件" text="上传文件或整个文件夹，开始整理你的空间" />}</div>}
  </section>;
}

function PhotoView({ photos, onOpen }: { photos: PhotoItem[]; onOpen: (photo: PhotoItem) => void }) {
  const groups = useMemo(() => {
    const result = new Map<string, { label: string; items: PhotoItem[] }>();
    [...photos].sort((left, right) => new Date(right.takenAt || right.createdAt).getTime() - new Date(left.takenAt || left.createdAt).getTime()).forEach((photo) => {
      const date = new Date(photo.takenAt || photo.createdAt);
      const key = Number.isNaN(date.getTime()) ? "unknown" : `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
      const label = Number.isNaN(date.getTime()) ? "未知日期" : new Intl.DateTimeFormat("zh-CN", { year: "numeric", month: "long", day: "numeric" }).format(date);
      const group = result.get(key) || { label, items: [] };
      group.items.push(photo);
      result.set(key, group);
    });
    return Array.from(result.entries());
  }, [photos]);
  return <div className="photo-view"><div className="photo-summary"><span className="summary-icon"><Camera size={21} /></span><span><strong>{photos.length || 0} 张照片</strong><small>来自照片页与相册，按拍摄时间自动整理</small></span><span className="timeline-label"><Clock3 size={17} /> 时间线</span></div>{groups.map(([key, group]) => <section className="photo-group" key={key}><div className="photo-date"><h2>{group.label}</h2><span>{group.items.length} 张</span></div><div className="photo-grid">{group.items.map((photo, index) => <button type="button" className={`photo-card photo-${photo.tone || "default"} ${index % 5 === 0 ? "tall" : ""}`} key={photo.nodeId} onClick={() => onOpen(photo)}>{photo.thumbUrl ? <img src={photo.thumbUrl} alt={photo.remark || photo.name} loading="lazy" decoding="async" /> : <div className="photo-art"><span>{photo.tone === "mountain" ? "山野" : photo.tone === "sunset" ? "日落" : photo.tone === "forest" ? "林间" : photo.tone === "city" ? "夜色" : photo.tone === "flower" ? "花期" : "日常"}</span></div>}<div className="photo-overlay"><strong>{photo.remark || photo.name}</strong><small>{photo.name}</small></div></button>)}</div></section>)}{!photos.length && <EmptyState icon={Images} title="还没有照片" text="从照片页或相册上传后，这里会按拍摄日期自动生成时间线" />}</div>;
}

function AlbumView({ albums, onOpen, onUpload, onShare }: { albums: Album[]; onOpen: (album: Album) => void; onUpload: (album: Album) => void; onShare: (album: Album) => void }) {
  return <div className="album-grid">{albums.map((album, index) => <article className="album-card" key={album.id}><button type="button" className={`album-cover cover-${index % 3}`} onClick={() => onOpen(album)} aria-label={`打开相册 ${album.name}`}>{album.coverUrl ? <img src={album.coverUrl} alt="" loading="lazy" decoding="async" /> : <><div className="cover-stack one" /><div className="cover-stack two" /><span><ImageIcon size={28} /></span></>}</button><div className="album-copy"><button type="button" className="album-title" onClick={() => onOpen(album)}><h3>{album.name}</h3></button><p>{album.description || "从这里上传照片，它们也会出现在照片时间线中"}</p><div><span>{album.itemCount} 张照片</span><span className="album-actions"><button className="album-upload" onClick={() => onUpload(album)}><UploadCloud size={15} /> 上传照片</button><button onClick={() => onShare(album)} aria-label="分享相册"><Share2 size={17} /></button></span></div></div></article>)}{!albums.length && <EmptyState icon={Images} title="还没有相册" text="新建相册，把同一段故事里的照片放在一起" />}</div>;
}

function AlbumDetail({ album, photos, onBack, onUpload, onOpen, onEdit, onPermissions, onShare, onDelete }: { album: Album; photos: PhotoItem[]; onBack: () => void; onUpload: (album: Album) => void; onOpen: (photo: PhotoItem) => void; onEdit: (album: Album) => void; onPermissions?: (album: Album) => void; onShare: (album: Album) => void; onDelete: (album: Album) => void }) {
  return <section className="album-detail"><header className="album-detail-head"><button className="back-button" onClick={onBack}><ArrowLeft size={17} /> 返回相册</button><div><small>相册 · {photos.length} 张</small><h2>{album.name}</h2><p>{album.description || "还没有相册描述"}</p></div><span className="album-detail-actions"><button onClick={() => onEdit(album)}><Edit3 size={16} /> 编辑</button>{onPermissions && <button onClick={() => onPermissions(album)}><ShieldCheck size={16} /> 权限</button>}<button onClick={() => onShare(album)}><Share2 size={16} /> 分享</button><button className="primary-button" onClick={() => onUpload(album)}><UploadCloud size={17} /> 上传照片</button><button className="danger-icon" onClick={() => onDelete(album)} aria-label="删除相册"><Trash2 size={17} /></button></span></header>{photos.length ? <div className="album-photo-grid">{photos.map((photo) => <button type="button" key={photo.nodeId} onClick={() => onOpen(photo)}><img src={photo.thumbUrl || photo.previewUrl} alt={photo.remark || photo.name} loading="lazy" decoding="async" /><span><strong>{photo.remark || photo.name}</strong><small>{relativeDate(photo.takenAt || photo.createdAt)}</small></span></button>)}</div> : <EmptyState icon={ImageIcon} title="相册还是空的" text="点击“上传照片”，照片会加入相册并同步到时间线" />}</section>;
}

function ShareView({ shares: actualShares, onRevoke }: { shares?: ShareItem[]; onRevoke: (id: string) => void }) {
  const preview = [{ id: "preview-1", resourceName: "川西 · 2026", resourceType: "album", expiresAt: "2026-08-16T12:00:00Z", hasPassword: true, revokedAt: undefined }, { id: "preview-2", resourceName: "2026 家庭旅行计划.pdf", resourceType: "file", expiresAt: undefined, hasPassword: true, revokedAt: undefined }, { id: "preview-3", resourceName: "家庭资料", resourceType: "folder", expiresAt: "2026-08-12T12:00:00Z", hasPassword: false, revokedAt: undefined }];
  const items = actualShares || preview;
  const [tab, setTab] = useState<"active" | "history">("active");
  const [referenceTime] = useState(() => Date.now());
  const active = items.filter((share) => !share.revokedAt && (!share.expiresAt || new Date(share.expiresAt).getTime() > referenceTime));
  const history = items.filter((share) => !active.includes(share));
  const shown = tab === "active" ? active : history;
  return <section className="surface-card share-list"><div className="share-tabs"><button className={tab === "active" ? "active" : ""} onClick={() => setTab("active")}>有效分享 <span>{active.length}</span></button><button className={tab === "history" ? "active" : ""} onClick={() => setTab("history")}>历史记录 <span>{history.length}</span></button></div><div className="share-header"><span>分享内容</span><span>访问设置</span><span>有效期</span><span /></div>{shown.map((share) => <div className="share-row" key={share.id}><span className="share-name"><span className="share-icon"><Link2 size={18} /></span><span><strong>{share.resourceName}</strong><small>{share.resourceType === "album" ? "相册" : share.resourceType === "folder" ? "文件夹" : "文件"}{share.own === false && share.creatorName ? ` · ${share.creatorName} 创建` : ""}</small></span></span><span>{share.hasPassword ? <><ShieldCheck size={15} /> 密码保护</> : "无需密码"}</span><span>{share.revokedAt ? "已撤销" : share.expiresAt && new Date(share.expiresAt).getTime() <= referenceTime ? "已过期" : share.expiresAt ? relativeDate(share.expiresAt) + " 到期" : "长期有效"}</span><span>{tab === "active" ? <button className="text-button" onClick={() => onRevoke(share.id)}>撤销</button> : <span className="share-status">已失效</span>}</span></div>)}{!shown.length && <EmptyState icon={Link2} title={tab === "active" ? "还没有有效分享" : "暂无分享历史"} text={tab === "active" ? "从文件或相册中创建一条受密码和有效期保护的链接" : "撤销或过期的分享会保留在这里便于审计"} />}</section>;
}

function TrashView({ nodes, onRestore, onPurge, onEmpty }: { nodes: TrashItem[]; onRestore: (id: string) => void; onPurge: (id: string) => void; onEmpty: () => void }) {
  return <section className="surface-card"><div className="trash-note"><Archive size={19} /><span>回收站中的内容仍会占用存储空间</span><button disabled={!nodes.length} onClick={onEmpty}>清空回收站</button></div><div className="file-list">{nodes.map((item) => <div className="file-row" key={item.id}><div className="file-name"><span className="file-icon muted">{fileIcon(item)}</span><span><strong>{item.name}</strong><small>将在 {relativeDate(item.purgeAt)} 永久删除</small></span></div><span>{formatBytes(item.sizeBytes)}</span><span>{relativeDate(item.deletedAt)} 删除</span><span className="row-actions"><button className="restore-button" onClick={() => onRestore(item.id)}>恢复</button><button aria-label="永久删除" onClick={() => onPurge(item.id)}><Trash2 size={17} /></button></span></div>)}{!nodes.length && <EmptyState icon={Trash2} title="回收站是空的" text="删除的文件会在这里保留 30 天" />}</div></section>;
}

function auditAction(item: AuditItem) {
  let metadata: Record<string, unknown> = {};
  try {
    const parsed = JSON.parse(item.metadata || "{}");
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) metadata = parsed;
  } catch { /* malformed historic metadata is shown without details */ }
  const name = typeof metadata.name === "string" ? `「${metadata.name}」` : "内容";
  const labels: Record<string, string> = {
    "node.rename": `重命名了${name}`,
    "node.folder_create": `创建了文件夹${name}`,
    "node.upload": `上传了${name}`,
    "node.move": `移动了${name}`,
    "node.trash": "将内容移到回收站",
    "node.restore": "从回收站恢复了内容",
    "node.permissions_update": "更新了目录访问权限",
    "album.update": "更新了相册信息",
    "album.create": `创建了相册${name}`,
    "album.items_add": "向相册添加了照片",
    "album.item_remove": "从相册移除了照片",
    "album.delete": "删除了相册",
    "album.permissions_update": "更新了相册访问权限",
    "share.create": "创建了对外分享",
    "share.revoke": "撤销了对外分享",
    "invitation.create": "创建了家庭邀请",
    "member.password_reset_create": "创建了成员密码重置链接",
    "account.session_revoke": "退出了一个登录会话",
    "member.role_update": "调整了成员角色",
    "account.profile_update": "更新了个人资料",
    "account.password_update": "更新了登录密码",
    "photo.remark_update": "更新了照片备注",
  };
  return labels[item.action] || item.action.replaceAll(".", " · ");
}

function FamilyView({ members, auditItems, space, canManage, canEditRoles, canEditQuota, canResetMember, onResetPassword, onRoleChange, onPermissionGuide, onEditQuota }: { members: Member[]; auditItems: AuditItem[]; space?: Space; canManage: boolean; canEditRoles: boolean; canEditQuota: boolean; canResetMember: (member: Member) => boolean; onResetPassword: (member: Member) => void; onRoleChange: (member: Member, role: "admin" | "member") => void; onPermissionGuide: () => void; onEditQuota: (space: Space) => void }) {
  return <div className="family-layout"><div className="family-main"><section className="surface-card family-card"><div className="section-title"><div><h2>家庭成员</h2><p>管理员只能管理家庭空间，无法查看成员私有空间</p></div><span>{members.length} 人</span></div><div className="member-list">{members.map((member, index) => <div className="member-row" key={member.id}><span className={`member-avatar member-${index}`}>{member.displayName.slice(0, 1)}</span><span><strong>{member.displayName}{member.role === "owner" && <em>所有者</em>}</strong><small>@{member.username}</small></span>{canEditRoles && member.role !== "owner" ? <select className="member-role-select" value={member.role} onChange={(event) => onRoleChange(member, event.target.value as "admin" | "member")} aria-label={`设置 ${member.displayName} 的角色`}><option value="member">家庭成员</option><option value="admin">管理员</option></select> : <span className="member-role">{member.role === "owner" ? "完全管理" : member.role === "admin" ? "管理员" : "家庭成员"}</span>}{canManage && canResetMember(member) ? <button onClick={() => onResetPassword(member)} title="生成密码重置链接" aria-label={`重置 ${member.displayName} 的密码`}><Settings size={17} /></button> : <span />}</div>)}</div></section>{canManage && <section className="surface-card audit-card"><div className="section-title"><div><h2>最近活动</h2><p>家庭空间的重要操作会记录在这里</p></div><span>最近 {auditItems.length} 条</span></div><div className="audit-list">{auditItems.slice(0, 12).map((item) => <article key={item.id}><span className="audit-dot" /><span><strong>{item.actorName || "系统"} {auditAction(item)}</strong><small>{new Intl.DateTimeFormat("zh-CN", { dateStyle: "medium", timeStyle: "short" }).format(new Date(item.createdAt))}</small></span></article>)}{!auditItems.length && <p className="audit-empty">暂无活动记录</p>}</div></section>}</div><aside className="family-side"><section className="surface-card permission-card"><span className="permission-art"><ShieldCheck size={27} /></span><h3>隐私边界清晰可见</h3><p>个人文件默认只有本人能访问；放入家庭空间后，再按目录设置查看、编辑或管理权限。</p><button onClick={onPermissionGuide}>查看权限说明 <ChevronRight size={15} /></button></section><section className="surface-card quota-card"><div><span>家庭空间用量</span><strong>{formatBytes(space?.usedBytes || 0)}</strong>{canEditQuota && space && <button onClick={() => onEditQuota(space)}>设置配额</button>}</div><div className="quota-ring" style={{ "--quota": `${space?.quotaBytes ? (space.usedBytes / space.quotaBytes) * 360 : 90}deg` } as React.CSSProperties}><span>{space?.quotaBytes ? Math.round(space.usedBytes / space.quotaBytes * 100) : 0}%</span></div></section></aside></div>;
}

function UploadTray({ tasks, onClose, onPause, onResume }: { tasks: UploadTask[]; onClose: () => void; onPause: (id: string) => void; onResume: (id: string) => void }) {
  const done = tasks.filter((item) => item.state === "ready").length;
  return <aside className="upload-tray"><div className="upload-title"><span><UploadCloud size={18} /> 上传任务 <small>{done}/{tasks.length}</small></span><button onClick={onClose} disabled={!done} aria-label="清除已完成上传" title={done ? "清除已完成" : "上传完成后可清除"}><X size={17} /></button></div><div className="upload-items">{tasks.slice(0, 8).map((task) => <div className="upload-item" key={task.id}><span className="file-icon"><File size={16} /></span><span><strong>{task.name}</strong><small>{task.state === "ready" ? "上传完成" : task.state === "failed" ? task.error : task.state === "paused" ? "已暂停，可继续上传" : `${Math.round(task.progress * 100)}% · ${formatBytes(task.size)}`}</small><span className={`upload-progress ${task.state}`}><i style={{ width: `${task.progress * 100}%` }} /></span></span>{task.state === "uploading" && <button className="upload-control" onClick={() => onPause(task.id)} aria-label="暂停上传"><Pause size={15} /></button>}{(task.state === "paused" || task.state === "failed") && <button className="upload-control" onClick={() => onResume(task.id)} aria-label={task.state === "failed" ? "重试上传" : "继续上传"}><Play size={15} /></button>}</div>)}</div></aside>;
}

function PhotoViewer({ photo, onClose, onSave, onDownload, onRemove }: { photo: PhotoItem; onClose: () => void; onSave: (remark: string) => void; onDownload: () => void; onRemove?: () => void }) {
  const [remark, setRemark] = useState(photo.remark || "");
  return <div className="photo-viewer" role="dialog" aria-modal="true" aria-label={`查看照片 ${photo.name}`}><div className="photo-viewer-stage">{photo.previewUrl || photo.thumbUrl ? <img src={photo.previewUrl || photo.thumbUrl} alt={photo.remark || photo.name} /> : <div className="photo-viewer-fallback"><ImageIcon size={42} /> 暂无预览</div>}<button className="photo-viewer-close" onClick={onClose} aria-label="关闭照片"><X size={21} /></button></div><aside className="photo-inspector"><div><small>照片详情</small><h2>{photo.name}</h2><p>{new Intl.DateTimeFormat("zh-CN", { dateStyle: "long", timeStyle: "short" }).format(new Date(photo.takenAt || photo.createdAt))}</p>{(photo.width || photo.height || photo.camera) && <p>{photo.width && photo.height ? `${photo.width} × ${photo.height}` : ""}{photo.camera ? ` · ${photo.camera}` : ""}</p>}</div><label>照片备注<textarea value={remark} onChange={(event) => setRemark(event.target.value)} maxLength={2000} placeholder="写下这张照片背后的故事…" /></label><div className="photo-inspector-actions"><button onClick={onDownload}><Download size={16} /> 下载原图</button>{onRemove && <button className="danger-text" onClick={onRemove}><Trash2 size={16} /> 从相册移除</button>}<button className="primary-button" onClick={() => onSave(remark.trim())}><Save size={16} /> 保存备注</button></div></aside></div>;
}

function FilePreviewDialog({ value, onClose, onDownload }: { value: FilePreviewState; onClose: () => void; onDownload: () => void }) {
  const mime = value.mimeType.toLowerCase().split(";")[0];
  const content = mime.startsWith("image/")
    ? <img src={value.url} alt={value.item.name} />
    : mime.startsWith("video/")
      ? <video src={value.url} controls />
      : mime.startsWith("audio/")
        ? <div className="audio-preview"><File size={42} /><strong>{value.item.name}</strong><audio src={value.url} controls /></div>
        : <iframe src={value.url} title={`预览 ${value.item.name}`} />;
  return <div className="file-preview" role="dialog" aria-modal="true" aria-label={`预览文件 ${value.item.name}`}><header><span><Eye size={18} /><strong>{value.item.name}</strong><small>{value.mimeType}</small></span><span><button onClick={onDownload}><Download size={16} /> 下载</button><button onClick={onClose} aria-label="关闭文件预览"><X size={19} /></button></span></header><div className="file-preview-stage">{content}</div></div>;
}

function AlbumEditDialog({ album, onClose, onSubmit }: { album: Album; onClose: () => void; onSubmit: (values: { name: string; description: string }) => void }) {
  const [name, setName] = useState(album.name);
  const [description, setDescription] = useState(album.description || "");
  return <div className="modal-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}><form className="modal" onSubmit={(event) => { event.preventDefault(); if (name.trim()) onSubmit({ name: name.trim(), description: description.trim() }); }}><div className="modal-icon"><Edit3 size={22} /></div><h2>编辑相册</h2><p>名称和描述会同步显示在相册及分享页。</p><label className="modal-label">相册名称<input value={name} onChange={(event) => setName(event.target.value)} maxLength={255} /></label><label className="modal-label">相册描述<textarea value={description} onChange={(event) => setDescription(event.target.value)} maxLength={2000} placeholder="记录这一册照片的故事" /></label><div className="modal-actions"><button type="button" onClick={onClose}>取消</button><button type="submit" className="primary-button">保存</button></div></form></div>;
}

function RenameDialog({ item, onClose, onSubmit }: { item: NodeItem; onClose: () => void; onSubmit: (name: string) => void }) {
  const [name, setName] = useState(item.name);
  return <div className="modal-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}><form className="modal" onSubmit={(event) => { event.preventDefault(); if (name.trim()) onSubmit(name.trim()); }}><div className="modal-icon"><Edit3 size={22} /></div><h2>重命名{item.kind === "folder" ? "文件夹" : "文件"}</h2><p>名称修改后，原文件内容和分享权限不会改变。</p><label className="modal-label">新名称<input value={name} onChange={(event) => setName(event.target.value)} maxLength={255} /></label><div className="modal-actions"><button type="button" onClick={onClose}>取消</button><button type="submit" className="primary-button">保存</button></div></form></div>;
}

function MoveDialog({ items, folders, rootName, onClose, onSubmit }: { items: NodeItem[]; folders: FolderOption[]; rootName: string; onClose: () => void; onSubmit: (parentId: string | null) => void }) {
  const folderMap = useMemo(() => new Map(folders.map((folder) => [folder.id, folder])), [folders]);
  const moving = useMemo(() => new Set(items.map((item) => item.id)), [items]);
  const options = useMemo(() => folders.filter((folder) => {
    let cursor: FolderOption | undefined = folder;
    const seen = new Set<string>();
    while (cursor && !seen.has(cursor.id)) {
      if (moving.has(cursor.id)) return false;
      seen.add(cursor.id);
      cursor = cursor.parentId ? folderMap.get(cursor.parentId) : undefined;
    }
    return true;
  }).map((folder) => {
    const names = [folder.name];
    let parentId = folder.parentId;
    const seen = new Set([folder.id]);
    while (parentId && !seen.has(parentId)) {
      seen.add(parentId);
      const parent = folderMap.get(parentId);
      if (!parent) break;
      names.unshift(parent.name);
      parentId = parent.parentId;
    }
    return { ...folder, label: `${rootName} / ${names.join(" / ")}` };
  }).sort((left, right) => left.label.localeCompare(right.label, "zh-CN")), [folderMap, folders, moving, rootName]);
  const commonParent = items.length && items.every((item) => item.parentId === items[0].parentId) ? items[0].parentId || "" : "";
  const [parentId, setParentId] = useState(commonParent);
  return <div className="modal-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}><form className="modal" onSubmit={(event) => { event.preventDefault(); onSubmit(parentId || null); }}><div className="modal-icon"><FolderInput size={22} /></div><h2>移动{items.length > 1 ? ` ${items.length} 个项目` : `「${items[0]?.name}」`}</h2><p>请选择同一空间中的目标文件夹；不能移动到自身或自己的子目录。</p><label className="modal-label">目标位置<select value={parentId} onChange={(event) => setParentId(event.target.value)}><option value="">{rootName}（根目录）</option>{options.map((folder) => <option key={folder.id} value={folder.id}>{folder.label}</option>)}</select></label><div className="modal-actions"><button type="button" onClick={onClose}>取消</button><button type="submit" className="primary-button">移动到这里</button></div></form></div>;
}

function PermissionDialog({ value, members, currentUserId, onClose, onSubmit }: { value: PermissionEditorState; members: Member[]; currentUserId?: string; onClose: () => void; onSubmit: (value: PermissionEditorState) => void }) {
  const [draft, setDraft] = useState(value);
  const editableMembers = members.filter((member) => member.id !== currentUserId && member.role === "member");
  const permissionFor = (userId: string) => draft.entries.find((entry) => entry.userId === userId)?.permission || "none";
  const setPermission = (member: Member, permission: PermissionEntry["permission"]) => setDraft((current) => ({ ...current, entries: [...current.entries.filter((entry) => entry.userId !== member.id), { userId: member.id, username: member.username, displayName: member.displayName, permission }] }));
  return <div className="modal-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}><form className="modal permission-editor" onSubmit={(event) => { event.preventDefault(); onSubmit(draft); }}><div className="modal-icon"><ShieldCheck size={22} /></div><h2>设置「{value.name}」权限</h2><p>所有者和管理员始终拥有管理权限；可为普通家庭成员单独指定访问级别。</p><label className="checkbox-label permission-inherit"><input type="checkbox" checked={draft.inherit} onChange={(event) => setDraft((current) => ({ ...current, inherit: event.target.checked }))} />未单独设置的成员继承上级目录权限</label><div className="permission-members">{editableMembers.map((member) => <label key={member.id}><span className="member-avatar">{member.displayName.slice(0, 1)}</span><span><strong>{member.displayName}</strong><small>@{member.username}</small></span><select value={permissionFor(member.id)} onChange={(event) => setPermission(member, event.target.value as PermissionEntry["permission"])}><option value="none">不单独设置</option><option value="viewer">查看者</option><option value="editor">编辑者</option><option value="manager">管理者</option></select></label>)}{!editableMembers.length && <p className="permission-empty">暂无可单独配置的普通家庭成员。</p>}</div><div className="modal-actions"><button type="button" onClick={onClose}>取消</button><button type="submit" className="primary-button">保存权限</button></div></form></div>;
}

function AccountDialog({ user, sessions, sessionsLoading, onClose, onProfile, onPassword, onRevokeSession }: { user: CurrentUser; sessions: SessionItem[]; sessionsLoading: boolean; onClose: () => void; onProfile: (displayName: string) => void; onPassword: (currentPassword: string, newPassword: string) => void; onRevokeSession: (session: SessionItem) => void }) {
  const [tab, setTab] = useState<"profile" | "security">("profile");
  const [displayName, setDisplayName] = useState(user.displayName);
  const [passwords, setPasswords] = useState({ current: "", next: "", confirm: "" });
  const [error, setError] = useState("");
  const submitPassword = (event: FormEvent) => {
    event.preventDefault();
    if (passwords.next.length < 10) { setError("新密码至少需要 10 个字符"); return; }
    if (passwords.next !== passwords.confirm) { setError("两次输入的新密码不一致"); return; }
    setError("");
    onPassword(passwords.current, passwords.next);
  };
  return <div className="modal-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}><section className="modal account-dialog" role="dialog" aria-modal="true" aria-label="账户设置"><div className="modal-icon">{tab === "profile" ? <UserRound size={22} /> : <KeyRound size={22} />}</div><h2>账户设置</h2><div className="account-tabs"><button className={tab === "profile" ? "active" : ""} onClick={() => setTab("profile")}>个人资料</button><button className={tab === "security" ? "active" : ""} onClick={() => setTab("security")}>登录安全</button></div>{tab === "profile" ? <form onSubmit={(event) => { event.preventDefault(); if (displayName.trim()) onProfile(displayName.trim()); }}><label className="modal-label">用户名<input value={user.username} disabled /></label><label className="modal-label">显示名称<input value={displayName} onChange={(event) => setDisplayName(event.target.value)} maxLength={100} /></label><div className="modal-actions"><button type="button" onClick={onClose}>取消</button><button type="submit" className="primary-button">保存资料</button></div></form> : <div className="account-security"><form onSubmit={submitPassword}><label className="modal-label">当前密码<input type="password" value={passwords.current} onChange={(event) => setPasswords({ ...passwords, current: event.target.value })} autoComplete="current-password" /></label><label className="modal-label">新密码<input type="password" value={passwords.next} onChange={(event) => setPasswords({ ...passwords, next: event.target.value })} autoComplete="new-password" placeholder="至少 10 个字符" /></label><label className="modal-label">确认新密码<input type="password" value={passwords.confirm} onChange={(event) => setPasswords({ ...passwords, confirm: event.target.value })} autoComplete="new-password" /></label>{error && <p className="form-error">{error}</p>}<div className="modal-actions"><button type="button" onClick={onClose}>取消</button><button type="submit" className="primary-button">更新密码</button></div></form><section className="session-list" aria-label="登录设备"><div><strong>登录设备</strong><small>发现陌生会话时可立即退出</small></div>{sessionsLoading ? <p>正在加载…</p> : sessions.map((session) => <article key={session.id}><span><ShieldCheck size={17} /></span><span><strong>{session.current ? "当前设备" : `活跃于 ${relativeDate(session.lastSeenAt)}`}</strong><small>{new Intl.DateTimeFormat("zh-CN", { dateStyle: "medium", timeStyle: "short" }).format(new Date(session.createdAt))} 登录</small></span>{session.current ? <em>当前</em> : <button type="button" onClick={() => onRevokeSession(session)}>退出</button>}</article>)}{!sessionsLoading && !sessions.length && <p>暂无有效登录会话</p>}</section></div>}</section></div>;
}

function PermissionGuideDialog({ onClose }: { onClose: () => void }) {
  return <div className="modal-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}><section className="modal permission-guide" role="dialog" aria-modal="true" aria-label="权限说明"><div className="modal-icon"><ShieldCheck size={22} /></div><h2>权限如何工作</h2><p>个人空间始终只有本人可见；家庭空间可针对文件夹和相册设置权限。</p><div className="permission-levels"><span><strong>查看者</strong><small>浏览与下载内容</small></span><span><strong>编辑者</strong><small>上传、重命名和整理内容</small></span><span><strong>管理者</strong><small>编辑内容、分享并设置成员权限</small></span></div><div className="modal-actions"><button type="button" onClick={onClose}>关闭</button></div></section></div>;
}

function QuotaDialog({ space, onClose, onSubmit }: { space: Space; onClose: () => void; onSubmit: (quotaBytes: number) => void }) {
  const [quotaGB, setQuotaGB] = useState(space.quotaBytes ? String(Math.round(space.quotaBytes / 1024 ** 3)) : "");
  return <div className="modal-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}><form className="modal" onSubmit={(event) => { event.preventDefault(); const value = quotaGB.trim() ? Number(quotaGB) : 0; if (Number.isFinite(value) && value >= 0) onSubmit(Math.round(value * 1024 ** 3)); }}><div className="modal-icon"><HardDrive size={22} /></div><h2>设置家庭空间配额</h2><p>当前已用 {formatBytes(space.usedBytes)}；留空或填写 0 表示不限额。</p><label className="modal-label">容量（GB）<input type="number" min="0" step="1" value={quotaGB} onChange={(event) => setQuotaGB(event.target.value)} placeholder="不限额" /></label><div className="modal-actions"><button type="button" onClick={onClose}>取消</button><button type="submit" className="primary-button">保存配额</button></div></form></div>;
}

function ShareDialog({ target, onClose, onSubmit }: { target: { name: string }; onClose: () => void; onSubmit: (options: { password: string; days: number; allowDownload: boolean }) => void }) {
  const [password, setPassword] = useState("");
  const [days, setDays] = useState(7);
  const [allowDownload, setAllowDownload] = useState(true);
  const [error, setError] = useState("");
  const submit = (event: FormEvent) => {
    event.preventDefault();
    const normalizedPassword = password.trim();
    if (normalizedPassword && normalizedPassword.length < 4) {
      setError("访问密码至少需要 4 个字符");
      return;
    }
    setError("");
    onSubmit({ password: normalizedPassword, days, allowDownload });
  };
  return <div className="modal-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}><form className="modal" onSubmit={submit}><div className="modal-icon"><Share2 size={22} /></div><h2>分享「{target.name}」</h2><p>链接创建后只展示一次；复制给需要访问的人。</p><label className="modal-label">访问密码（可选）<input value={password} onChange={(event) => { setPassword(event.target.value); setError(""); }} placeholder="留空则无需密码，设置时至少 4 位" maxLength={128} /></label>{error && <p className="form-error">{error}</p>}<label className="modal-label">有效期<select value={days} onChange={(event) => setDays(Number(event.target.value))}><option value={1}>1 天</option><option value={7}>7 天</option><option value={30}>30 天</option><option value={0}>长期有效</option></select></label><label className="checkbox-label"><input type="checkbox" checked={allowDownload} onChange={(event) => setAllowDownload(event.target.checked)} />允许下载原文件</label><div className="modal-actions"><button type="button" onClick={onClose}>取消</button><button type="submit" className="primary-button">创建并复制链接</button></div></form></div>;
}

function ShareCreatedDialog({ url, onClose, onCopy }: { url: string; onClose: () => void; onCopy: () => void }) {
  return <div className="modal-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}><section className="modal share-created" role="dialog" aria-modal="true" aria-label="分享链接已创建"><div className="modal-icon"><Link2 size={22} /></div><h2>分享链接已创建</h2><p>出于安全考虑，这条链接只在这里展示一次。请复制保存后再关闭。</p><label className="modal-label">分享链接<input value={url} readOnly onFocus={(event) => event.currentTarget.select()} /></label><div className="share-created-actions"><button type="button" onClick={onCopy}><Copy size={16} /> 复制链接</button><a className="primary-button" href={url} target="_blank" rel="noreferrer"><ExternalLink size={16} /> 打开验证</a></div><div className="modal-actions"><button type="button" onClick={onClose}>完成</button></div></section></div>;
}

function ActionLinkDialog({ value, onClose, onCopy }: { value: CreatedActionLink; onClose: () => void; onCopy: () => void }) {
  return <div className="modal-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}><section className="modal share-created" role="dialog" aria-modal="true" aria-label={value.title}><div className="modal-icon"><Link2 size={22} /></div><h2>{value.title}</h2><p>{value.description}</p><label className="modal-label">一次性链接<input value={value.url} readOnly onFocus={(event) => event.currentTarget.select()} /></label><div className="share-created-actions"><button type="button" onClick={onCopy}><Copy size={16} /> 复制链接</button><a className="primary-button" href={value.url} target="_blank" rel="noreferrer"><ExternalLink size={16} /> 打开验证</a></div><div className="modal-actions"><button type="button" onClick={onClose}>完成</button></div></section></div>;
}

function NameDialog({ type, canInviteAdmin, onClose, onSubmit, onInvite }: { type: "folder" | "album" | "invite"; canInviteAdmin: boolean; onClose: () => void; onSubmit: (name: string) => void; onInvite: (role: "member" | "admin") => void }) {
  const [name, setName] = useState("");
  const [inviteRole, setInviteRole] = useState<"member" | "admin">("member");
  return <div className="modal-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}><form className="modal" onSubmit={(event) => { event.preventDefault(); if (type === "invite") onInvite(inviteRole); else onSubmit(name); }}><div className="modal-icon">{type === "folder" ? <Folder size={22} /> : type === "album" ? <Images size={22} /> : <UserPlus size={22} />}</div><h2>{type === "folder" ? "新建文件夹" : type === "album" ? "新建相册" : "邀请家庭成员"}</h2><p>{type === "invite" ? "将生成一个 7 天有效的一次性邀请链接。" : "给它取一个清晰、容易找到的名字。"}</p>{type !== "invite" ? <input value={name} onChange={(event) => setName(event.target.value)} placeholder={type === "folder" ? "文件夹名称" : "相册名称"} /> : <label className="modal-label">加入后的角色<select value={inviteRole} onChange={(event) => setInviteRole(event.target.value as "member" | "admin")}><option value="member">家庭成员</option>{canInviteAdmin && <option value="admin">家庭管理员</option>}</select>{!canInviteAdmin && <small>只有家庭所有者可以邀请管理员</small>}</label>}<div className="modal-actions"><button type="button" onClick={onClose}>取消</button><button type="submit" className="primary-button">{type === "invite" ? "生成并复制链接" : "创建"}</button></div></form></div>;
}

function EmptyState({ icon: Icon, title, text }: { icon: typeof Folder; title: string; text: string }) {
  return <div className="empty-state"><span><Icon size={30} /></span><h3>{title}</h3><p>{text}</p></div>;
}

function LoadingScreen() { return <div className="loading-screen"><span className="loading-cloud"><Cloud size={30} /></span><strong>栖云</strong><small>正在打开你的空间…</small></div>; }

function LoginScreen({ values, onChange, onSubmit, toast }: { values: { username: string; password: string }; onChange: (value: { username: string; password: string }) => void; onSubmit: (event: FormEvent) => void; toast: string }) {
  return <AuthShell title="欢迎回来" subtitle="回到你的文件与照片"><form onSubmit={onSubmit}><label>用户名<input value={values.username} onChange={(event) => onChange({ ...values, username: event.target.value })} placeholder="输入用户名" /></label><label>密码<input type="password" value={values.password} onChange={(event) => onChange({ ...values, password: event.target.value })} placeholder="输入密码" /></label><button className="auth-submit">进入我的空间 <ChevronRight size={18} /></button>{toast && <p className="auth-error">{toast}</p>}</form><div className="auth-foot"><ShieldCheck size={16} /> 账号与文件均由你自己的服务器保管</div></AuthShell>;
}

function SetupScreen({ values, onChange, onSubmit, toast }: { values: { householdName: string; timezone: string; username: string; displayName: string; password: string }; onChange: (value: typeof values) => void; onSubmit: (event: FormEvent) => void; toast: string }) {
  return <AuthShell title="创建你的家庭云" subtitle="只需一步，建立独立、安全的私人空间"><form onSubmit={onSubmit}><div className="form-pair"><label>家庭名称<input value={values.householdName} onChange={(event) => onChange({ ...values, householdName: event.target.value })} /></label><label>你的称呼<input value={values.displayName} onChange={(event) => onChange({ ...values, displayName: event.target.value })} placeholder="例如：陈谨" /></label></div><label>登录用户名<input value={values.username} onChange={(event) => onChange({ ...values, username: event.target.value })} placeholder="使用小写字母或数字" /></label><label>登录密码<input type="password" value={values.password} onChange={(event) => onChange({ ...values, password: event.target.value })} placeholder="至少 10 个字符" /></label><button className="auth-submit">创建并进入 <ChevronRight size={18} /></button>{toast && <p className="auth-error">{toast}</p>}</form><div className="auth-foot"><ShieldCheck size={16} /> 家庭管理员也无法查看成员的私有空间</div></AuthShell>;
}

function AuthShell({ title, subtitle, children }: { title: string; subtitle: string; children: React.ReactNode }) {
  return <main className="auth-shell"><section className="auth-story"><div className="auth-brand"><span><Cloud size={22} /></span>栖云</div><div className="story-copy"><span className="eyebrow">YOUR PRIVATE CLOUD</span><h1>把记忆与重要文件，<br />留在真正属于你的地方。</h1><p>文件、照片与家人的共同回忆，安静地存放在自己的服务器里。</p></div><div className="story-cards"><div className="story-card card-file"><Folder size={22} /><span><strong>家庭影像</strong><small>428 个项目</small></span></div><div className="story-card card-photo"><Camera size={22} /><span><strong>夏日旅行</strong><small>刚刚完成备份</small></span></div></div><small className="auth-privacy">RustFS 私有存储 · 严格权限隔离</small></section><section className="auth-panel"><div className="auth-box"><span className="mobile-auth-brand"><Cloud size={22} /> 栖云</span><h2>{title}</h2><p>{subtitle}</p>{children}</div></section></main>;
}
