"use client";

import { useCallback, useEffect, useState } from "react";
import { api } from "./upload-client";

type SecurityInfo = { available: boolean; enabled: boolean; recoveryCodesRemaining: number; events: { kind: string; sourceIp: string; sourceLabel: string; device: string; createdAt: string }[] };
const eventNames: Record<string,string> = {
  "login.success": "登录成功", "login.password_denied": "密码验证失败", "login.mfa_denied": "二次验证失败",
  "mfa.enabled": "已开启二次验证", "mfa.disabled": "已关闭二次验证", "mfa.recovery_used": "已使用一次性恢复码",
  "mfa.setup_denied": "二次验证设置被拒绝", "mfa.code_denied": "动态码验证失败", "mfa.disable_denied": "关闭二次验证被拒绝",
  "session.revoked": "已退出一台登录设备", "password.changed": "已修改密码", "password.reset": "已重置密码",
};

export function AccountSecurity({ onSessionsChanged }: { onSessionsChanged: () => void }) {
  const [info,setInfo]=useState<SecurityInfo|null>(null);
  const [password,setPassword]=useState("");
  const [code,setCode]=useState("");
  const [setup,setSetup]=useState<{secret:string;uri:string}|null>(null);
  const [recovery,setRecovery]=useState<string[]>([]);
  const [busy,setBusy]=useState(false);
  const [message,setMessage]=useState("");
  const load=useCallback(async()=>setInfo(await api<SecurityInfo>("/me/security")),[]);
  useEffect(()=>{
    const controller=new AbortController();
    void api<SecurityInfo>("/me/security",{signal:controller.signal}).then(setInfo).catch(error=>{if(!controller.signal.aborted)setMessage(error instanceof Error?error.message:"加载失败");});
    return ()=>controller.abort();
  },[]);
  const run=async(action:()=>Promise<void>)=>{
    setBusy(true);setMessage("");
    try {await action();await load();}catch(error){setMessage(error instanceof Error?error.message:"操作失败");}finally{setBusy(false);}
  };
  const begin=()=>run(async()=>{
    setSetup(await api<{secret:string;uri:string}>("/me/totp/setup",{method:"POST",body:JSON.stringify({password})}));
    setPassword("");setCode("");
  });
  const confirm=()=>run(async()=>{
    const result=await api<{recoveryCodes:string[]}>("/me/totp/confirm",{method:"POST",body:JSON.stringify({code:code.trim()})});
    setRecovery(result.recoveryCodes);setSetup(null);setCode("");onSessionsChanged();
  });
  const disable=()=>run(async()=>{
    await api("/me/totp/disable",{method:"POST",body:JSON.stringify({password,code:code.trim()})});
    setPassword("");setCode("");onSessionsChanged();
  });
  const saveRecovery=()=>{
    const url=URL.createObjectURL(new Blob(["栖云一次性恢复码：每个只能使用一次，请离线保管。\n\n"+recovery.join("\n")],{type:"text/plain;charset=utf-8"}));
    const link=document.createElement("a");link.href=url;link.download="qiyun-recovery-codes.txt";link.click();
    window.setTimeout(()=>URL.revokeObjectURL(url),1000);
  };
  return <section className="two-factor-panel" aria-label="二次验证与安全事件">
    <h3>身份验证器二次验证</h3>
    {!info?<p>正在加载安全设置…</p>:<>
      <p>{info.enabled?`已开启 · 剩余 ${info.recoveryCodesRemaining} 个恢复码`:"开启后，登录还需要身份验证器生成的动态码。"}</p>
      {!info.available&&<p>管理员尚未配置二次验证密钥。</p>}
      {recovery.length>0?<div className="recovery-codes"><strong>恢复码只展示这一次</strong><p>丢失身份验证器时，可用密码和任一恢复码登录。每个恢复码只能用一次。</p><pre>{recovery.join("\n")}</pre><button type="button" className="secondary-button" onClick={saveRecovery}>下载恢复码</button><button type="button" className="primary-button" onClick={()=>setRecovery([])}>我已离线保存</button></div>:setup?<form onSubmit={event=>{event.preventDefault();void confirm();}}>
        <p>在身份验证器中手动添加账号，选择“基于时间”，输入以下密钥。设置需在 10 分钟内完成。</p>
        <label className="modal-label">账号密钥<input readOnly value={setup.secret} onFocus={event=>event.currentTarget.select()} /></label>
        <label className="modal-label">6 位动态码<input value={code} onChange={event=>setCode(event.target.value)} inputMode="numeric" autoComplete="one-time-code" maxLength={6} required /></label>
        <button className="primary-button" disabled={busy}>验证并开启</button><button type="button" onClick={()=>{setSetup(null);setCode("");}}>取消</button>
      </form>:<form onSubmit={event=>{event.preventDefault();void(info.enabled?disable():begin());}}>
        <label className="modal-label">确认当前密码<input type="password" value={password} onChange={event=>setPassword(event.target.value)} autoComplete="current-password" maxLength={128} required /></label>
        {info.enabled&&<label className="modal-label">动态码或恢复码<input value={code} onChange={event=>setCode(event.target.value)} autoComplete="one-time-code" maxLength={22} required /></label>}
        <button className="secondary-button" disabled={busy||!info.available}>{busy?"正在处理…":info.enabled?"关闭二次验证":"设置二次验证"}</button>
      </form>}
      <p>开启或关闭后，其他已登录设备会退出；当前设备保留。</p>
      <h3>最近安全事件</h3>
      <p>位置显示服务观察到的来源 IP；代理入口可能隐藏真实地址。</p>
      <ul className="security-event-list">{info.events.map((event,index)=><li key={`${event.createdAt}-${index}`}><strong>{eventNames[event.kind]||"安全设置变更"}</strong><small>{event.device} · {event.sourceLabel} {event.sourceIp}</small><time>{new Date(event.createdAt).toLocaleString("zh-CN")}</time></li>)}</ul>
      {!info.events.length&&<p>暂无安全事件</p>}
    </>}
    {message&&<p role="alert" className="form-error">{message}</p>}
  </section>;
}
