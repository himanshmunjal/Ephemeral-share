const BASE_URL = import.meta.env.VITE_API_URL || "http://localhost:8080/api/v1";

// A random per-browser id. It lets the server recognise "my uploads" (so I can
// delete them) without any account, email or login. It is not an identity —
// just an unguessable label the server never shows to anyone else.
function clientId() {
  try {
    let id = localStorage.getItem("client_id");
    if (!id) {
      id = crypto.randomUUID();
      localStorage.setItem("client_id", id);
    }
    return id;
  } catch {
    return "anon-" + Math.random().toString(36).slice(2, 12);
  }
}

// The owner token authorises "end session" and deleting anyone's item. It is
// returned once at creation and kept only in this browser.
export const ownerToken = {
  get: (code) => {
    try {
      return localStorage.getItem(`owner:${code}`);
    } catch {
      return null;
    }
  },
  set: (code, token) => {
    try {
      localStorage.setItem(`owner:${code}`, token);
    } catch {
      /* private mode: owner controls simply won't persist across reloads */
    }
  },
  clear: (code) => {
    try {
      localStorage.removeItem(`owner:${code}`);
    } catch {
      /* ignore */
    }
  },
};

export class ApiError extends Error {
  constructor(message, status, code) {
    super(message);
    this.status = status;
    this.code = code; // machine-readable: not_found | full | locked_out | file_too_large | ...
  }
}

function authHeaders(code) {
  const h = { "X-Participant-ID": clientId() };
  const tok = code && ownerToken.get(code);
  if (tok) h["X-Owner-Token"] = tok;
  return h;
}

async function request(path, { code, who, headers, ...options } = {}) {
  let res;
  try {
    res = await fetch(`${BASE_URL}${path}`, {
      ...options,
      headers: { "Content-Type": "application/json", ...authHeaders(code), ...(who ? { "X-Participant-Who": who } : {}), ...headers },
    });
  } catch {
    throw new ApiError("Can't reach the server. Check your connection and try again.", 0, "network");
  }
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    let message = body.error || `Request failed with ${res.status}`;
    if (res.status === 429 && body.code !== "locked_out") message = "You're going too fast — wait a few seconds and retry.";
    throw new ApiError(message, res.status, body.code);
  }
  return res.status === 204 ? null : res.json();
}

export const api = {
  createSession: (type, maxParticipants) =>
    request("/sessions", {
      method: "POST",
      body: JSON.stringify({ type, max_participants: maxParticipants }),
    }),

  getSession: (code) => request(`/sessions/${code}`, { code }),

  joinSession: (code) => request(`/sessions/${code}/join`, { method: "POST", code }),

  endSession: (code) => request(`/sessions/${code}`, { method: "DELETE", code }),

  listContent: (code) => request(`/sessions/${code}/content`, { code }),

  addText: (code, contentType, text, who) =>
    request(`/sessions/${code}/content/text`, {
      method: "POST",
      code,
      who,
      body: JSON.stringify({ content_type: contentType, text }),
    }),

  requestUploadUrl: (code, filename, contentType, sizeBytes, who) =>
    request(`/sessions/${code}/content/upload-url`, {
      method: "POST",
      code,
      who,
      body: JSON.stringify({ filename, content_type: contentType, size_bytes: sizeBytes }),
    }),

  // Tells the backend the direct-to-storage upload finished, which is what
  // makes the file visible (and broadcast) to everyone else in the session.
  completeUpload: (code, contentId) =>
    request(`/sessions/${code}/content/${contentId}/complete`, { method: "POST", code }),

  // Removes a finished item, or abandons a pending upload (cancel / retry).
  deleteContent: (code, contentId) =>
    request(`/sessions/${code}/content/${contentId}`, { method: "DELETE", code }),

  // Uploads the raw file straight to object storage via the pre-signed URL.
  // Returns { promise, abort } so the caller can cancel mid-flight.
  uploadToPresignedUrl: (url, blob, onProgress) => {
    const xhr = new XMLHttpRequest();
    const promise = new Promise((resolve, reject) => {
      xhr.open("PUT", url);
      xhr.setRequestHeader("Content-Type", "application/octet-stream");
      xhr.upload.onprogress = (e) => {
        if (e.lengthComputable && onProgress) onProgress(Math.round((e.loaded / e.total) * 100));
      };
      xhr.onload = () => (xhr.status >= 200 && xhr.status < 300 ? resolve() : reject(new Error("Upload failed")));
      xhr.onerror = () => reject(new Error("Upload failed — check your connection"));
      xhr.onabort = () => reject(Object.assign(new Error("Upload cancelled"), { aborted: true }));
      xhr.send(blob);
    });
    return { promise, abort: () => xhr.abort() };
  },

  downloadUrl: (code, contentId) => `${BASE_URL}/sessions/${code}/content/${contentId}/download`,

  // Resolves the short-lived pre-signed object URL without a redirect, so the
  // follow-up fetch to object storage keeps a real Origin header for CORS.
  getDownloadUrl: (code, contentId) =>
    request(`/sessions/${code}/content/${contentId}/download?redirect=false`, { code }),
};

export function wsUrl(code, who) {
  const base = BASE_URL.replace(/^http/, "ws").replace(/\/api\/v1$/, "");
  // `who` is url-safe base64 ciphertext of the display identity.
  return `${base}/ws/sessions/${code}${who ? `?who=${who}` : ""}`;
}
