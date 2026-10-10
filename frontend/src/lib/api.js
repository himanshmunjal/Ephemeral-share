const BASE_URL = import.meta.env.VITE_API_URL || "http://localhost:8080/api/v1";

async function request(path, options = {}) {
  const res = await fetch(`${BASE_URL}${path}`, {
    credentials: "include",
    headers: { "Content-Type": "application/json", ...options.headers },
    ...options,
  });
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new Error(body.error || `Request failed with ${res.status}`);
  }
  return res.status === 204 ? null : res.json();
}

export const api = {
  createSession: (type, maxParticipants) =>
    request("/sessions", {
      method: "POST",
      body: JSON.stringify({ type, max_participants: maxParticipants }),
    }),

  getSession: (code) => request(`/sessions/${code}`),

  joinSession: (code) => request(`/sessions/${code}/join`, { method: "POST" }),

  endSession: (code) => request(`/sessions/${code}`, { method: "DELETE" }),

  listContent: (code) => request(`/sessions/${code}/content`),

  addText: (code, contentType, text) =>
    request(`/sessions/${code}/content/text`, {
      method: "POST",
      body: JSON.stringify({ content_type: contentType, text }),
    }),

  requestUploadUrl: (code, filename, contentType, sizeBytes) =>
    request(`/sessions/${code}/content/upload-url`, {
      method: "POST",
      body: JSON.stringify({ filename, content_type: contentType, size_bytes: sizeBytes }),
    }),

  // Tells the backend the direct-to-storage upload finished, which is what
  // makes the file visible (and broadcast) to everyone else in the session.
  completeUpload: (code, contentId) =>
    request(`/sessions/${code}/content/${contentId}/complete`, { method: "POST" }),

  // Uploads the raw file straight to object storage via the pre-signed URL.
  uploadToPresignedUrl: (url, file, onProgress) =>
    new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      xhr.open("PUT", url);
      xhr.setRequestHeader("Content-Type", file.type || "application/octet-stream");
      xhr.upload.onprogress = (e) => {
        if (e.lengthComputable && onProgress) onProgress(Math.round((e.loaded / e.total) * 100));
      };
      xhr.onload = () => (xhr.status >= 200 && xhr.status < 300 ? resolve() : reject(new Error("Upload failed")));
      xhr.onerror = () => reject(new Error("Upload failed"));
      xhr.send(file);
    }),

  downloadUrl: (code, contentId) => `${BASE_URL}/sessions/${code}/content/${contentId}/download`,
};

export function wsUrl(code) {
  const base = BASE_URL.replace(/^http/, "ws").replace(/\/api\/v1$/, "");
  return `${base}/ws/sessions/${code}`;
}
