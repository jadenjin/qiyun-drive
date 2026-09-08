import { sha256 } from "@noble/hashes/sha2.js";

self.onmessage = async (event: MessageEvent<File>) => {
  try {
    const file = event.data;
    const hash = sha256.create();
    const chunkSize = 4 * 1024 * 1024;
    for (let offset = 0; offset < file.size; offset += chunkSize) {
      hash.update(new Uint8Array(await file.slice(offset, offset + chunkSize).arrayBuffer()));
    }
    const digest = Array.from(hash.digest(), (byte) => byte.toString(16).padStart(2, "0")).join("");
    self.postMessage({ digest });
  } catch {
    self.postMessage({ error: "无法读取文件进行校验，请重新选择文件" });
  }
};
