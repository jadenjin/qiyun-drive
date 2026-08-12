export type UploadTask = {
  id: string;
  name: string;
  relativePath: string;
  size: number;
  progress: number;
  state: "queued" | "uploading" | "paused" | "ready" | "failed";
  error?: string;
};

type UploadSession = {
  id: string;
  nodeId: string;
  method: "put" | "multipart";
  name: string;
  url?: string;
  partSize?: number;
};

type Part = { partNumber: number; etag: string };
export type ResumableUpload = { key: string; session: UploadSession; file: File; parts: Part[]; partSize: number; updatedAt: number; albumId?: string };

const apiBase = process.env.NEXT_PUBLIC_API_BASE || "/api/v1";

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
    throw new Error(data?.error?.message || `请求失败 (${response.status})`);
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

async function saveResumeState(key: string, value: unknown) {
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

export async function loadResumableUploads(): Promise<ResumableUpload[]> {
  if (!("indexedDB" in window)) return [];
  return new Promise((resolve) => {
    const request = indexedDB.open("pan-upload-queue", 1);
    request.onupgradeneeded = () => request.result.createObjectStore("uploads");
    request.onerror = () => resolve([]);
    request.onsuccess = () => {
      const results: ResumableUpload[] = [];
      const tx = request.result.transaction("uploads", "readonly");
      const cursor = tx.objectStore("uploads").openCursor();
      cursor.onsuccess = () => {
        if (!cursor.result) return;
        const value = cursor.result.value as Partial<ResumableUpload>;
        if (value.session && value.file instanceof File) {
          results.push({ key: String(cursor.result.key), session: value.session, file: value.file, parts: value.parts || [], partSize: value.partSize || 16 * 1024 * 1024, updatedAt: value.updatedAt || 0, albumId: value.albumId });
        }
        cursor.result.continue();
      };
      tx.oncomplete = () => { request.result.close(); resolve(results); };
      tx.onerror = () => { request.result.close(); resolve([]); };
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

export async function uploadFile(
  file: File,
  options: {
    spaceId: string;
    parentId: string | null;
    batchId?: string;
    albumId?: string;
    resumeKey?: string;
    signal: AbortSignal;
    onProgress: (value: number) => void;
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
    }),
    signal: options.signal,
  });
  const resumeKey = options.resumeKey || `${session.id}:${file.name}:${file.size}:${file.lastModified}`;
  await saveResumeState(resumeKey, { session, file, parts: [], partSize: session.partSize || 16 * 1024 * 1024, updatedAt: Date.now(), albumId: options.albumId });

  if (session.method === "put") {
    await putBlob(session.url!, file, file.type, options.signal, (loaded) => options.onProgress(loaded / Math.max(1, file.size)));
    await api(`/uploads/${session.id}/complete`, { method: "POST", body: JSON.stringify({ parts: [] }), signal: options.signal });
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
          if (!etag) throw new Error("对象存储未返回分片校验标识，请检查 MinIO CORS 配置");
          partProgress.set(partNumber, blob.size);
          return { partNumber, etag };
        }),
      );
      completed.push(...results);
      await saveResumeState(resumeKey, { session, file, parts: completed, partSize, updatedAt: Date.now(), albumId: options.albumId });
    }
  }
  completed.sort((a, b) => a.partNumber - b.partNumber);
  await api(`/uploads/${session.id}/complete`, { method: "POST", body: JSON.stringify({ parts: completed }), signal: options.signal });
  await clearResumeState(resumeKey);
  options.onProgress(1);
  return session.nodeId;
}

export async function resumeMultipartUpload(
  resumable: ResumableUpload,
  signal: AbortSignal,
  onProgress: (value: number) => void,
) {
  const { session, file, key } = resumable;
  const refreshed = await api<{ state: "uploading" | "ready"; nodeId: string; method?: "put" | "multipart"; url?: string; partSize?: number }>(`/uploads/${session.id}/resume`, { method: "POST", signal });
  if (refreshed.state === "ready") {
    await clearResumeState(key);
    onProgress(1);
    return refreshed.nodeId;
  }
  const activeSession = { ...session, method: refreshed.method || session.method, url: refreshed.url || session.url, partSize: refreshed.partSize || session.partSize };
  const partSize = activeSession.partSize || resumable.partSize;
  await saveResumeState(key, { ...resumable, session: activeSession, partSize, updatedAt: Date.now() });
  if (activeSession.method === "put") {
    if (!activeSession.url) throw new Error("无法刷新上传地址，请重新选择文件");
    await putBlob(activeSession.url, file, file.type, signal, (loaded) => onProgress(loaded / Math.max(1, file.size)));
    await api(`/uploads/${activeSession.id}/complete`, { method: "POST", body: JSON.stringify({ parts: [] }), signal });
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
        if (!etag) throw new Error("对象存储未返回分片校验标识，请检查 MinIO CORS 配置");
        partProgress.set(partNumber, blob.size);
        return { partNumber, etag };
      }));
      completed.push(...results);
      await saveResumeState(key, { session: activeSession, file, parts: completed, partSize, updatedAt: Date.now() });
    }
  }
  completed.sort((a, b) => a.partNumber - b.partNumber);
  await api(`/uploads/${activeSession.id}/complete`, { method: "POST", body: JSON.stringify({ parts: completed }), signal });
  await clearResumeState(key);
  onProgress(1);
  return refreshed.nodeId;
}

export { api };
