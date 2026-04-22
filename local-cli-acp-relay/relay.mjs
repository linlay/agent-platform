#!/usr/bin/env node
import crypto from "node:crypto";
import fs from "node:fs";
import http from "node:http";
import os from "node:os";
import path from "node:path";
import process from "node:process";
import readline from "node:readline";
import { spawn, spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const DEFAULT_PORT = 3220;
const DEFAULT_HANDSHAKE_TIMEOUT_MS = 10_000;
const DEFAULT_RUN_TIMEOUT_MS = 10 * 60 * 1000;

const rootDir = path.dirname(fileURLToPath(import.meta.url));
const env = readEnv(path.join(rootDir, ".env"));

const defaultCwd = expandUserPath(pick("DEFAULT_CWD") || process.cwd());
const config = {
  port: toNumber(pick("PORT"), DEFAULT_PORT),
  authToken: pick("AUTH_TOKEN"),
  command: pick("CLAUDE_CODE_ACP_COMMAND") || "claude-code-acp",
  commandArgs: splitArgs(pick("CLAUDE_CODE_ACP_ARGS")),
  defaultCwd,
  allowedCwdRoots: parseAllowedRoots(pick("ALLOWED_CWD_ROOTS"), defaultCwd),
  handshakeTimeoutMs: toNumber(pick("HANDSHAKE_TIMEOUT_MS"), DEFAULT_HANDSHAKE_TIMEOUT_MS),
  runTimeoutMs: toNumber(pick("RUN_TIMEOUT_MS"), DEFAULT_RUN_TIMEOUT_MS),
};

const clients = new Set();
const chatSessions = new Map();
const activeRuns = new Map();
const activeSessionRuns = new Map();
const awaitingById = new Map();

let acp = null;
let acpStartError = "";

function pick(key) {
  return process.env[key] ?? env.get(key) ?? "";
}

function readEnv(filePath) {
  const values = new Map();
  if (!fs.existsSync(filePath)) {
    return values;
  }
  const lines = fs.readFileSync(filePath, "utf8").split(/\r?\n/u);
  for (const line of lines) {
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith("#")) {
      continue;
    }
    const index = trimmed.indexOf("=");
    if (index <= 0) {
      continue;
    }
    const key = trimmed.slice(0, index).trim();
    const value = trimmed.slice(index + 1).trim().replace(/^['"]|['"]$/gu, "");
    values.set(key, value);
  }
  return values;
}

function splitArgs(input) {
  const text = String(input || "").trim();
  if (!text) {
    return [];
  }
  const args = [];
  let current = "";
  let quote = "";
  let escaping = false;
  for (const char of text) {
    if (escaping) {
      current += char;
      escaping = false;
      continue;
    }
    if (char === "\\") {
      escaping = true;
      continue;
    }
    if (quote) {
      if (char === quote) {
        quote = "";
      } else {
        current += char;
      }
      continue;
    }
    if (char === "'" || char === '"') {
      quote = char;
      continue;
    }
    if (/\s/u.test(char)) {
      if (current) {
        args.push(current);
        current = "";
      }
      continue;
    }
    current += char;
  }
  if (current) {
    args.push(current);
  }
  return args;
}

function toNumber(raw, fallback) {
  const next = Number.parseInt(String(raw || ""), 10);
  return Number.isFinite(next) && next > 0 ? next : fallback;
}

function expandUserPath(input) {
  const trimmed = String(input || "").trim();
  if (trimmed === "~") {
    return os.homedir();
  }
  if (trimmed.startsWith("~/") || trimmed.startsWith("~\\")) {
    return path.join(os.homedir(), trimmed.slice(2));
  }
  return path.resolve(trimmed || process.cwd());
}

function parseAllowedRoots(raw, fallbackRoot) {
  const roots = String(raw || "")
    .split(path.delimiter)
    .map((item) => expandUserPath(item))
    .filter(Boolean);
  if (roots.length === 0) {
    roots.push(fallbackRoot || process.cwd());
  }
  return roots;
}

function now() {
  return Date.now();
}

function makeId(prefix) {
  if (typeof crypto.randomUUID === "function") {
    return prefix + "_" + crypto.randomUUID().replace(/-/gu, "").slice(0, 16);
  }
  return prefix + "_" + crypto.randomBytes(8).toString("hex");
}

function makeEvent(type, payload = {}) {
  return {
    type,
    timestamp: now(),
    ...payload,
  };
}

function nonEmptyText(value) {
  const text = String(value || "").trim();
  return text || "";
}

function isAuthorized(req) {
  if (!config.authToken) {
    return true;
  }
  const parsed = new URL(req.url || "/", "http://127.0.0.1");
  const queryToken = parsed.searchParams.get("token") || "";
  const auth = req.headers.authorization || "";
  const bearer = auth.toLowerCase().startsWith("bearer ") ? auth.slice(7).trim() : "";
  return queryToken === config.authToken || bearer === config.authToken;
}

function commandAvailable(command) {
  if (path.isAbsolute(command) && fs.existsSync(command)) {
    return true;
  }
  const result = process.platform === "win32"
    ? spawnSync("where.exe", [command], { encoding: "utf8", timeout: 1500 })
    : spawnSync("sh", ["-lc", "command -v " + shellQuote(command)], { encoding: "utf8", timeout: 1500 });
  return result.status === 0;
}

function shellQuote(value) {
  return "'" + String(value).replace(/'/g, "'\\''") + "'";
}

class AcpConnection {
  constructor(options) {
    this.options = options;
    this.child = null;
    this.nextId = 1;
    this.pending = new Map();
    this.initialized = false;
    this.exited = false;
  }

  async start() {
    if (!commandAvailable(this.options.command)) {
      throw new Error("未检测到 Claude Code ACP adapter：" + this.options.command);
    }
    this.child = spawn(this.options.command, this.options.commandArgs, {
      cwd: this.options.defaultCwd,
      stdio: ["pipe", "pipe", "pipe"],
      env: {
        ...process.env,
        FORCE_COLOR: "0",
        ZENMIND_DISABLE_CLAUDE_ISLAND_PERMISSION_HOOK: "1",
      },
    });
    this.child.on("exit", (code, signal) => {
      this.exited = true;
      this.initialized = false;
      const reason = signal ? "signal " + signal : "exit " + String(code ?? 0);
      for (const pending of this.pending.values()) {
        pending.reject(new Error("ACP adapter exited: " + reason));
      }
      this.pending.clear();
      for (const run of activeRuns.values()) {
        run.emit(makeEvent("run.error", {
          runId: run.runId,
          chatId: run.chatId,
          message: "Claude Code ACP adapter exited: " + reason,
        }));
      }
      activeRuns.clear();
      activeSessionRuns.clear();
    });
    this.child.stderr.on("data", (chunk) => {
      process.stderr.write("[claude-code-acp] " + chunk.toString("utf8"));
    });
    readline.createInterface({ input: this.child.stdout }).on("line", (line) => {
      this.handleLine(line);
    });
    await this.request("initialize", {
      protocolVersion: 1,
      clientCapabilities: {
        fs: { readTextFile: false, writeTextFile: false },
        terminal: false,
      },
      clientInfo: {
        name: "zenmind-local-cli-acp-relay",
        title: "ZenMind Local CLI ACP Relay",
        version: "0.1.0",
      },
    }, this.options.handshakeTimeoutMs);
    this.initialized = true;
  }

  handleLine(line) {
    const text = String(line || "").trim();
    if (!text) {
      return;
    }
    let msg;
    try {
      msg = JSON.parse(text);
    } catch {
      process.stderr.write("[acp-relay] invalid stdout json: " + text + "\n");
      return;
    }
    if (Object.prototype.hasOwnProperty.call(msg, "id") && (msg.result !== undefined || msg.error !== undefined)) {
      const pending = this.pending.get(msg.id);
      if (!pending) {
        return;
      }
      this.pending.delete(msg.id);
      clearTimeout(pending.timer);
      if (msg.error) {
        pending.reject(new Error(msg.error.message || JSON.stringify(msg.error)));
      } else {
        pending.resolve(msg.result);
      }
      return;
    }
    if (msg.method === "session/update") {
      handleSessionUpdate(msg.params || {});
      return;
    }
    if (
      msg.method === "session/request_permission"
      || msg.method === "session/requestPermission"
    ) {
      this.handlePermissionRequest(msg).catch((error) => {
        this.respondError(msg.id, -32000, error.message || String(error));
      });
      return;
    }
    if (msg.id !== undefined) {
      this.respondError(msg.id, -32601, "Client method is not implemented: " + String(msg.method || ""));
    }
  }

  async handlePermissionRequest(msg) {
    const params = msg.params || {};
    const sessionId = String(params.sessionId || "");
    const run = activeSessionRuns.get(sessionId);
    if (!run) {
      this.respond(msg.id, { outcome: { outcome: "cancelled" } });
      return;
    }
    const awaitingId = makeId("awaiting");
    const options = Array.isArray(params.options) ? params.options : [];
    const toolCall = params.toolCall && typeof params.toolCall === "object" ? params.toolCall : {};
    const approvalId = String(toolCall.toolCallId || awaitingId);
    const command = nonEmptyText(toolCall.title || toolCall.toolCallId || "Claude Code 请求授权");
    const normalizedOptions = options.map(normalizePermissionOption);
    awaitingById.set(awaitingId, {
      rpcId: msg.id,
      run,
      options,
      toolCall,
      approvalId,
      command,
      normalizedOptions,
    });
    run.emit(makeEvent("awaiting.ask", {
      runId: run.runId,
      chatId: run.chatId,
      awaitingId,
      mode: "approval",
      timeout: config.runTimeoutMs,
      approvals: [
        {
          id: approvalId,
          command,
          description: buildPermissionDescription(toolCall, options),
          options: normalizedOptions.map((option) => ({
            label: option.label,
            decision: option.decision,
            description: option.description,
          })),
          allowFreeText: false,
        },
      ],
    }));
  }

  request(method, params, timeoutMs = 0) {
    if (!this.child || this.exited) {
      return Promise.reject(new Error("ACP adapter is not running"));
    }
    const id = this.nextId++;
    const payload = { jsonrpc: "2.0", id, method, params };
    return new Promise((resolve, reject) => {
      const timer = timeoutMs > 0
        ? setTimeout(() => {
            this.pending.delete(id);
            reject(new Error("ACP request timed out: " + method));
          }, timeoutMs)
        : null;
      this.pending.set(id, { resolve, reject, timer });
      this.child.stdin.write(JSON.stringify(payload) + "\n", "utf8");
    });
  }

  notify(method, params) {
    if (!this.child || this.exited) {
      return false;
    }
    this.child.stdin.write(JSON.stringify({ jsonrpc: "2.0", method, params }) + "\n", "utf8");
    return true;
  }

  respond(id, result) {
    if (!this.child || this.exited || id === undefined) {
      return;
    }
    this.child.stdin.write(JSON.stringify({ jsonrpc: "2.0", id, result }) + "\n", "utf8");
  }

  respondError(id, code, message) {
    if (!this.child || this.exited || id === undefined) {
      return;
    }
    this.child.stdin.write(JSON.stringify({ jsonrpc: "2.0", id, error: { code, message } }) + "\n", "utf8");
  }
}

function buildPermissionDescription(toolCall, options) {
  const pieces = [];
  if (toolCall.kind) {
    pieces.push("类型：" + String(toolCall.kind));
  }
  if (toolCall.status) {
    pieces.push("状态：" + String(toolCall.status));
  }
  if (options.length > 0) {
    pieces.push("可选项：" + options.map((item) => String(item.name || item.optionId || item.kind)).join(" / "));
  }
  return pieces.join("\n");
}

function normalizePermissionOption(option) {
  const optionId = nonEmptyText(option?.optionId || option?.id);
  const label = nonEmptyText(option?.name || optionId || option?.kind || "选择");
  const kind = nonEmptyText(option?.kind).toLowerCase();
  return {
    optionId,
    label,
    kind,
    decision: optionKindToDecision(kind, optionId, label),
    description: nonEmptyText(option?.description || option?.kind || optionId),
  };
}

function optionKindToDecision(kind) {
  const text = String(kind || "").toLowerCase();
  if (text.includes("reject")) {
    return "reject";
  }
  if (text.includes("always") || text.includes("session")) {
    return "approve_prefix_run";
  }
  return "approve";
}

async function ensureAcp() {
  if (acp?.initialized && !acp.exited) {
    return acp;
  }
  acpStartError = "";
  acp = new AcpConnection(config);
  try {
    await acp.start();
    return acp;
  } catch (error) {
    acpStartError = error instanceof Error ? error.message : String(error);
    throw error;
  }
}

async function getOrCreateSession(chatId, cwd) {
  const existing = chatSessions.get(chatId);
  if (existing) {
    return existing;
  }
  const client = await ensureAcp();
  const sessionCwd = resolveSafeCwd(cwd);
  const result = await client.request("session/new", {
    cwd: sessionCwd,
    mcpServers: [],
  }, config.handshakeTimeoutMs);
  const sessionId = String(result?.sessionId || "");
  if (!sessionId) {
    throw new Error("ACP session/new did not return sessionId");
  }
  chatSessions.set(chatId, sessionId);
  return sessionId;
}

function resolveSafeCwd(raw) {
  const target = expandUserPath(raw || config.defaultCwd);
  const resolved = fs.existsSync(target) && fs.statSync(target).isDirectory()
    ? fs.realpathSync(target)
    : fs.realpathSync(config.defaultCwd);
  const allowed = config.allowedCwdRoots.some((root) => {
    const realRoot = fs.existsSync(root) ? fs.realpathSync(root) : path.resolve(root);
    return resolved === realRoot || resolved.startsWith(realRoot + path.sep);
  });
  return allowed ? resolved : fs.realpathSync(config.defaultCwd);
}

async function handleQuery(input, emit) {
  const chatId = String(input.chatId || makeId("chat"));
  const runId = String(input.runId || makeId("run"));
  const message = String(input.message || "");
  if (!message.trim()) {
    throw new Error("message is required");
  }
  const cwd = input.params?.cwd || input.params?.workingDirectory || input.cwd || config.defaultCwd;
  const sessionId = await getOrCreateSession(chatId, cwd);
  const run = createRunState({ runId, chatId, sessionId, emit });
  activeRuns.set(runId, run);
  activeSessionRuns.set(sessionId, run);

  emit(makeEvent("chat.start", { chatId }));
  emit(makeEvent("run.start", { runId, chatId }));

  const timer = setTimeout(() => {
    run.timedOut = true;
    acp?.notify("session/cancel", { sessionId });
    emit(makeEvent("run.error", {
      runId,
      chatId,
      message: "Claude Code ACP run timed out",
    }));
  }, config.runTimeoutMs);

  try {
    const client = await ensureAcp();
    const result = await client.request("session/prompt", {
      sessionId,
      prompt: [{ type: "text", text: message }],
    });
    run.closeOpenBlocks();
    if (!run.cancelled && !run.timedOut) {
      emit(makeEvent("run.complete", {
        runId,
        chatId,
        stopReason: result?.stopReason || result?.reason || "complete",
      }));
    }
  } catch (error) {
    run.closeOpenBlocks();
    if (!run.cancelled && !run.timedOut) {
      emit(makeEvent("run.error", {
        runId,
        chatId,
        message: error instanceof Error ? error.message : String(error),
      }));
    }
  } finally {
    clearTimeout(timer);
    activeRuns.delete(runId);
    if (activeSessionRuns.get(sessionId) === run) {
      activeSessionRuns.delete(sessionId);
    }
  }
}

function createRunState({ runId, chatId, sessionId, emit }) {
  const state = {
    runId,
    chatId,
    sessionId,
    emit,
    contentId: "content_" + runId,
    reasoningId: "reasoning_" + runId,
    contentStarted: false,
    reasoningStarted: false,
    planSeen: false,
    toolArgsSent: new Set(),
    toolEnded: new Set(),
    cancelled: false,
    timedOut: false,
    ensureContent() {
      if (!this.contentStarted) {
        this.contentStarted = true;
        emit(makeEvent("content.start", { runId, chatId, contentId: this.contentId }));
      }
    },
    ensureReasoning() {
      if (!this.reasoningStarted) {
        this.reasoningStarted = true;
        emit(makeEvent("reasoning.start", { runId, chatId, reasoningId: this.reasoningId }));
      }
    },
    closeOpenBlocks() {
      if (this.reasoningStarted) {
        emit(makeEvent("reasoning.end", { runId, chatId, reasoningId: this.reasoningId }));
        this.reasoningStarted = false;
      }
      if (this.contentStarted) {
        emit(makeEvent("content.end", { runId, chatId, contentId: this.contentId }));
        this.contentStarted = false;
      }
    },
  };
  return state;
}

function handleSessionUpdate(params) {
  const sessionId = String(params.sessionId || "");
  const update = params.update || params;
  const run = activeSessionRuns.get(sessionId);
  if (!run || !update || typeof update !== "object") {
    return;
  }
  const kind = String(update.sessionUpdate || update.type || "");
  switch (kind) {
    case "agent_message_chunk":
    case "agentMessageChunk": {
      const text = contentText(update.content ?? update);
      if (!text) return;
      run.ensureContent();
      run.emit(makeEvent("content.delta", {
        runId: run.runId,
        chatId: run.chatId,
        contentId: run.contentId,
        delta: text,
      }));
      break;
    }
    case "thought_message_chunk":
    case "agent_thought_chunk":
    case "agentThoughtChunk": {
      const text = contentText(update.content ?? update);
      if (!text) return;
      run.ensureReasoning();
      run.emit(makeEvent("reasoning.delta", {
        runId: run.runId,
        chatId: run.chatId,
        reasoningId: run.reasoningId,
        delta: text,
      }));
      break;
    }
    case "tool_call":
    case "toolCall":
      emitToolCall(run, update);
      break;
    case "tool_call_update":
    case "toolCallUpdate":
      emitToolCallUpdate(run, update);
      break;
    case "plan":
      emitPlan(run, update);
      break;
    default:
      break;
  }
}

function contentText(value) {
  if (!value) return "";
  if (typeof value === "string") return value;
  if (Array.isArray(value)) {
    return value.map(contentText).join("");
  }
  if (typeof value === "object") {
    if (typeof value.text === "string") return value.text;
    if (typeof value.delta === "string") return value.delta;
    if (value.content) return contentText(value.content);
  }
  return "";
}

function emitToolCall(run, update) {
  const toolId = String(update.toolCallId || update.id || makeId("tool"));
  const toolName = String(update.title || update.kind || "tool");
  run.emit(makeEvent("tool.start", {
    runId: run.runId,
    chatId: run.chatId,
    toolId,
    toolName,
    title: toolName,
    status: update.status || "pending",
  }));
  if (update.rawInput !== undefined) {
    run.toolArgsSent.add(toolId);
    run.emit(makeEvent("tool.args", {
      runId: run.runId,
      chatId: run.chatId,
      toolId,
      delta: JSON.stringify(update.rawInput),
    }));
  }
}

function emitToolCallUpdate(run, update) {
  const toolId = String(update.toolCallId || update.id || "");
  if (!toolId) return;
  if (update.rawInput !== undefined && !run.toolArgsSent.has(toolId)) {
    run.toolArgsSent.add(toolId);
    run.emit(makeEvent("tool.args", {
      runId: run.runId,
      chatId: run.chatId,
      toolId,
      delta: JSON.stringify(update.rawInput),
    }));
  }
  const status = String(update.status || "");
  const resultText = toolResultText(update);
  if (resultText || ["completed", "failed"].includes(status)) {
    run.emit(makeEvent("tool.result", {
      runId: run.runId,
      chatId: run.chatId,
      toolId,
      result: {
        text: resultText || status,
        status: status || "completed",
      },
    }));
  }
  if (["completed", "failed"].includes(status) && !run.toolEnded.has(toolId)) {
    run.toolEnded.add(toolId);
    run.emit(makeEvent("tool.end", {
      runId: run.runId,
      chatId: run.chatId,
      toolId,
      status,
    }));
  }
}

function toolResultText(update) {
  if (update.rawOutput !== undefined) {
    return typeof update.rawOutput === "string" ? update.rawOutput : JSON.stringify(update.rawOutput);
  }
  if (Array.isArray(update.content)) {
    return update.content.map(contentText).filter(Boolean).join("\n");
  }
  return "";
}

function emitPlan(run, update) {
  const type = run.planSeen ? "plan.update" : "plan.create";
  run.planSeen = true;
  run.emit(makeEvent(type, {
    runId: run.runId,
    chatId: run.chatId,
    items: Array.isArray(update.entries) ? update.entries : [],
  }));
}

function handleSubmit(input, emit) {
  const awaitingId = String(input.awaitingId || "");
  const pending = awaitingById.get(awaitingId);
  if (!pending) {
    return { accepted: false, status: "unmatched", detail: "No pending ACP permission request" };
  }
  awaitingById.delete(awaitingId);
  const params = Array.isArray(input.params) ? input.params : [];
  emit(makeEvent("request.submit", {
    runId: pending.run.runId,
    chatId: pending.run.chatId,
    awaitingId,
    params,
  }));
  const selected = selectPermissionOption(params[0] || {}, pending);
  const isCancelled = !selected?.optionId;
  const decision = selected?.optionId
    ? { outcome: { outcome: "selected", optionId: selected.optionId } }
    : { outcome: { outcome: "cancelled" } };
  acp?.respond(pending.rpcId, decision);
  emit(makeEvent("awaiting.answer", {
    runId: pending.run.runId,
    chatId: pending.run.chatId,
    awaitingId,
    mode: "approval",
    status: isCancelled ? "error" : "answered",
    ...(isCancelled
      ? {
          error: {
            code: "user_dismissed",
            message: "用户未批准本次操作",
          },
        }
      : {
          approvals: [
            {
              id: pending.approvalId,
              command: pending.command,
              decision: selected.decision,
              rawDecision: selected.rawDecision,
              ...(selected.reason ? { reason: selected.reason } : {}),
            },
          ],
        }),
  }));
  return {
    accepted: true,
    status: isCancelled ? "dismissed" : "accepted",
    detail: "Permission response forwarded",
  };
}

function selectPermissionOption(item, pending) {
  const rawDecision = nonEmptyText(item?.decision).toLowerCase();
  const reason = nonEmptyText(item?.reason);
  const optionId = nonEmptyText(item?.optionId);
  if (optionId) {
    const matched = pending.options.find((option) => option.optionId === optionId);
    const normalized = pending.normalizedOptions.find((option) => option.optionId === optionId);
    return {
      optionId,
      decision: normalized?.decision || rawDecision || "approve",
      rawDecision: rawDecision || normalized?.decision || "approve",
      reason,
      option: matched || null,
    };
  }
  if (!rawDecision) {
    return null;
  }
  const normalized = pending.normalizedOptions.find((option) => option.decision === rawDecision)
    || pending.normalizedOptions.find((option) => {
      if (rawDecision === "approve_prefix_run") {
        return option.kind.includes("always") || option.kind.includes("session");
      }
      if (rawDecision === "approve") {
        return option.kind.includes("allow");
      }
      if (rawDecision === "reject") {
        return option.kind.includes("reject");
      }
      return false;
    })
    || null;
  if (!normalized?.optionId) {
    return null;
  }
  return {
    optionId: normalized.optionId,
    decision: normalized.decision,
    rawDecision,
    reason,
    option: pending.options.find((option) => option.optionId === normalized.optionId) || null,
  };
}

function handleInterrupt(input, emit) {
  const runId = String(input.runId || "");
  const run = activeRuns.get(runId);
  if (!run) {
    return { accepted: false, status: "unmatched", detail: "No active run" };
  }
  acp?.notify("session/cancel", { sessionId: run.sessionId });
  run.cancelled = true;
  run.closeOpenBlocks();
  emit(makeEvent("run.cancel", {
    runId: run.runId,
    chatId: run.chatId,
    reason: "user_cancelled",
  }));
  activeRuns.delete(runId);
  if (activeSessionRuns.get(run.sessionId) === run) {
    activeSessionRuns.delete(run.sessionId);
  }
  return { accepted: true, status: "accepted", detail: "Cancel forwarded" };
}

class WsPeer {
  constructor(socket) {
    this.socket = socket;
    this.buffer = Buffer.alloc(0);
    this.closed = false;
    socket.on("data", (chunk) => this.onData(chunk));
    socket.on("close", () => {
      this.closed = true;
      clients.delete(this);
    });
    socket.on("error", () => {
      this.closed = true;
      clients.delete(this);
    });
  }

  send(value) {
    if (this.closed) return;
    const payload = Buffer.from(JSON.stringify(value), "utf8");
    let header;
    if (payload.length < 126) {
      header = Buffer.from([0x81, payload.length]);
    } else if (payload.length < 65536) {
      header = Buffer.alloc(4);
      header[0] = 0x81;
      header[1] = 126;
      header.writeUInt16BE(payload.length, 2);
    } else {
      header = Buffer.alloc(10);
      header[0] = 0x81;
      header[1] = 127;
      header.writeBigUInt64BE(BigInt(payload.length), 2);
    }
    this.socket.write(Buffer.concat([header, payload]));
  }

  onData(chunk) {
    this.buffer = Buffer.concat([this.buffer, chunk]);
    for (;;) {
      const frame = readWsFrame(this.buffer);
      if (!frame) return;
      this.buffer = this.buffer.slice(frame.bytes);
      if (frame.opcode === 0x8) {
        this.socket.end();
        return;
      }
      if (frame.opcode === 0x9) {
        this.socket.write(Buffer.from([0x8a, 0x00]));
        continue;
      }
      if (frame.opcode !== 0x1) {
        continue;
      }
      let msg;
      try {
        msg = JSON.parse(frame.payload.toString("utf8"));
      } catch (error) {
        this.send(makeEvent("run.error", { message: "invalid websocket json: " + error.message }));
        continue;
      }
      handleInboundMessage(msg, (event) => this.send(event)).catch((error) => {
        this.send(makeEvent("run.error", { message: error instanceof Error ? error.message : String(error) }));
      });
    }
  }
}

function readWsFrame(buffer) {
  if (buffer.length < 2) return null;
  const opcode = buffer[0] & 0x0f;
  const masked = (buffer[1] & 0x80) !== 0;
  let len = buffer[1] & 0x7f;
  let offset = 2;
  if (len === 126) {
    if (buffer.length < 4) return null;
    len = buffer.readUInt16BE(2);
    offset = 4;
  } else if (len === 127) {
    if (buffer.length < 10) return null;
    len = Number(buffer.readBigUInt64BE(2));
    offset = 10;
  }
  const maskOffset = offset;
  if (masked) offset += 4;
  if (buffer.length < offset + len) return null;
  const payload = Buffer.from(buffer.slice(offset, offset + len));
  if (masked) {
    const mask = buffer.slice(maskOffset, maskOffset + 4);
    for (let index = 0; index < payload.length; index += 1) {
      payload[index] ^= mask[index % 4];
    }
  }
  return { opcode, payload, bytes: offset + len };
}

async function handleInboundMessage(msg, emit) {
  const type = String(msg.type || "");
  if (type === "request.query") {
    await handleQuery(msg, emit);
    return;
  }
  if (type === "request.submit" || type === "awaiting.answer") {
    const result = handleSubmit(msg, emit);
    if (!result.accepted) {
      emit(makeEvent("run.error", { runId: msg.runId, chatId: msg.chatId, message: result.detail }));
    }
    return;
  }
  if (type === "request.interrupt" || type === "request.steer") {
    handleInterrupt(msg, emit);
  }
}

function broadcast(event) {
  for (const client of clients) {
    client.send(event);
  }
}

function writeSse(res, event) {
  res.write("data: " + JSON.stringify(event) + "\n\n");
}

const server = http.createServer((req, res) => {
  const parsed = new URL(req.url || "/", "http://127.0.0.1");
  if (parsed.pathname === "/api/status") {
    if (!isAuthorized(req)) {
      res.writeHead(401, { "content-type": "application/json" });
      res.end(JSON.stringify({ code: 401, msg: "unauthorized", data: {} }));
      return;
    }
    res.writeHead(200, { "content-type": "application/json" });
    res.end(JSON.stringify({
      code: 0,
      msg: "success",
      data: {
        provider: "claude-code",
        cli_connected: Boolean(acp?.initialized && !acp.exited),
        adapter_command: config.command,
        adapter_available: commandAvailable(config.command),
        active_runs: activeRuns.size,
        last_error: acpStartError,
      },
    }));
    return;
  }
  if (parsed.pathname === "/api/query" && req.method === "POST") {
    if (!isAuthorized(req)) {
      res.writeHead(401, { "content-type": "application/json" });
      res.end(JSON.stringify({ code: 401, msg: "unauthorized", data: {} }));
      return;
    }
    let body = "";
    req.on("data", (chunk) => {
      body += chunk.toString("utf8");
    });
    req.on("end", () => {
      res.writeHead(200, {
        "content-type": "text/event-stream",
        "cache-control": "no-cache",
        connection: "keep-alive",
      });
      let input;
      try {
        input = JSON.parse(body || "{}");
      } catch (error) {
        writeSse(res, makeEvent("run.error", { message: "invalid json: " + error.message }));
        res.end();
        return;
      }
      handleQuery(input, (event) => writeSse(res, event))
        .catch((error) => writeSse(res, makeEvent("run.error", { message: error.message || String(error) })))
        .finally(() => {
          res.write("data: [DONE]\n\n");
          res.end();
        });
    });
    return;
  }
  res.writeHead(404, { "content-type": "application/json" });
  res.end(JSON.stringify({ code: 404, msg: "not found", data: {} }));
});

server.on("upgrade", (req, socket) => {
  const parsed = new URL(req.url || "/", "http://127.0.0.1");
  if (parsed.pathname !== "/ws" || !isAuthorized(req)) {
    socket.write("HTTP/1.1 401 Unauthorized\r\n\r\n");
    socket.destroy();
    return;
  }
  const key = req.headers["sec-websocket-key"];
  if (!key) {
    socket.write("HTTP/1.1 400 Bad Request\r\n\r\n");
    socket.destroy();
    return;
  }
  const accept = crypto
    .createHash("sha1")
    .update(String(key) + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11")
    .digest("base64");
  socket.write(
    "HTTP/1.1 101 Switching Protocols\r\n" +
    "Upgrade: websocket\r\n" +
    "Connection: Upgrade\r\n" +
    "Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
  );
  const peer = new WsPeer(socket);
  clients.add(peer);
});

server.listen(config.port, "127.0.0.1", async () => {
  console.log("[acp-relay] listening on http://127.0.0.1:" + config.port);
  try {
    await ensureAcp();
    console.log("[acp-relay] Claude Code ACP adapter initialized");
  } catch (error) {
    console.error("[acp-relay] Claude Code ACP adapter unavailable:", error.message || String(error));
  }
});
