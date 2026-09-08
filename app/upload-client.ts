export type UploadTask = {
  id: string;
  name: string;
  relativePath: string;
  size: number;
  progress: number;
  state: "queued" | "uploading" | "checking" | "confirming" | "needs_file" | "paused" | "ready" | "failed";
  error?: string;
};

export type UploadSection = "files" | "photos";

type UploadSession = {
  id: string;
  nodeId: string;
  method: "put" | "multipart";
  name: string;
  url?: string;
  partSize?: number;
};

type Part = { partNumber: number; etag: string };
type FileMetadata = { name: string; size: number; lastModified: number; type: string; relativePath: string };
export type ResumableUpload = { key: string; ownerUserId: string; session: UploadSession; file?: File; metadata: FileMetadata; sha256?: string; phase?: "uploading" | "confirming"; parts: Part[]; partSize: number; updatedAt: number; albumId?: string };
export class NeedsFileError extends Error { constructor(){super("请重新选择原文件，已上传分片会保留");this.name="NeedsFileError";} }
const memoryFiles = new Map<string,File>();
const memoryRecords = new Map<string,ResumableUpload>();
function fileMetadata(file: File): FileMetadata { return {name:file.name,size:file.size,lastModified:file.lastModified,type:file.type,relativePath:file.webkitRelativePath||file.name}; }

const apiBase = process.env.NEXT_PUBLIC_API_BASE || "/api/v1";
class ApiError extends Error {
  constructor(message:string,readonly status:number,readonly code?:string){super(message);this.name="ApiError";}
}

async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const controller = new AbortController();
  const timeout = window.setTimeout(() => controller.abort(new DOMException("Request timed out", "TimeoutError")), 30_000);
  const abort = () => controller.abort(init?.signal?.reason);
  if (init?.signal?.aborted) abort();
  else init?.signal?.addEventListener("abort", abort, { once: true });
  const headers = new Headers(init?.headers);
  if (init?.body && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  let response: Response;
  try {
    response = await fetch(`${apiBase}${path}`, { credentials: "include", ...init, headers, signal: controller.signal });
  } catch (error) {
    if (controller.signal.aborted && !init?.signal?.aborted) throw new Error("请求超时，请检查网络后重试");
    throw error;
  } finally {
    window.clearTimeout(timeout);
    init?.signal?.removeEventListener("abort", abort);
  }
  if (!response.ok) {
    const data = await response.json().catch(() => null);
    throw new ApiError(data?.error?.message || `请求失败 (${response.status})`,response.status,data?.error?.code);
  }
  return response.status === 204 ? (undefined as T) : response.json();
}

function putBlob(
  url: string,
  blob: Blob,
  contentType: string,
  signal: AbortSignal,
  onProgress: (loaded: number) => void,
): Promise<string> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("PUT", url);
    xhr.timeout = 10 * 60 * 1000;
    xhr.setRequestHeader("Content-Type", contentType || "application/octet-stream");
    xhr.upload.onprogress = (event) => onProgress(event.loaded);
    xhr.onerror = () => reject(new Error("网络中断，请重试"));
    xhr.ontimeout = () => reject(new Error("上传超时，请重试或暂停后继续"));
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve(xhr.getResponseHeader("ETag") || "");
      } else {
        reject(new Error(`对象存储返回 ${xhr.status}`));
      }
    };
    const abort = () => {
      xhr.abort();
      reject(new DOMException("Paused", "AbortError"));
    };
    if (signal.aborted) {
      abort();
      return;
    }
    signal.addEventListener("abort", abort, { once: true });
    xhr.onloadend = () => signal.removeEventListener("abort", abort);
    xhr.send(blob);
  });
}

async function saveResumeState(key: string, input: Omit<ResumableUpload,"key"|"metadata"> & {metadata?:FileMetadata}) {
  if(input.file) memoryFiles.set(key,input.file);
  const metadata=input.metadata || (input.file?fileMetadata(input.file):undefined);
  if(!metadata) throw new Error("缺少恢复文件信息");
  const value={...input,file:undefined,metadata};
  memoryRecords.set(key,{...value,key});
  if (!("indexedDB" in window)) return;
  await new Promise<void>((resolve) => {
    const request = indexedDB.open("pan-upload-queue", 1);
    request.onupgradeneeded = () => request.result.createObjectStore("uploads");
    request.onerror = () => resolve();
    request.onsuccess = () => {
      const tx = request.result.transaction("uploads", "readwrite");
      tx.objectStore("uploads").put(value, key);
      tx.oncomplete = () => { request.result.close(); resolve(); };
      tx.onerror = () => { request.result.close(); resolve(); };
    };
  });
}

export async function clearResumeState(key: string) {
  memoryFiles.delete(key); memoryRecords.delete(key);
  if (!("indexedDB" in window)) return;
  await new Promise<void>((resolve) => {
    const request = indexedDB.open("pan-upload-queue", 1);
    request.onupgradeneeded = () => request.result.createObjectStore("uploads");
    request.onerror = () => resolve();
    request.onsuccess = () => {
      const tx = request.result.transaction("uploads", "readwrite");
      tx.objectStore("uploads").delete(key);
      tx.oncomplete = () => { request.result.close(); resolve(); };
      tx.onerror = () => { request.result.close(); resolve(); };
    };
  });
}

export async function clearAllResumeState() {
  memoryFiles.clear();memoryRecords.clear();
  if (!("indexedDB" in window)) return;
  await new Promise<void>((resolve) => {
    const request = indexedDB.open("pan-upload-queue", 1);
    request.onupgradeneeded = () => request.result.createObjectStore("uploads");
    request.onerror = () => resolve();
    request.onsuccess = () => {
      const tx = request.result.transaction("uploads", "readwrite");
      tx.objectStore("uploads").clear();
      tx.oncomplete = () => { request.result.close(); resolve(); };
      tx.onerror = () => { request.result.close(); resolve(); };
    };
  });
}

export async function loadResumableUploads(ownerUserId: string): Promise<ResumableUpload[]> {
  const inMemory=()=>Array.from(memoryRecords.values()).filter(item=>item.ownerUserId===ownerUserId).map(item=>({...item,file:memoryFiles.get(item.key)}));
  if (!("indexedDB" in window)) return inMemory();
  return new Promise((resolve) => {
    const request = indexedDB.open("pan-upload-queue", 1);
    request.onupgradeneeded = () => request.result.createObjectStore("uploads");
    request.onerror = () => resolve(inMemory());
    request.onsuccess = () => {
      const results: ResumableUpload[] = [];
      const tx = request.result.transaction("uploads", "readwrite");
      const cursor = tx.objectStore("uploads").openCursor();
      cursor.onsuccess = () => {
        if (!cursor.result) return;
        const value = cursor.result.value as Partial<ResumableUpload>;
        const key=String(cursor.result.key);
        const oldFile=value.file instanceof File?value.file:undefined;
        const metadata=value.metadata || (oldFile?fileMetadata(oldFile):undefined);
        if(oldFile&&metadata) cursor.result.update({...value,file:undefined,metadata});
        if (value.ownerUserId === ownerUserId && value.session && metadata) {
          if(oldFile)memoryFiles.set(key,oldFile);
          const item={...value,key,ownerUserId,session:value.session,metadata,file:memoryFiles.get(key),parts:value.parts||[],partSize:value.partSize||16*1024*1024,updatedAt:value.updatedAt||0};
          results.push(item);memoryRecords.set(key,item);
        }
        cursor.result.continue();
      };
      tx.oncomplete = () => { request.result.close(); const keys=new Set(results.map(item=>item.key));resolve([...results,...inMemory().filter(item=>!keys.has(item.key))]); };
      tx.onerror = () => { request.result.close(); resolve(inMemory()); };
    };
  });
}

export async function createFolderBatch(
  spaceId: string,
  parentId: string | null,
  files: File[],
): Promise<string | undefined> {
  const folders = Array.from(
    new Set(
      files.flatMap((file) => {
        const relative = file.webkitRelativePath || file.name;
        const pieces = relative.split("/").slice(0, -1);
        return pieces.map((_, index) => pieces.slice(0, index + 1).join("/"));
      }),
    ),
  );
  if (!folders.length && files.length === 1) return undefined;
  const batch = await api<{ id: string }>("/upload-batches", {
    method: "POST",
    body: JSON.stringify({ spaceId, parentId, folders: folders.slice(0, 500), totalFiles: files.length }),
  });
  return batch.id;
}

async function waitForPublication(sessionId: string, signal: AbortSignal): Promise<string> {
  for (;;) {
    const status = await api<{ state: string; nodeId: string }>(`/uploads/${sessionId}/resume`, { method: "POST", signal });
    if (status.state === "ready") return status.nodeId;
    if (status.state !== "completing") throw new Error("文件尚未完成上传，请重试");
    await new Promise<void>((resolve, reject) => {
      const abort = () => { window.clearTimeout(timer); reject(new DOMException("Paused", "AbortError")); };
      const timer = window.setTimeout(() => { signal.removeEventListener("abort", abort); resolve(); }, 1500);
      if (signal.aborted) abort();
      else signal.addEventListener("abort", abort, { once: true });
    });
  }
}

function hashFile(file: File, signal: AbortSignal): Promise<string> {
  return new Promise((resolve, reject) => {
    const worker = new Worker(new URL("./hash-worker.ts", import.meta.url), { type: "module" });
    const finish = () => { worker.terminate(); signal.removeEventListener("abort", abort); };
    const abort = () => { finish(); reject(new DOMException("Paused", "AbortError")); };
    worker.onmessage = (event: MessageEvent<{ digest?: string; error?: string }>) => {
      finish();
      if (event.data.digest) resolve(event.data.digest);
      else reject(new Error(event.data.error || "文件校验失败"));
    };
    worker.onerror = () => { finish(); reject(new Error("文件校验失败，请重试")); };
    if (signal.aborted) { abort(); return; }
    signal.addEventListener("abort", abort, { once: true });
    worker.postMessage(file);
  });
}

async function completeAndWait(sha256: string, sessionId: string, parts: Part[], signal: AbortSignal) {
  for(let attempt=0;;attempt++) {
    try {await api(`/uploads/${sessionId}/complete`, { method: "POST", body: JSON.stringify({ parts, sha256 }), signal });break;}
    catch(error){if(!(error instanceof ApiError)||error.code!=="concurrent_change"||attempt>=2||signal.aborted)throw error;}
  }
  return waitForPublication(sessionId, signal);
}

export async function uploadFile(
  file: File,
  options: {
    spaceId: string;
    parentId: string | null;
    batchId?: string;
    albumId?: string;
    section?: UploadSection;
    ownerUserId: string;
    resumeKey?: string;
    signal: AbortSignal;
    onProgress: (value: number) => void;
    onPhase?: (value: UploadTask["state"]) => void;
  },
) {
  const session = await api<UploadSession>("/uploads", {
    method: "POST",
    body: JSON.stringify({
      spaceId: options.spaceId,
      parentId: options.parentId,
      batchId: options.batchId,
      name: file.name,
      relativePath: file.webkitRelativePath || file.name,
      sizeBytes: file.size,
      mimeType: file.type || "application/octet-stream",
      conflictPolicy: "keep_both",
      section: options.section || "files",
    }),
    signal: options.signal,
  });
  const resumeKey = options.resumeKey || `${session.id}:${file.name}:${file.size}:${file.lastModified}`;
  await saveResumeState(resumeKey, { ownerUserId: options.ownerUserId, session, file, parts: [], partSize: session.partSize || 16 * 1024 * 1024, updatedAt: Date.now(), albumId: options.albumId });
  options.onPhase?.("checking");
  const sha256=await hashFile(file,options.signal);
  const snapshot={ownerUserId:options.ownerUserId,session,file,sha256,partSize:session.partSize||16*1024*1024,updatedAt:Date.now(),albumId:options.albumId};
  await saveResumeState(resumeKey,{...snapshot,parts:[]});
  options.onPhase?.("uploading");

  if (session.method === "put") {
    await putBlob(session.url!, file, file.type, options.signal, (loaded) => options.onProgress(loaded / Math.max(1, file.size)));
    await saveResumeState(resumeKey,{...snapshot,parts:[],phase:"confirming"});
    options.onPhase?.("confirming");
    await completeAndWait(sha256, session.id, [], options.signal);
    await clearResumeState(resumeKey);
    options.onProgress(1);
    return session.nodeId;
  }

  const partSize = session.partSize || 16 * 1024 * 1024;
  const partCount = Math.ceil(file.size / partSize);
  const completed: Part[] = [];
  const partProgress = new Map<number, number>();
  const updateProgress = () => {
    let loaded = 0;
    partProgress.forEach((value) => (loaded += value));
    options.onProgress(loaded / Math.max(1, file.size));
  };
  for (let batchStart = 1; batchStart <= partCount; batchStart += 20) {
    const numbers = Array.from({ length: Math.min(20, partCount - batchStart + 1) }, (_, index) => batchStart + index);
    const signed = await api<{ items: { partNumber: number; url: string }[] }>(`/uploads/${session.id}/parts`, {
      method: "POST",
      body: JSON.stringify({ partNumbers: numbers }),
      signal: options.signal,
    });
    for (let cursor = 0; cursor < signed.items.length; cursor += 4) {
      const group = signed.items.slice(cursor, cursor + 4);
      const results = await Promise.all(
        group.map(async ({ partNumber, url }) => {
          const start = (partNumber - 1) * partSize;
          const blob = file.slice(start, Math.min(file.size, start + partSize));
          const etag = await putBlob(url, blob, file.type, options.signal, (loaded) => {
            partProgress.set(partNumber, loaded);
            updateProgress();
          });
          if (!etag) throw new Error("对象存储未返回分片校验标识，请检查 RustFS CORS 配置");
          partProgress.set(partNumber, blob.size);
          return { partNumber, etag };
        }),
      );
      completed.push(...results);
      await saveResumeState(resumeKey, { ...snapshot, parts: completed, partSize, updatedAt: Date.now() });
    }
  }
  completed.sort((a, b) => a.partNumber - b.partNumber);
  await saveResumeState(resumeKey,{...snapshot,parts:completed,phase:"confirming"});
  options.onPhase?.("confirming");
  await completeAndWait(sha256, session.id, completed, options.signal);
  await clearResumeState(resumeKey);
  options.onProgress(1);
  return session.nodeId;
}

export async function resumeMultipartUpload(
  resumable: ResumableUpload,
  signal: AbortSignal,
  onProgress: (value: number) => void,
  onPhase?: (value: UploadTask["state"]) => void,
  selectedFile?: File,
) {
  const { session, key } = resumable;
  const refreshed = await api<{ state: "uploading" | "completing" | "ready"; nodeId: string; method?: "put" | "multipart"; url?: string; partSize?: number }>(`/uploads/${session.id}/resume`, { method: "POST", signal });
  if (refreshed.state === "completing") {onPhase?.("confirming"); await waitForPublication(session.id, signal);}
  if (refreshed.state === "ready" || refreshed.state === "completing") {
    await clearResumeState(key);
    onProgress(1);
    return refreshed.nodeId;
  }
  const activeSession = { ...session, method: refreshed.method || session.method, url: refreshed.url || session.url, partSize: refreshed.partSize || session.partSize };
  if(resumable.phase==="confirming"&&resumable.sha256){
    onPhase?.("confirming");await completeAndWait(resumable.sha256,session.id,resumable.parts,signal);await clearResumeState(key);onProgress(1);return refreshed.nodeId;
  }
  const file=selectedFile||resumable.file||memoryFiles.get(key);
  if(!file)throw new NeedsFileError();
  if(file.name!==resumable.metadata.name||file.size!==resumable.metadata.size)throw new Error("所选文件的名称或大小不匹配，请选择原文件");
  onPhase?.("checking");
  const sha256=await hashFile(file,signal);
  if(resumable.sha256&&resumable.sha256!==sha256)throw new Error("所选文件内容已改变，请选择原文件");
  resumable={...resumable,sha256,file};
  onPhase?.("uploading");
  const partSize = activeSession.partSize || resumable.partSize;
  await saveResumeState(key, { ...resumable, session: activeSession, partSize, updatedAt: Date.now() });
  if (activeSession.method === "put") {
    if (!activeSession.url) throw new Error("无法刷新上传地址，请重新选择文件");
    await putBlob(activeSession.url, file, file.type, signal, (loaded) => onProgress(loaded / Math.max(1, file.size)));
    await saveResumeState(key,{...resumable,parts:[],phase:"confirming"});onPhase?.("confirming");
    await completeAndWait(sha256, activeSession.id, [], signal);
    await clearResumeState(key);
    onProgress(1);
    return refreshed.nodeId;
  }
  const completed = [...resumable.parts];
  const completedNumbers = new Set(completed.map((part) => part.partNumber));
  const partCount = Math.ceil(file.size / partSize);
  const partProgress = new Map<number, number>();
  completedNumbers.forEach((number) => partProgress.set(number, Math.min(partSize, file.size - (number - 1) * partSize)));
  const updateProgress = () => {
    let loaded = 0;
    partProgress.forEach((value) => (loaded += value));
    onProgress(loaded / Math.max(1, file.size));
  };
  updateProgress();
  const missing = Array.from({ length: partCount }, (_, index) => index + 1).filter((number) => !completedNumbers.has(number));
  for (let start = 0; start < missing.length; start += 20) {
    const numbers = missing.slice(start, start + 20);
    const signed = await api<{ items: { partNumber: number; url: string }[] }>(`/uploads/${activeSession.id}/parts`, { method: "POST", body: JSON.stringify({ partNumbers: numbers }), signal });
    for (let cursor = 0; cursor < signed.items.length; cursor += 4) {
      const results = await Promise.all(signed.items.slice(cursor, cursor + 4).map(async ({ partNumber, url }) => {
        const offset = (partNumber - 1) * partSize;
        const blob = file.slice(offset, Math.min(file.size, offset + partSize));
        const etag = await putBlob(url, blob, file.type, signal, (loaded) => { partProgress.set(partNumber, loaded); updateProgress(); });
        if (!etag) throw new Error("对象存储未返回分片校验标识，请检查 RustFS CORS 配置");
        partProgress.set(partNumber, blob.size);
        return { partNumber, etag };
      }));
      completed.push(...results);
      await saveResumeState(key, { ...resumable, session: activeSession, file, parts: completed, partSize, updatedAt: Date.now() });
    }
  }
  completed.sort((a, b) => a.partNumber - b.partNumber);
  await saveResumeState(key,{...resumable,parts:completed,phase:"confirming"});onPhase?.("confirming");
  await completeAndWait(sha256, activeSession.id, completed, signal);
  await clearResumeState(key);
  onProgress(1);
  return refreshed.nodeId;
}

export { api };
