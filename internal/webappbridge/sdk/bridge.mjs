// Generated from Desktop SDK SHA256: f9f5df16a818299173660be0bd37526dab81486760e16cf6242212b632b873b2
async function cloudPageHeaders() {
  const response = await fetch("/__desktop/bootstrap.json", { credentials: "same-origin", cache: "no-store", redirect: "error" });
  const value = await response.json();
  if (!response.ok || typeof value.csrfToken !== "string" || !value.csrfToken) throw new DesktopBridgeError("page.session", "app_grant_required", "Application session is unavailable.");
  return { "X-WebApp-CSRF": value.csrfToken };
}

const ACTION_PATH = "/__desktop/actions/call";
const ASSISTANT_IMAGE_UPLOAD_PATH = "/__desktop/assistant/image/uploads";
const APP_CONFIG_PATH = "/__desktop/app-config.json";
const USER_CONFIG_PATH = "/__desktop/user-config.json";

export class DesktopBridgeError extends Error {
  constructor(action, code, message, details) {
    super(message || code || "Desktop Bridge request failed");
    this.name = "DesktopBridgeError";
    this.action = action || "unknown";
    this.code = code || "action_failed";
    if (details !== undefined) this.details = details;
  }
}

// Compose uses a generation-bound bootstrap credential and short-lived action
// tokens. Desktop credentials retain their existing direct transport on macOS
// and Windows; only the explicit cloud credential prefix selects this protocol.
async function refreshCloudBackendToken(transport) {
  if (!transport?.bootstrapToken || transport.expiresAt > Date.now() + 30000) return;
  if (!transport.refresh) transport.refresh = (async () => {
    const target = new URL(transport.url); target.pathname = "/webapps/session";
    const response = await fetch(target.href, { method: "POST", redirect: "error", credentials: "omit",
      headers: { Authorization: "Bearer " + transport.bootstrapToken }, signal: AbortSignal.timeout(5000) });
    const value = await response.json();
    if (!response.ok || typeof value.token !== "string" || !value.token.startsWith("wba_") ||
        !Number.isSafeInteger(value.expiresAt) || value.expiresAt <= Date.now()) {
      throw new DesktopBridgeError("backend.session", "app_grant_required", "Application authorization is unavailable.");
    }
    transport.token = value.token; transport.expiresAt = value.expiresAt;
  })().finally(() => { transport.refresh = undefined; });
  await transport.refresh;
}

async function call(action, args = {}, transport, signal) {
  let body;
  try {
    body = JSON.stringify({ action, args });
  } catch {
    throw new DesktopBridgeError(action, "invalid_args", "Desktop Bridge arguments must be JSON serializable.");
  }
  let response;
  try {
    signal?.throwIfAborted();
    await refreshCloudBackendToken(transport);
    signal?.throwIfAborted();
    response = await fetch(transport?.url || ACTION_PATH, {
      method: "POST",
      headers: { "Content-Type": "application/json", ...(transport ? { Authorization: "Bearer " + transport.token } : await cloudPageHeaders()) },
      credentials: transport ? "omit" : "same-origin", redirect: "error", signal,
      body
    });
  } catch (error) {
    if (error instanceof DesktopBridgeError) throw error;
    throw new DesktopBridgeError(
      action,
      "bridge_unavailable",
      "Desktop Bridge is unavailable.",
      { cause: error?.name || "Error" }
    );
  }
  let payload;
  try {
    payload = await response.json();
  } catch {
    throw new DesktopBridgeError(action, "invalid_response", "Desktop Bridge returned an invalid response.");
  }
  if (!response.ok || !payload?.ok) {
    throw new DesktopBridgeError(
      payload?.action || action,
      payload?.error?.code || "action_failed",
      payload?.error?.message || "Desktop Bridge request failed with HTTP " + response.status + ".",
      payload?.error?.details
    );
  }
  return payload.result;
}

function reserved(action) {
  return async function reservedDesktopCapability() {
    throw new DesktopBridgeError(
      action,
      "not_supported",
      "This capability is unavailable in Compose."
    );
  };
}

async function listCapabilities() {
  return call("desktop.capabilities.list");
}

async function uploadAssistantImageInputs(source, mask) {
  if (!(source instanceof Blob)) {
    throw new DesktopBridgeError(
      "assistant.image",
      "invalid_args",
      "source must be an image Blob."
    );
  }
  if (mask !== undefined && mask !== null && !(mask instanceof Blob)) {
    throw new DesktopBridgeError(
      "assistant.image",
      "invalid_args",
      "mask must be a PNG Blob."
    );
  }
  const form = new FormData();
  form.set("source", source, source.name || "image-studio-source.png");
  if (mask instanceof Blob) {
    form.set("mask", mask, mask.name || "image-studio-mask.png");
  }
  let response;
  try {
    response = await fetch(ASSISTANT_IMAGE_UPLOAD_PATH, { method: "POST", body: form });
  } catch (error) {
    throw new DesktopBridgeError(
      "assistant.image",
      "bridge_unavailable",
      "Desktop image upload bridge is unavailable.",
      { cause: error?.name || "Error" }
    );
  }
  let payload;
  try {
    payload = await response.json();
  } catch {
    throw new DesktopBridgeError(
      "assistant.image",
      "invalid_response",
      "Desktop image upload bridge returned an invalid response."
    );
  }
  if (!response.ok || !payload?.ok || typeof payload.uploadId !== "string") {
    throw new DesktopBridgeError(
      "assistant.image",
      payload?.error?.code || "image_upload_failed",
      payload?.error?.message || "Desktop image upload failed."
    );
  }
  return payload.uploadId;
}

async function generateAssistantImage(input = {}) {
  if (!input || typeof input !== "object" || Array.isArray(input)) {
    throw new DesktopBridgeError("assistant.image", "invalid_args", "Image request must be an object.");
  }
  const { source, mask, ...request } = input;
  const uploadId = source instanceof Blob
    ? await uploadAssistantImageInputs(source, mask)
    : "";
  if (mask instanceof Blob && !uploadId) {
    throw new DesktopBridgeError("assistant.image", "invalid_args", "mask requires a source image.");
  }
  return call("assistant.image", {
    ...request,
    ...(uploadId ? { uploadId } : {})
  });
}

async function getAppConfig() {
  let response;
  try {
    response = await fetch(APP_CONFIG_PATH, { headers: { "Accept": "application/json" } });
  } catch (error) {
    throw new DesktopBridgeError(
      "desktop.app.getConfig",
      "bridge_unavailable",
      "WebApp configuration is unavailable.",
      { cause: error?.name || "Error" }
    );
  }
  let payload;
  try {
    payload = await response.json();
  } catch {
    throw new DesktopBridgeError(
      "desktop.app.getConfig",
      "invalid_response",
      "Desktop returned an invalid WebApp configuration."
    );
  }
  if (!response.ok || !payload || typeof payload !== "object" || Array.isArray(payload)) {
    throw new DesktopBridgeError(
      "desktop.app.getConfig",
      "invalid_response",
      "Desktop returned an invalid WebApp configuration."
    );
  }
  const appConfig = payload.appConfig;
  if (!appConfig || typeof appConfig !== "object" || Array.isArray(appConfig)) {
    throw new DesktopBridgeError(
      "desktop.app.getConfig",
      "invalid_response",
      "Desktop returned an invalid appConfig."
    );
  }
  return appConfig;
}

async function getUserConfig() {
  let response;
  try {
    response = await fetch(USER_CONFIG_PATH, { headers: { "Accept": "application/json" } });
  } catch (error) {
    throw new DesktopBridgeError(
      "desktop.app.getUserConfig",
      "bridge_unavailable",
      "WebApp user configuration is unavailable.",
      { cause: error?.name || "Error" }
    );
  }
  let payload;
  try {
    payload = await response.json();
  } catch {
    throw new DesktopBridgeError(
      "desktop.app.getUserConfig",
      "invalid_response",
      "Desktop returned an invalid WebApp user configuration."
    );
  }
  const values = payload?.values;
  if (!response.ok || !values || typeof values !== "object" || Array.isArray(values)) {
    throw new DesktopBridgeError(
      "desktop.app.getUserConfig",
      "invalid_response",
      "Desktop returned an invalid WebApp user configuration."
    );
  }
  return values;
}

// Uses the visitor's preload, including when this module is served by Tunnel.
// Never call the publisher's HTTP action bridge to obtain a visitor identity.
export const auth = Object.freeze({ isAvailable: () => false, createSession: reserved("auth.createSession") });

export const desktop = Object.freeze({
  requestAccess: (input) => {
    return call("desktop.requestAccess", input);
  },
  authenticateConnector: (input) => {
    if (!navigator.userActivation?.isActive) throw new DesktopBridgeError("desktop.authenticateConnector", "user_gesture_required", "Click to sign in to the connector.");
    return call("desktop.authenticateConnector", input);
  },
  app: Object.freeze({
    getConfig: getAppConfig,
    getUserConfig
  }),
  theme: Object.freeze({
    get: () => call("desktop.theme.get"),
    set: (input) => call("desktop.theme.set", input)
  }),
  locale: Object.freeze({
    get: () => call("desktop.locale.get"),
    set: (input) => call("desktop.locale.set", input)
  }),
  capabilities: Object.freeze({
    list: listCapabilities,
    async has(id) {
      const result = await listCapabilities();
      return result.capabilities.some((capability) =>
        capability.id === id && capability.status === "available" && capability.declared === true
      );
    }
  }),
    browser: Object.freeze({
      openExternal: (input) => call("desktop.browser.openExternal", input)
    }),
    dialog: Object.freeze({
      selectFiles: (input = {}) => call("desktop.dialog.selectFiles", input),
      selectDirectory: () => call("desktop.dialog.selectDirectory"),
      selectSavePath: (input = {}) => call("desktop.dialog.selectSavePath", input)
    }),
    microphone: Object.freeze({
      getPermission: () => call("desktop.microphone.getPermission"),
      requestAccess: () => call("desktop.microphone.requestAccess"),
      async open(constraints = {}) {
        await call("desktop.microphone.requestAccess");
        if (!navigator.mediaDevices?.getUserMedia) {
          throw new DesktopBridgeError(
            "desktop.microphone.open",
            "unavailable",
            "Microphone capture is unavailable in this WebApp."
          );
        }
        if (!constraints || typeof constraints !== "object" || Array.isArray(constraints)) {
          throw new DesktopBridgeError(
            "desktop.microphone.open",
            "invalid_args",
            "Microphone constraints must be an object."
          );
        }
        try {
          return await navigator.mediaDevices.getUserMedia({
            audio: { echoCancellation: true, noiseSuppression: true, ...constraints },
            video: false
          });
        } catch (error) {
          const permissionDenied = error?.name === "NotAllowedError" || error?.name === "SecurityError";
          throw new DesktopBridgeError(
            "desktop.microphone.open",
            permissionDenied ? "permission_denied" : "media_unavailable",
            permissionDenied ? "Microphone permission was denied." : "Microphone capture failed.",
            { cause: error?.name || "Error" }
          );
        }
      }
    }),
    clipboard: Object.freeze({
      writeText: (input) => call("desktop.clipboard.writeText", input),
      readText: reserved("desktop.clipboard.readText")
    }),
    notification: Object.freeze({
      show: (input) => call("desktop.notification.show", input)
    }),
    screen: Object.freeze({
      capture: reserved("desktop.screen.capture")
    }),
    file: Object.freeze({
      reveal: reserved("desktop.file.reveal")
    }),
    window: Object.freeze({
      getState: reserved("desktop.window.getState"),
      minimize: reserved("desktop.window.minimize"),
      maximize: reserved("desktop.window.maximize"),
      restore: reserved("desktop.window.restore"),
      close: reserved("desktop.window.close")
    }),
    camera: Object.freeze({
      getPermission: reserved("desktop.camera.getPermission"),
      requestAccess: reserved("desktop.camera.requestAccess"),
      open: reserved("desktop.camera.open")
    }),
    share: Object.freeze({
      open: reserved("desktop.share.open")
    })
});

const image = Object.assign(generateAssistantImage, {
  cancel: (requestId) => call("assistant.image.cancel", { requestId })
});
function executeJson(value, seen = new Set(), depth = 0) {
  if (depth > 64) throw Error("JSON is too deeply nested.");
  if (value === null || typeof value === "boolean" || typeof value === "string") return;
  if (typeof value === "number" && Number.isFinite(value)) return;
  if (!value || typeof value !== "object" || seen.has(value) || Object.getOwnPropertySymbols(value).length ||
      (!Array.isArray(value) && ![Object.prototype, null].includes(Object.getPrototypeOf(value)))) throw Error("Use standard JSON values.");
  seen.add(value);
  if (Array.isArray(value)) {
    if (Object.keys(value).length !== value.length) throw Error("Use dense JSON arrays.");
    for (const item of value) executeJson(item, seen, depth + 1);
  } else {
    for (const [key, descriptor] of Object.entries(Object.getOwnPropertyDescriptors(value))) {
      if (!descriptor.enumerable || !Object.hasOwn(descriptor, "value")) throw Error("Use JSON data properties.");
      if (depth === -1 && key === "input" && descriptor.value === undefined) continue;
      executeJson(descriptor.value, seen, depth + 1);
    }
  }
  seen.delete(value);
}

async function executeNodeRequest(transport, body, signal) {
  await refreshCloudBackendToken(transport);
  // Only the managed backend loads Node; page SDK clients never resolve this import.
  const { request } = await import("node:http");
  signal.throwIfAborted();
  return new Promise((resolve, reject) => {
    const req = request(transport.url, { method: "POST", signal,
      headers: { "Content-Type": "application/json", Authorization: "Bearer " + transport.token } }, res => {
      const chunks = []; let size = 0;
      res.on("data", chunk => {
        size += chunk.length;
        if (size > 1024 * 1024 + 4096) {
          req.destroy(new DesktopBridgeError("assistant.execute", "assistant_result_too_large", "Execution result is too large."));
        } else chunks.push(chunk);
      });
      res.on("error", reject);
      res.on("end", () => resolve({ ok: res.statusCode >= 200 && res.statusCode < 300,
        json: async () => JSON.parse(Buffer.concat(chunks).toString("utf8")) }));
    });
    req.on("error", reject);
    req.end(body);
  });
}

async function executeAssistant(input, { signal } = {}, transport) {
  const action = "assistant.execute";
  let body;
  try {
    executeJson(input, new Set(), -1);
    if (!input || Array.isArray(input) || typeof input !== "object" ||
        Object.keys(input).some(key => !["message", "input", "outputSchema"].includes(key)) ||
        typeof input.message !== "string" || !input.message.trim() ||
        !input.outputSchema || Array.isArray(input.outputSchema) || input.outputSchema.type !== "object") throw Error("Use message, input and an object outputSchema.");
    if (input.message.length > 12000) throw new DesktopBridgeError(action, "assistant_message_too_long", "Assistant message exceeds 12000 characters.");
    if (new TextEncoder().encode(JSON.stringify(input.outputSchema)).length > 65536) throw Error("Schema exceeds 64 KiB.");
    const checkFile = node => {
      if (!node || typeof node !== "object") return;
      if (Object.hasOwn(node, "$file")) throw Error("File inputs are not supported.");
      Object.values(node).forEach(checkFile);
    };
    checkFile(input.input);
    body = JSON.stringify({ action, args: input });
    if (new TextEncoder().encode(body).length > (transport ? 262144 : 65536)) throw Error("Execution request exceeds the bridge body limit.");
  } catch (error) {
    if (error instanceof DesktopBridgeError) throw error;
    throw new DesktopBridgeError(action, "invalid_args", error.message || "Invalid execution arguments.");
  }
  const deadline = new AbortController();
  const timer = setTimeout(() => deadline.abort(), 615000);
  timer.unref?.();
  const combined = signal ? AbortSignal.any([signal, deadline.signal]) : deadline.signal;
  try {
    combined.throwIfAborted();
    const response = transport ? await executeNodeRequest(transport, body, combined) :
      await fetch(ACTION_PATH, { method: "POST", headers: { "Content-Type": "application/json", ...await cloudPageHeaders() },
        body, credentials: "same-origin", redirect: "error", signal: combined });
    const payload = await response.json();
    combined.throwIfAborted();
    if (!response.ok || !payload?.ok) throw new DesktopBridgeError(action, payload?.error?.code || "action_failed",
      payload?.error?.message || "Assistant execution failed.", payload?.error?.details);
    const result = payload.result;
    if (typeof result?.chatId !== "string" || !result.chatId ||
        !(result.status === "success" && result.output && typeof result.output === "object" && !Array.isArray(result.output) ||
          result.status === "failure" && typeof result.reason === "string" && result.reason.trim())) {
      throw new DesktopBridgeError(action, "invalid_response", "Invalid execution result.");
    }
    return result;
  } catch (error) {
    if (combined.aborted) throw new DesktopBridgeError(action, signal?.aborted ? "cancelled" : "assistant_timeout", "Assistant execution ended.");
    if (error instanceof DesktopBridgeError) throw error;
    throw new DesktopBridgeError(action, error instanceof SyntaxError ? "invalid_response" : "bridge_unavailable", "Assistant execution is unavailable.");
  } finally { clearTimeout(timer); }
}

function createDataClient(call, image, execute) {
const connector = Object.freeze({
  list: () => call("connector.list"),
  describe: (input) => call("connector.describe", input),
  invoke: (input) => call("connector.invoke", input)
});
const skill = Object.freeze({ list: () => call("skill.list"), describe: (input) => call("skill.describe", input) });
const automation = Object.freeze({
  list: reserved("automation.list"), get: reserved("automation.get"),
  create: reserved("automation.create"), update: reserved("automation.update"),
  pause: reserved("automation.pause"), resume: reserved("automation.resume"), remove: reserved("automation.remove"),
  runs: Object.freeze({ list: reserved("automation.runs.list"), get: reserved("automation.runs.get") })
});
const kanban = Object.freeze({
  boards: Object.freeze({ list: () => call("kanban.boards.list") }),
  issues: Object.freeze({ list: (input = {}) => call("kanban.issues.list", input), get: (input) => call("kanban.issues.get", input) })
});
const artifact = Object.freeze({
  open: (input) => call("artifact.open", input),
  saveAs: (input) => call("artifact.saveAs", input),
  list: (input) => call("artifact.list", input),
  get: (input) => call("artifact.get", input),
  read: async (input) => {
    const result = await call("artifact.read", input);
    if (typeof result?.dataBase64 !== "string") throw new DesktopBridgeError("artifact.read", "invalid_response", "Invalid artifact response.");
    const bytes = Uint8Array.from(atob(result.dataBase64), value => value.charCodeAt(0));
    return new Blob([bytes]).stream();
  }
});
const assistant = Object.freeze({
  execute,
  stop: (input) => call("assistant.stop", input),
  async *subscribe(input, { signal } = {}) {
    let cursor = input.cursor || 0;
    while (!signal?.aborted) {
      const result = await call("assistant.events", { runId: input.runId, cursor }, signal);
      for (const event of result.events) { if (signal?.aborted) return; yield event; }
      cursor = result.cursor;
      if (result.terminal) return;
      await new Promise(resolve => {
        const done = () => { clearTimeout(timer); signal?.removeEventListener("abort", done); resolve(); };
        const timer = setTimeout(done, 500);
        signal?.addEventListener("abort", done, { once: true });
      });
    }
  },
  chat: (input) => call("assistant.chat", typeof input === "string" ? { message: input } : input),
  image
});
return Object.freeze({ connector, skill, automation, kanban, artifact, assistant });
}
export const { connector, skill, automation, kanban, artifact, assistant } = createDataClient((action, args, signal) => call(action, args, undefined, signal), Object.freeze(Object.assign(reserved("assistant.image"), { cancel: reserved("assistant.image.cancel") })), executeAssistant);

// Copy the generated bridge.mjs into a managed Node backend. Credentials stay
// in that process and must never be forwarded to frontend code or logs.
export function createBackendClient({ url, token }) {
  const target = new URL(url);
  if (target.protocol !== "http:" || !["127.0.0.1", "localhost", "[::1]"].includes(target.hostname) ||
      target.pathname !== "/webapps" || target.username || target.password || target.search || target.hash ||
      typeof token !== "string" || !token) {
    throw new DesktopBridgeError("backend.init", "invalid_args", "Use Desktop's injected backend bridge configuration.");
  }
  const transport = { url: target.href.replace(/\/$/u, "") + "/actions/call", token,
    ...(token.startsWith("wbb_") ? { bootstrapToken: token, expiresAt: 0 } : {}) };
  const client = createDataClient((action, args, signal) => call(action, args, transport, signal), reserved("assistant.image"), (input, options) => executeAssistant(input, options, transport));
  return Object.freeze({ ...client, artifact: Object.freeze({
    list: client.artifact.list, get: client.artifact.get, read: client.artifact.read,
    open: reserved("artifact.open"), saveAs: reserved("artifact.saveAs")
  }) });
}
