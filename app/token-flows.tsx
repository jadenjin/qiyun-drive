"use client";
/* eslint-disable @next/next/no-img-element */

import { Archive, ChevronRight, Cloud, Download, File, Folder, Image as ImageIcon, LockKeyhole, ShieldCheck, UserPlus } from "lucide-react";
import Link from "next/link";
import { FormEvent, useCallback, useEffect, useState } from "react";
import { api } from "./upload-client";

export function InviteAccept({ token }: { token: string }) {
  const [values, setValues] = useState({ username: "", displayName: "", password: "" });
  const [state, setState] = useState<"form" | "done">("form");
  const [error, setError] = useState("");
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    try {
      await api("/invitations/accept", { method: "POST", body: JSON.stringify({ token, ...values }) });
      setState("done");
    } catch (value) { setError(value instanceof Error ? value.message : "加入失败"); }
  };
  return <TokenShell icon={<UserPlus size={25} />} title={state === "done" ? "欢迎回家" : "加入家庭空间"} subtitle={state === "done" ? "账号已经创建，可以进入你的私有空间。" : "创建你的账号。个人空间默认只有你能看到。"}>{state === "done" ? <Link className="auth-submit token-link" href="/">进入栖云 <ChevronRight size={18} /></Link> : <form onSubmit={submit}><label>你的称呼<input value={values.displayName} onChange={(event) => setValues({ ...values, displayName: event.target.value })} placeholder="例如：林夕" /></label><label>登录用户名<input value={values.username} onChange={(event) => setValues({ ...values, username: event.target.value })} /></label><label>设置密码<input type="password" value={values.password} onChange={(event) => setValues({ ...values, password: event.target.value })} placeholder="至少 10 个字符" /></label><button className="auth-submit">加入家庭 <ChevronRight size={18} /></button>{error && <p className="auth-error">{error}</p>}</form>}</TokenShell>;
}

export function PasswordReset({ token }: { token: string }) {
  const [password, setPassword] = useState("");
  const [done, setDone] = useState(false);
  const [error, setError] = useState("");
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    try { await api("/password-resets/complete", { method: "POST", body: JSON.stringify({ token, password }) }); setDone(true); }
    catch (value) { setError(value instanceof Error ? value.message : "重置失败"); }
  };
  return <TokenShell icon={<LockKeyhole size={25} />} title={done ? "密码已更新" : "设置新密码"} subtitle={done ? "其他设备上的旧登录会话已经失效。" : "使用至少 10 个字符，并避免重复使用旧密码。"}>{done ? <Link className="auth-submit token-link" href="/">返回登录 <ChevronRight size={18} /></Link> : <form onSubmit={submit}><label>新密码<input type="password" value={password} onChange={(event) => setPassword(event.target.value)} placeholder="输入新密码" /></label><button className="auth-submit">确认更新 <ChevronRight size={18} /></button>{error && <p className="auth-error">{error}</p>}</form>}</TokenShell>;
}

type ShareData = { resourceType: "file" | "folder" | "album"; name: string; description?: string; allowDownload: boolean; downloadUrl?: string; archiveUrl?: string; items?: { name: string; kind?: string; relativePath?: string; sizeBytes?: number; previewUrl?: string; remark?: string }[] };

export function PublicShare({ token }: { token: string }) {
  const [password, setPassword] = useState("");
  const [data, setData] = useState<ShareData | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [archiveAccessToken, setArchiveAccessToken] = useState("");
  const openShare = useCallback(async (passwordValue: string, silent = false) => {
    setLoading(true);
    try {
      const unlocked = await api<{ accessToken: string }>(`/public/shares/${token}/unlock`, { method: "POST", body: JSON.stringify({ password: passwordValue }) });
      const response = await fetch(`${process.env.NEXT_PUBLIC_API_BASE || "/api/v1"}/public/shares/${token}`, { headers: { Authorization: `Bearer ${unlocked.accessToken}` } });
      if (!response.ok) throw new Error("分享内容已经失效");
      const share = await response.json() as ShareData;
      setData(share);
      setArchiveAccessToken(unlocked.accessToken);
      setError("");
    } catch (value) {
      const message = value instanceof Error ? value.message : "无法打开分享";
      if (!silent || message !== "请输入访问密码") setError(message);
    } finally { setLoading(false); }
  }, [token]);
  useEffect(() => {
    const timer = window.setTimeout(() => { void openShare("", true); }, 0);
    return () => window.clearTimeout(timer);
  }, [openShare]);
  const unlock = async (event: FormEvent) => {
    event.preventDefault();
    await openShare(password);
  };
  if (loading && !data) return <TokenShell icon={<ShieldCheck size={25} />} title="正在安全打开分享" subtitle="正在验证链接并获取内容，请稍候。"><div className="token-loading" aria-live="polite">正在加载…</div></TokenShell>;
  if (!data) return <TokenShell icon={<LockKeyhole size={25} />} title="打开栖云分享" subtitle="此分享需要访问密码。"><form onSubmit={unlock}><label>访问密码<input type="password" value={password} onChange={(event) => setPassword(event.target.value)} placeholder="请输入分享密码" /></label><button className="auth-submit" disabled={loading}>查看分享 <ChevronRight size={18} /></button>{error && <p className="auth-error">{error}</p>}</form></TokenShell>;
  const KindIcon = data.resourceType === "album" ? ImageIcon : data.resourceType === "folder" ? Folder : File;
  return <main className="public-share-shell"><header><Link href="/"><span><Cloud size={20} /></span>栖云</Link><span><ShieldCheck size={15} /> 加密分享</span></header><section className="public-share-card"><div className="share-hero"><span className="share-kind"><KindIcon size={27} /></span><div><small>{data.resourceType === "album" ? "共享相册" : data.resourceType === "folder" ? "共享文件夹" : "共享文件"}</small><h1>{data.name}</h1>{data.description && <p>{data.description}</p>}</div>{data.downloadUrl && <a className="primary-button" href={data.downloadUrl}><Download size={17} /> 下载文件</a>}{data.archiveUrl && <form method="post" action={data.archiveUrl}><input type="hidden" name="access_token" value={archiveAccessToken} /><button className="primary-button" type="submit"><Archive size={17} /> 打包下载</button></form>}</div>{data.items && <div className={data.resourceType === "album" ? "public-photo-grid" : "public-file-list"}>{data.items.map((item, index) => data.resourceType === "album" ? <article key={`${item.name}-${index}`}>{item.previewUrl ? <img src={item.previewUrl} alt={item.remark || item.name} loading="lazy" decoding="async" /> : <span><ImageIcon /></span>}<div><strong>{item.remark || item.name}</strong><small>{item.name}</small></div></article> : <div key={`${item.relativePath}-${index}`}><span>{item.kind === "folder" ? <Folder size={18} /> : <File size={18} />}</span><strong>{item.relativePath || item.name}</strong><small>{item.kind === "folder" ? "文件夹" : formatPublicBytes(item.sizeBytes || 0)}</small></div>)}</div>}</section></main>;
}

function TokenShell({ icon, title, subtitle, children }: { icon: React.ReactNode; title: string; subtitle: string; children: React.ReactNode }) {
  return <main className="token-shell"><Link className="token-brand" href="/"><span><Cloud size={21} /></span>栖云</Link><section className="token-card"><span className="token-icon">{icon}</span><h1>{title}</h1><p>{subtitle}</p>{children}<small className="token-security"><ShieldCheck size={14} /> 内容由栖云私有存储保护</small></section></main>;
}

function formatPublicBytes(value: number) {
  if (!value) return "—";
  const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), 4);
  return `${(value / 1024 ** index).toFixed(index > 2 ? 1 : 0)} ${["B", "KB", "MB", "GB", "TB"][index]}`;
}
