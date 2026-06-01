(function () {
  "use strict";

  var ENDPOINTS = {
    overview: "/api/monitor?messageLimit=50",
    connections: "/api/monitor/ws/connections?limit=100",
    messages: "/api/monitor/ws/messages?limit=50",
  };
  var TOKEN_STORAGE_KEY = "agent-platform.monitor.accessToken";

  var state = {
    loading: false,
    accessToken: "",
    selectedSessionId: "",
    connectionView: "active",
    knownSessionIds: [],
    overview: null,
    connectionsSnapshot: null,
    messagesSnapshot: null,
  };

  var dom = {};

  document.addEventListener("DOMContentLoaded", function () {
    dom = {
      tokenInput: document.getElementById("token-input"),
      applyToken: document.getElementById("apply-token"),
      clearToken: document.getElementById("clear-token"),
      sessionSelect: document.getElementById("session-select"),
      clearFilter: document.getElementById("clear-filter"),
      connectionViewInputs: document.querySelectorAll('input[name="connection-view"]'),
      refreshButton: document.getElementById("refresh-button"),
      pageSubtitle: document.getElementById("page-subtitle"),
      loadingState: document.getElementById("loading-state"),
      errorBanner: document.getElementById("error-banner"),
      generatedAt: document.getElementById("generated-at"),
      overviewGrid: document.getElementById("overview-grid"),
      connectionsCount: document.getElementById("connections-count"),
      connectionsBody: document.getElementById("connections-body"),
      connectionsEmpty: document.getElementById("connections-empty"),
      messagesCount: document.getElementById("messages-count"),
      messagesBody: document.getElementById("messages-body"),
      messagesEmpty: document.getElementById("messages-empty"),
    };

    initializeAccessToken();
    dom.applyToken.addEventListener("click", function () {
      applyAccessToken();
    });
    dom.clearToken.addEventListener("click", function () {
      clearAccessToken();
    });
    dom.tokenInput.addEventListener("keydown", function (event) {
      if (event.key === "Enter") {
        applyAccessToken();
      }
    });
    dom.refreshButton.addEventListener("click", function () {
      loadDashboard();
    });
    dom.clearFilter.addEventListener("click", function () {
      state.selectedSessionId = "";
      dom.sessionSelect.value = "";
      loadDashboard();
    });
    dom.sessionSelect.addEventListener("change", function (event) {
      state.selectedSessionId = event.target.value;
      loadDashboard();
    });
    dom.connectionViewInputs.forEach(function (input) {
      input.addEventListener("change", function (event) {
        if (!event.target.checked) {
          return;
        }
        state.connectionView = event.target.value === "all" ? "all" : "active";
        renderAll();
      });
    });

    setText(dom.pageSubtitle, window.location.host + " /monitor");
    renderEmptyShell();
    loadDashboard();
  });

  async function requestJSON(url) {
    var response;
    var headers = { Accept: "application/json" };
    var token = normalizedAccessToken(state.accessToken);
    if (token) {
      headers.Authorization = "Bearer " + token;
    }
    try {
      response = await fetch(url, {
        headers: headers,
        cache: "no-store",
      });
    } catch (error) {
      throw new Error("请求失败：" + readableError(error));
    }

    var envelope;
    try {
      envelope = await response.json();
    } catch (error) {
      throw new Error("接口返回了无法解析的 JSON：" + url);
    }

    if (!response.ok) {
      var httpMessage = envelope && envelope.msg ? envelope.msg : response.statusText;
      if (response.status === 401) {
        httpMessage = "Unauthorized。请在顶部输入 access_token 后点击“应用 token”。";
      }
      throw new Error("HTTP " + response.status + "：" + httpMessage);
    }
    if (!envelope || typeof envelope !== "object" || !Object.prototype.hasOwnProperty.call(envelope, "code")) {
      throw new Error("接口返回格式不符合 { code, msg, data }：" + url);
    }
    if (envelope.code !== 0) {
      throw new Error(envelope.msg || "接口返回 code=" + envelope.code);
    }
    return envelope.data || {};
  }

  async function loadDashboard() {
    setLoading(true);
    setError("");

    try {
      var sessionId = state.selectedSessionId;
      var overviewURL = ENDPOINTS.overview;
      var connectionsURL = withSession(ENDPOINTS.connections, sessionId);
      var messagesURL = withSession(ENDPOINTS.messages, sessionId);
      var results = await Promise.all([
        requestJSON(overviewURL),
        requestJSON(connectionsURL),
        requestJSON(messagesURL),
      ]);

      state.overview = results[0] || {};
      state.connectionsSnapshot = results[1] || {};
      state.messagesSnapshot = results[2] || {};
      updateKnownSessionIds();
      renderAll();
    } catch (error) {
      setError(readableError(error));
    } finally {
      setLoading(false);
    }
  }

  function withSession(url, sessionId) {
    if (!sessionId) {
      return url;
    }
    var separator = url.indexOf("?") === -1 ? "?" : "&";
    return url + separator + "sessionId=" + encodeURIComponent(sessionId);
  }

  function renderEmptyShell() {
    renderOverview({});
    renderSessionSelect();
    renderConnectionViewControls();
    renderConnections([], 0);
    renderMessages([]);
  }

  function renderAll() {
    var connections = getConnections();
    var messages = getMessages();
    renderOverview(state.overview || {});
    renderSessionSelect();
    renderConnectionViewControls();
    renderConnections(getVisibleConnections(connections), connections.length);
    renderMessages(messages);
  }

  function renderOverview(overview) {
    var ws = objectValue(overview.ws);
    var connections = getConnections();
    var messages = getMessages();
    var latestConnection = objectValue(ws.latestConnection);
    var generatedAt = firstValue(
      overview.generatedAt,
      state.connectionsSnapshot && state.connectionsSnapshot.generatedAt,
      state.messagesSnapshot && state.messagesSnapshot.generatedAt
    );

    setText(dom.generatedAt, generatedAt ? "生成时间 " + formatTime(generatedAt) : "未加载");

    var totalSessions = state.knownSessionIds.length || collectSessionIds(connections, messages).length;
    var recentMessageCount = messages.length || arrayValue(ws.recentMessages).length;
    var onlineConnections = firstValue(ws.connectionCount, state.connectionsSnapshot && state.connectionsSnapshot.connectionCount, 0);
    var status = generatedAt ? "正常" : "未加载";

    var metrics = [
      { label: "服务状态", value: status, tone: generatedAt ? "ok" : "warn" },
      { label: "在线连接数", value: onlineConnections },
      { label: "会话数", value: totalSessions },
      { label: "最近消息数", value: recentMessageCount },
      { label: "启动时间", value: formatOptionalTime(firstValue(overview.startedAt, overview.startTime, overview.bootTime)) },
      { label: "运行时长", value: formatDurationValue(firstValue(overview.uptimeMs, overview.uptime, overview.durationMs)) },
      { label: "最新连接", value: latestConnection.sessionId || "未提供" },
      { label: "连接快照", value: connections.length + " 条" },
      { label: "消息快照", value: messages.length + " 条" },
      { label: "overview.generatedAt", value: generatedAt || "未提供" },
      { label: "ws.recentMessages", value: arrayValue(ws.recentMessages).length },
      { label: "筛选 session", value: state.selectedSessionId || "全部" },
    ];

    dom.overviewGrid.replaceChildren();
    metrics.forEach(function (metric) {
      var item = create("div", "metric");
      var label = create("div", "metric-label");
      var value = create("div", "metric-value");
      if (metric.tone === "ok") {
        value.classList.add("ok");
      } else if (metric.tone === "warn") {
        value.classList.add("warn");
      }
      setText(label, metric.label);
      setText(value, normalizeDisplayValue(metric.value));
      item.append(label, value);
      dom.overviewGrid.appendChild(item);
    });
  }

  function renderSessionSelect() {
    var previous = state.selectedSessionId;
    var options = [optionElement("", "全部 session")];
    state.knownSessionIds.forEach(function (sessionId) {
      options.push(optionElement(sessionId, sessionId));
    });
    dom.sessionSelect.replaceChildren.apply(dom.sessionSelect, options);
    if (previous && state.knownSessionIds.indexOf(previous) === -1) {
      dom.sessionSelect.appendChild(optionElement(previous, previous + "（当前筛选）"));
    }
    dom.sessionSelect.value = previous;
    dom.clearFilter.disabled = !previous || state.loading;
  }

  function renderConnectionViewControls() {
    dom.connectionViewInputs.forEach(function (input) {
      input.checked = input.value === state.connectionView;
      input.disabled = state.loading;
    });
  }

  function renderConnections(connections, totalCount) {
    var total = typeof totalCount === "number" ? totalCount : connections.length;
    dom.connectionsBody.replaceChildren();
    if (state.connectionView === "active") {
      setText(dom.connectionsCount, "active " + connections.length + " / all " + total + " 条");
      setText(dom.connectionsEmpty, "暂无 active 连接");
    } else {
      setText(dom.connectionsCount, "all " + total + " 条");
      setText(dom.connectionsEmpty, "暂无连接");
    }
    dom.connectionsEmpty.hidden = connections.length !== 0;

    connections.forEach(function (connection) {
      var row = document.createElement("tr");
      appendCell(row, connection.sessionId, "mono");
      appendCell(row, firstValue(connection.connectionId, connection.connId, connection.id, connection.sessionId), "mono");
      appendStackCell(row, [
        ["kind", connection.kind],
        ["subject", connection.subject],
        ["gateway", connection.gatewayId],
        ["channel", connection.channel],
        ["source", connection.source],
        ["device", connection.deviceId],
      ], connection.userAgent);
      appendCell(row, formatOptionalTime(connection.connectedAt), "mono");
      appendCell(row, formatOptionalTime(firstValue(connection.lastSeenAt, connection.lastMessageAt, connection.closedAt)), "mono");
      appendStatusCell(row, connection);
      appendCell(row, firstValue(connection.remoteAddress, connection.remoteAddr, connection.addr), "mono");
      appendStackCell(row, [
        ["in", connection.receivedMessages],
        ["out", connection.sentMessages],
        ["errors", connection.errors],
      ]);
      appendStackCell(row, [
        ["inflight", connection.inflightRequests],
        ["streams", connection.activeStreams],
        ["queue", connection.writeQueueDepth],
      ]);
      dom.connectionsBody.appendChild(row);
    });
  }

  function renderMessages(messages) {
    dom.messagesBody.replaceChildren();
    setText(dom.messagesCount, messages.length + " 条");
    dom.messagesEmpty.hidden = messages.length !== 0;

    messages.forEach(function (message) {
      var row = document.createElement("tr");
      appendCell(row, formatOptionalTime(firstValue(message.time, message.createdAt, message.timestamp)), "mono");
      appendCell(row, message.sessionId, "mono");
      appendDirectionCell(row, message.direction);
      appendCell(row, firstValue(message.type, message.frame), "mono");
      appendStackCell(row, [
        ["frame", message.frame],
        ["id", message.id],
        ["size", formatBytes(message.sizeBytes)],
        ["error", message.error],
      ]);
      appendStackCell(row, [
        ["event", message.event],
        ["topic", message.topic],
      ]);
      appendPayloadCell(row, message);
      dom.messagesBody.appendChild(row);
    });
  }

  function appendCell(row, value, className) {
    var cell = document.createElement("td");
    if (className) {
      cell.className = className;
    }
    setText(cell, normalizeDisplayValue(value));
    row.appendChild(cell);
  }

  function appendStackCell(row, items, userAgent) {
    var cell = document.createElement("td");
    var stack = create("div", "stack");
    items.forEach(function (item) {
      var label = item[0];
      var value = normalizeDisplayValue(item[1]);
      if (value === "-") {
        return;
      }
      var line = create("div", "mono");
      setText(line, label + ": " + value);
      stack.appendChild(line);
    });
    if (!stack.childElementCount) {
      setText(stack, "-");
    }
    cell.appendChild(stack);
    appendUserAgentControls(cell, userAgent);
    row.appendChild(cell);
  }

  function appendUserAgentControls(cell, userAgent) {
    var normalizedUserAgent = normalizeDisplayValue(userAgent);
    if (normalizedUserAgent === "-") {
      return;
    }
    cell.title = normalizedUserAgent;
    var copyButton = create("button", "copy-button copy-user-agent");
    copyButton.type = "button";
    copyButton.title = "复制完整 userAgent";
    setText(copyButton, "复制 UA");
    copyButton.addEventListener("click", function () {
      copyUserAgentText(normalizedUserAgent, copyButton, cell);
    });
    cell.appendChild(copyButton);
  }

  function copyUserAgentText(text, button, sourceElement) {
    copyPayloadText(text, button, sourceElement);
  }

  function appendStatusCell(row, connection) {
    var cell = document.createElement("td");
    var chip = create("span", "chip");
    var active = isActiveConnection(connection);
    chip.classList.add(active ? "chip-active" : "chip-closed");
    setText(chip, active ? "active" : firstValue(connection.status, connection.state, "closed"));
    cell.appendChild(chip);
    if (connection.closedAt) {
      var closedAt = create("div", "mono");
      setText(closedAt, "closedAt: " + formatTime(connection.closedAt));
      cell.appendChild(closedAt);
    }
    row.appendChild(cell);
  }

  function appendDirectionCell(row, direction) {
    var cell = document.createElement("td");
    var chip = create("span", "chip");
    var normalized = String(direction || "-");
    if (normalized === "in") {
      chip.classList.add("chip-in");
    } else if (normalized === "out") {
      chip.classList.add("chip-out");
    }
    setText(chip, normalized);
    cell.appendChild(chip);
    row.appendChild(cell);
  }

  function appendPayloadCell(row, message) {
    var cell = document.createElement("td");
    var fullPayload = firstValue(message.payload, message.data, message.body, "");
    var raw = firstValue(fullPayload, message.payloadPreview, "");
    var text = stringifyPayload(raw);

    if (!text) {
      setText(cell, "-");
      row.appendChild(cell);
      return;
    }

    var wrapper = create("div", "payload-cell");
    var summary = create("span", "payload-summary");
    var copyButton = create("button", "copy-button");
    copyButton.type = "button";
    setText(summary, text);
    summary.title = summarize(text, 1200);
    setText(copyButton, "复制");
    copyButton.addEventListener("click", function () {
      copyPayloadText(text, copyButton, summary);
    });
    wrapper.append(summary, copyButton);
    cell.appendChild(wrapper);
    row.appendChild(cell);
  }

  async function copyPayloadText(text, button, sourceElement) {
    try {
      if (navigator.clipboard && navigator.clipboard.writeText) {
        await navigator.clipboard.writeText(text);
        markCopyState(button, "已复制", true);
        return;
      }
      var fallbackState = copyTextFallback(text, sourceElement);
      if (fallbackState === "copied") {
        markCopyState(button, "已复制", true);
      } else if (fallbackState === "selected") {
        markCopyState(button, "已选中", true);
      } else {
        throw new Error("clipboard unavailable");
      }
    } catch (error) {
      if (copyTextFallback(text, sourceElement) === "selected") {
        markCopyState(button, "已选中", true);
        return;
      }
      markCopyState(button, "复制失败", false);
    }
  }

  function copyTextFallback(text, sourceElement) {
    var textarea = document.createElement("textarea");
    textarea.value = text;
    textarea.setAttribute("readonly", "readonly");
    textarea.style.position = "fixed";
    textarea.style.top = "-1000px";
    textarea.style.left = "-1000px";
    document.body.appendChild(textarea);
    textarea.select();
    try {
      if (document.execCommand && document.execCommand("copy")) {
        textarea.remove();
        return "copied";
      }
    } catch (error) {
      // Fall through to selecting the visible text for manual copy.
    }
    textarea.remove();
    return selectElementText(sourceElement) ? "selected" : "";
  }

  function selectElementText(element) {
    if (!element || !window.getSelection || !document.createRange) {
      return false;
    }
    var range = document.createRange();
    range.selectNodeContents(element);
    var selection = window.getSelection();
    selection.removeAllRanges();
    selection.addRange(range);
    return true;
  }

  function markCopyState(button, label, ok) {
    button.classList.toggle("copied", ok);
    setText(button, label);
    window.setTimeout(function () {
      button.classList.remove("copied");
      setText(button, "复制");
    }, 1200);
  }

  function updateKnownSessionIds() {
    var seen = new Set(state.knownSessionIds);
    collectSessionIds(getConnections(), getMessages()).forEach(function (sessionId) {
      seen.add(sessionId);
    });
    state.knownSessionIds = Array.from(seen).sort();
  }

  function collectSessionIds(connections, messages) {
    var seen = new Set();
    connections.forEach(function (item) {
      if (item && item.sessionId) {
        seen.add(String(item.sessionId));
      }
    });
    messages.forEach(function (item) {
      if (item && item.sessionId) {
        seen.add(String(item.sessionId));
      }
    });
    return Array.from(seen).sort();
  }

  function getConnections() {
    var snapshot = state.connectionsSnapshot || {};
    return arrayValue(firstValue(snapshot.connections, snapshot.items, snapshot.list));
  }

  function getVisibleConnections(connections) {
    if (state.connectionView === "all") {
      return connections;
    }
    return connections.filter(isActiveConnection);
  }

  function isActiveConnection(connection) {
    if (!connection || typeof connection !== "object") {
      return false;
    }
    var explicit = firstValue(connection.active, connection.online, connection.connected);
    var parsed = parseBooleanFlag(explicit);
    if (parsed !== null) {
      return parsed;
    }
    var status = String(firstValue(connection.status, connection.state, connection.readyState, "")).toLowerCase();
    return ["active", "open", "connected", "online", "ready", "running"].indexOf(status) !== -1;
  }

  function parseBooleanFlag(value) {
    if (value === undefined || value === null || value === "") {
      return null;
    }
    if (typeof value === "boolean") {
      return value;
    }
    if (typeof value === "number") {
      return value !== 0;
    }
    var normalized = String(value).toLowerCase();
    if (["true", "1", "yes", "y", "active", "online", "connected", "open"].indexOf(normalized) !== -1) {
      return true;
    }
    if (["false", "0", "no", "n", "closed", "offline", "disconnected"].indexOf(normalized) !== -1) {
      return false;
    }
    return null;
  }

  function getMessages() {
    var snapshot = state.messagesSnapshot || {};
    return arrayValue(firstValue(snapshot.messages, snapshot.items, snapshot.list));
  }

  function setLoading(loading) {
    state.loading = loading;
    if (!dom.loadingState) {
      return;
    }
    dom.loadingState.hidden = !loading;
    dom.refreshButton.disabled = loading;
    dom.sessionSelect.disabled = loading;
    dom.clearFilter.disabled = loading || !state.selectedSessionId;
    setTokenControls();
    renderConnectionViewControls();
  }

  function initializeAccessToken() {
    var urlToken = readUrlAccessToken();
    if (urlToken) {
      state.accessToken = urlToken;
      storeToken(urlToken);
    } else {
      state.accessToken = loadStoredToken();
    }
    dom.tokenInput.value = state.accessToken;
    setTokenControls();
    scrubAccessTokenFromUrl();
  }

  function applyAccessToken() {
    state.accessToken = normalizedAccessToken(dom.tokenInput.value);
    dom.tokenInput.value = state.accessToken;
    storeToken(state.accessToken);
    setTokenControls();
    loadDashboard();
  }

  function clearAccessToken() {
    state.accessToken = "";
    dom.tokenInput.value = "";
    storeToken("");
    setTokenControls();
    loadDashboard();
  }

  function setTokenControls() {
    dom.tokenInput.disabled = state.loading;
    dom.applyToken.disabled = state.loading;
    dom.clearToken.disabled = state.loading || !state.accessToken;
  }

  function normalizedAccessToken(value) {
    var token = String(value || "").trim();
    if (token.toLowerCase().indexOf("bearer ") === 0) {
      token = token.slice(7).trim();
    }
    return token;
  }

  function readUrlAccessToken() {
    try {
      var params = new URLSearchParams(window.location.search);
      return normalizedAccessToken(params.get("access_token"));
    } catch (error) {
      return "";
    }
  }

  function scrubAccessTokenFromUrl() {
    try {
      var url = new URL(window.location.href);
      if (!url.searchParams.has("access_token")) {
        return;
      }
      url.searchParams.delete("access_token");
      var nextPath = url.pathname + url.search + url.hash;
      window.history.replaceState(window.history.state, document.title, nextPath);
    } catch (error) {
      // The token has already been copied to memory; URL cleanup is best effort.
    }
  }

  function loadStoredToken() {
    try {
      return normalizedAccessToken(window.sessionStorage.getItem(TOKEN_STORAGE_KEY));
    } catch (error) {
      return "";
    }
  }

  function storeToken(token) {
    try {
      if (token) {
        window.sessionStorage.setItem(TOKEN_STORAGE_KEY, token);
      } else {
        window.sessionStorage.removeItem(TOKEN_STORAGE_KEY);
      }
    } catch (error) {
      // The token still works for the current page even if storage is blocked.
    }
  }

  function setError(message) {
    if (!message) {
      dom.errorBanner.hidden = true;
      setText(dom.errorBanner, "");
      return;
    }
    dom.errorBanner.hidden = false;
    setText(dom.errorBanner, message);
  }

  function optionElement(value, label) {
    var option = document.createElement("option");
    option.value = value;
    setText(option, label);
    return option;
  }

  function create(tagName, className) {
    var element = document.createElement(tagName);
    if (className) {
      element.className = className;
    }
    return element;
  }

  function setText(element, value) {
    element.textContent = normalizeDisplayValue(value);
  }

  function objectValue(value) {
    return value && typeof value === "object" && !Array.isArray(value) ? value : {};
  }

  function arrayValue(value) {
    return Array.isArray(value) ? value : [];
  }

  function firstValue() {
    for (var i = 0; i < arguments.length; i += 1) {
      var value = arguments[i];
      if (value !== undefined && value !== null && value !== "") {
        return value;
      }
    }
    return "";
  }

  function normalizeDisplayValue(value) {
    if (value === undefined || value === null || value === "") {
      return "-";
    }
    if (typeof value === "boolean") {
      return value ? "true" : "false";
    }
    return String(value);
  }

  function readableError(error) {
    if (!error) {
      return "未知错误";
    }
    return error.message || String(error);
  }

  function formatOptionalTime(value) {
    return value ? formatTime(value) : "未提供";
  }

  function formatTime(value) {
    var date;
    if (typeof value === "number") {
      date = new Date(value > 100000000000 ? value : value * 1000);
    } else {
      date = new Date(value);
    }
    if (Number.isNaN(date.getTime())) {
      return String(value);
    }
    return date.toLocaleString();
  }

  function formatDurationValue(value) {
    if (value === undefined || value === null || value === "") {
      return "未提供";
    }
    if (typeof value === "number") {
      return formatDuration(value);
    }
    return String(value);
  }

  function formatDuration(milliseconds) {
    var totalSeconds = Math.floor(milliseconds / 1000);
    var days = Math.floor(totalSeconds / 86400);
    var hours = Math.floor((totalSeconds % 86400) / 3600);
    var minutes = Math.floor((totalSeconds % 3600) / 60);
    var seconds = totalSeconds % 60;
    var parts = [];
    if (days) {
      parts.push(days + "d");
    }
    if (hours || parts.length) {
      parts.push(hours + "h");
    }
    if (minutes || parts.length) {
      parts.push(minutes + "m");
    }
    parts.push(seconds + "s");
    return parts.join(" ");
  }

  function formatBytes(value) {
    if (value === undefined || value === null || value === "") {
      return "";
    }
    var bytes = Number(value);
    if (!Number.isFinite(bytes)) {
      return value;
    }
    if (bytes < 1024) {
      return bytes + " B";
    }
    if (bytes < 1024 * 1024) {
      return (bytes / 1024).toFixed(1) + " KB";
    }
    return (bytes / 1024 / 1024).toFixed(1) + " MB";
  }

  function stringifyPayload(value) {
    if (value === undefined || value === null) {
      return "";
    }
    if (typeof value === "string") {
      return compactJSONString(value);
    }
    try {
      return JSON.stringify(value);
    } catch (error) {
      return String(value);
    }
  }

  function compactJSONString(value) {
    try {
      return JSON.stringify(JSON.parse(value));
    } catch (error) {
      return value;
    }
  }

  function summarize(text, maxLength) {
    if (text.length <= maxLength) {
      return text;
    }
    return text.slice(0, maxLength - 1) + "...";
  }
})();
