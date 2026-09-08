"use client";
import { useEffect, useState } from "react";
import { api } from "./upload-client";

type Report = {status?:string;startedAt?:string;updatedAt?:string;verifiedAt?:string;backupCreatedAt?:string;verifiedFiles?:number};
type Operations = {
  workerHeartbeat:string|null;pendingIntegrity:number;
  queue:{kind:string;state:string;count:number;oldestAt:string}[];
  failures:{kind:string;attempts:number;reason:string;updatedAt:string}[];
  host:{updatedAt:string;disks:{label:string;totalBytes:number;usedBytes:number;freeBytes:number}[];services:{name:string;status:string;health?:string}[]}|null;
  backup:Report|null;latestBackup:Report|null;offhost:Report|null;restore:Report|null;latestRestore:Report|null;
};
const kinds:Record<string,string>={finalize_upload:"上传确认",index_photo:"照片索引",seal_legacy_asset:"历史文件完整性校验",purge_node:"永久删除",cleanup_upload:"过期上传清理"};
const states:Record<string,string>={pending:"等待处理",running:"处理中",failed:"失败待检查",success:"成功",healthy:"正常",unhealthy:"异常",unavailable:"不可用",exited:"已停止"};
const bytes=(value:number)=>`${(value/1073741824).toFixed(1)} GiB`;
function date(value?:string|null){return value?new Date(value).toLocaleString("zh-CN"):"尚无记录";}
function stale(value:string|undefined|null,seconds:number){return !value||Date.now()-new Date(value).getTime()>seconds*1000;}

export function OperationsPanel(){
  const [data,setData]=useState<Operations|null>(null);
  const [error,setError]=useState("");
  useEffect(()=>{
    const controller=new AbortController();
    const refresh=()=>void api<Operations>("/admin/operations",{signal:controller.signal}).then(result=>{setData(result);setError("");}).catch(error=>{if(!controller.signal.aborted)setError(error instanceof Error?error.message:"无法读取运行状态");});
    refresh();const timer=window.setInterval(refresh,30000);
    return ()=>{controller.abort();window.clearInterval(timer);};
  },[]);
  const backupAt=data?.latestBackup?.updatedAt;
  const offhostAt=data?.offhost?.backupCreatedAt;
  const restoreAt=data?.latestRestore?.updatedAt;
  return <section className="surface-card operations-panel" aria-label="设备运维">
    <h2>设备运维</h2><p>每 30 秒更新，仅家庭所有者可见。</p>
    {error&&<p role="alert">{error}</p>}
    {!data?<p>正在读取运行状态…</p>:<>
      <div className="ops-summary"><article className={stale(data.workerHeartbeat,90)?"ops-warning":""}><strong>后台处理服务</strong><span>{stale(data.workerHeartbeat,90)?"心跳异常或尚未启动":"运行中"}</span><small>最近心跳：{date(data.workerHeartbeat)}</small></article><article><strong>历史文件摘要</strong><span>待校验 {data.pendingIntegrity} 个</span><small>校验完成后可执行文件哈希恢复演练</small></article></div>
      <h3>磁盘与容器</h3>
      {(!data.host||stale(data.host.updatedAt,180))&&<p className="ops-warning">主机报告缺失或已过期，请检查状态采集服务。</p>}
      <div className="ops-summary">{data.host?.disks.map(disk=><article key={disk.label} className={disk.freeBytes/disk.totalBytes<0.1?"ops-warning":""}><strong>{disk.label}</strong><span>剩余 {bytes(disk.freeBytes)}</span><small>已用 {bytes(disk.usedBytes)} / 总计 {bytes(disk.totalBytes)}</small><progress value={disk.usedBytes} max={disk.totalBytes} /></article>)}</div>
      <ul className="ops-services">{data.host?.services.map(service=><li key={service.name}><strong>{service.name}</strong><span>{states[service.health||service.status]||service.status}</span></li>)}</ul>
      <h3>备份与恢复</h3><div className="ops-summary">
        <article className={stale(backupAt,36*3600)?"ops-warning":""}><strong>本机备份</strong><span>{date(backupAt)}</span><small>最近运行：{states[data.backup?.status||""]||"尚无记录"}</small></article>
        <article className={stale(offhostAt,48*3600)?"ops-warning":""}><strong>异机副本</strong><span>{date(offhostAt)}</span><small>校验时间：{date(data.offhost?.verifiedAt)}</small></article>
        <article className={stale(restoreAt,8*86400)?"ops-warning":""}><strong>完整恢复演练</strong><span>{date(restoreAt)}</span><small>最近运行：{states[data.restore?.status||""]||"尚无记录"} · 已验证 {data.latestRestore?.verifiedFiles||0} 个文件</small></article>
      </div>
      <h3>后台队列</h3>{data.queue.length?<table><thead><tr><th>任务</th><th>状态</th><th>数量</th><th>最早任务</th></tr></thead><tbody>{data.queue.map(item=><tr key={`${item.kind}-${item.state}`}><td>{kinds[item.kind]||"后台任务"}</td><td>{states[item.state]||item.state}</td><td>{item.count}</td><td>{date(item.oldestAt)}</td></tr>)}</tbody></table>:<p>没有积压任务。</p>}
      <h3>最近失败</h3>{data.failures.length?<ul className="ops-failures">{data.failures.map((item,index)=><li key={`${item.updatedAt}-${index}`}><strong>{kinds[item.kind]||"后台任务"} · 已尝试 {item.attempts} 次</strong><span>{item.reason}</span><small>{date(item.updatedAt)}</small></li>)}</ul>:<p>没有失败任务。</p>}
    </>}
  </section>;
}
