(function () {
  "use strict";

  var ENDPOINTS = {
    overview: "/api/monitor?messageLimit=50",
    channels: "/api/monitor/channels",
    connections: "/api/monitor/ws/connections?limit=100",
    messages: "/api/monitor/ws/messages?limit=50",
  };

  var state = {
    loading: false,
    selectedSessionId: "",
    connectionView: "active",
    channelModeFilter: "",
    channelStatusFilter: "",
    channelSearch: "",
    selectedChannelId: "",
    knownSessionIds: [],
    overview: null,
    channelsSnapshot: null,
    channelsError: "",
    connectionsSnapshot: null,
    messagesSnapshot: null,
  };

  var dom = {};

  document.addEventListener("DOMContentLoaded", function () {
    dom = {
      sessionSelect: document.getElementById("session-select"),
      clearFilter: document.getElementById("clear-filter"),
      connectionViewInputs: document.querySelectorAll('input[name="connection-view"]'),
      refreshButton: document.getElementById("refresh-button"),
      loadingState: document.getElementById("loading-state"),
      errorBanner: document.getElementById("error-banner"),
      generatedAt: document.getElementById("generated-at"),
      overviewGrid: document.getElementById("overview-grid"),
      channelsCount: document.getElementById("channels-count"),
      channelsError: document.getElementById("channels-error"),
      channelsSummary: document.getElementById("channels-summary"),
      channelModeFilter: document.getElementById("channel-mode-filter"),
      channelStatusFilter: document.getElementById("channel-status-filter"),
      channelSearch: document.getElementById("channel-search"),
      channelsBody: document.getElementById("channels-body"),
      channelsEmpty: document.getElementById("channels-empty"),
      channelDetail: document.getElementById("channel-detail"),
      connectionsCount: document.getElementById("connections-count"),
      connectionsBody: document.getElementById("connections-body"),
      connectionsEmpty: document.getElementById("connections-empty"),
      messagesCount: document.getElementById("messages-count"),
      messagesBody: document.getElementById("messages-body"),
      messagesEmpty: document.getElementById("messages-empty"),
    };

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
    dom.channelModeFilter.addEventListener("change", function (event) {
      state.channelModeFilter = event.target.value;
      renderChannels();
    });
    dom.channelStatusFilter.addEventListener("change", function (event) {
      state.channelStatusFilter = event.target.value;
      renderChannels();
    });
    dom.channelSearch.addEventListener("input", function (event) {
      state.channelSearch = event.target.value;
      renderChannels();
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

    renderEmptyShell();
    loadDashboard();
  });

  async function requestJSON(url) {
    var response;
    try {
      response = await fetch(url, {
        headers: { Accept: "application/json" },
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
      var channelsURL = ENDPOINTS.channels;
      var connectionsURL = withSession(ENDPOINTS.connections, sessionId);
      var messagesURL = withSession(ENDPOINTS.messages, sessionId);
      var results = await Promise.all([
        requestJSON(overviewURL),
        requestJSON(channelsURL).then(function (data) {
          return { data: data };
        }).catch(function (error) {
          return { error: error };
        }),
        requestJSON(connectionsURL),
        requestJSON(messagesURL),
      ]);

      state.overview = results[0] || {};
      if (results[1] && results[1].error) {
        state.channelsSnapshot = { items: [], total: 0 };
        state.channelsError = readableError(results[1].error);
      } else {
        state.channelsSnapshot = results[1].data || {};
        state.channelsError = "";
      }
      state.connectionsSnapshot = results[2] || {};
      state.messagesSnapshot = results[3] || {};
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
    renderChannels();
    renderSessionSelect();
    renderConnectionViewControls();
    renderConnections([], 0);
    renderMessages([]);
  }

  function renderAll() {
    var connections = getConnections();
    var messages = getMessages();
    renderOverview(state.overview || {});
    renderChannels();
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

  function renderChannels() {
    var channels = getChannels();
    var visibleChannels = getVisibleChannels(channels);
    renderChannelError();
    renderChannelSummary(channels);
    renderChannelFilters();
    renderChannelList(visibleChannels, channels.length);
    renderChannelDetail(channels, visibleChannels);
  }

  function renderChannelError() {
    if (!dom.channelsError) {
      return;
    }
    if (!state.channelsError) {
      dom.channelsError.hidden = true;
      setText(dom.channelsError, "");
      return;
    }
    dom.channelsError.hidden = false;
    setText(dom.channelsError, "Channels 加载失败：" + state.channelsError);
  }

  function renderChannelSummary(channels) {
    var counts = channelCounts(channels);
    setText(dom.channelsCount, channels.length + " 条");
    dom.channelsSummary.replaceChildren();
    [
      { label: "channel 总数", value: channels.length },
      { label: "connected", value: counts.connected, tone: counts.connected ? "ok" : "" },
      { label: "client", value: counts.client },
      { label: "server", value: counts.server },
      { label: "active connections", value: counts.activeConnections },
      { label: "imports / exports", value: counts.imports + " / " + counts.exports },
    ].forEach(function (metric) {
      var item = create("div", "channel-summary-item");
      var label = create("div", "metric-label");
      var value = create("div", "metric-value");
      if (metric.tone === "ok") {
        value.classList.add("ok");
      }
      setText(label, metric.label);
      setText(value, metric.value);
      item.append(label, value);
      dom.channelsSummary.appendChild(item);
    });
  }

  function renderChannelFilters() {
    dom.channelModeFilter.value = state.channelModeFilter;
    dom.channelStatusFilter.value = state.channelStatusFilter;
    dom.channelSearch.value = state.channelSearch;
    dom.channelModeFilter.disabled = state.loading;
    dom.channelStatusFilter.disabled = state.loading;
    dom.channelSearch.disabled = state.loading;
  }

  function renderChannelList(channels, totalCount) {
    dom.channelsBody.replaceChildren();
    setText(dom.channelsCount, channels.length + " / " + totalCount + " 条");
    dom.channelsEmpty.hidden = channels.length !== 0;
    channels.forEach(function (channel) {
      var row = document.createElement("tr");
      row.className = "channel-row";
      if (String(channel.id || "") === state.selectedChannelId) {
        row.classList.add("is-selected");
      }
      row.tabIndex = 0;
      row.addEventListener("click", function () {
        state.selectedChannelId = String(channel.id || "");
        renderChannels();
      });
      row.addEventListener("keydown", function (event) {
        if (event.key !== "Enter" && event.key !== " ") {
          return;
        }
        event.preventDefault();
        state.selectedChannelId = String(channel.id || "");
        renderChannels();
      });
      appendStackCell(row, [
        ["id", channel.id],
        ["name", channel.name],
        ["type", channel.type],
        ["protocol", channel.protocol],
      ]);
      appendCell(row, channel.mode, "mono");
      appendChannelStatusCell(row, channel.status);
      appendStackCell(row, [
        ["active", objectValue(channel.connection).activeCount],
        ["latest", objectValue(channel.connection).latestSessionId],
      ]);
      appendStackCell(row, [
        ["allowed", objectValue(channel.agents).allowedCount],
        ["imports", objectValue(channel.agents).importCount],
        ["exports", objectValue(channel.agents).exportCount],
      ]);
      dom.channelsBody.appendChild(row);
    });
  }

  function renderChannelDetail(allChannels, visibleChannels) {
    var selected = findChannelById(allChannels, state.selectedChannelId);
    if (!selected && visibleChannels.length > 0) {
      selected = visibleChannels[0];
      state.selectedChannelId = String(selected.id || "");
    }
    dom.channelDetail.replaceChildren();
    if (!selected) {
      var empty = create("div", "empty-state");
      setText(empty, "选择一个 channel 查看详情");
      dom.channelDetail.appendChild(empty);
      return;
    }
    var connection = objectValue(selected.connection);
    var agents = objectValue(selected.agents);
    var config = objectValue(selected.config);
    var head = create("div", "channel-detail-head");
    var title = create("div", "");
    var h3 = document.createElement("h3");
    var subtitle = create("span", "muted");
    setText(h3, firstValue(selected.name, selected.id));
    setText(subtitle, "id: " + normalizeDisplayValue(selected.id));
    title.append(h3, subtitle);
    var chip = create("span", "chip");
    chip.classList.add(channelStatusClass(selected.status));
    setText(chip, selected.status);
    head.append(title, chip);
    dom.channelDetail.appendChild(head);

    dom.channelDetail.appendChild(detailBlock("连接", [
      ["mode", selected.mode],
      ["connected", connection.connected],
      ["activeCount", connection.activeCount],
      ["latestSessionId", connection.latestSessionId],
      ["connectedAt", formatOptionalTime(connection.connectedAt)],
      ["lastSeenAt", formatOptionalTime(connection.lastSeenAt)],
    ]));
    dom.channelDetail.appendChild(detailBlock("配置摘要", [
      ["endpointUrl", config.endpointUrl],
      ["endpointPath", config.endpointPath],
      ["authType", config.authType],
      ["heartbeat", formatSeconds(config.heartbeatIntervalSeconds)],
      ["reconnectHandshake", formatSeconds(config.reconnectHandshakeTimeoutSeconds)],
      ["reconnectMin", formatSeconds(config.reconnectMinSeconds)],
      ["reconnectMax", formatSeconds(config.reconnectMaxSeconds)],
    ]));
    dom.channelDetail.appendChild(detailBlock("Agents", [
      ["allowedAllAgents", agents.allowedAllAgents],
      ["allowedCount", agents.allowedCount],
      ["importCount", agents.importCount],
      ["exportCount", agents.exportCount],
    ]));
    dom.channelDetail.appendChild(agentListBlock("导入 agent", arrayValue(agents.imports), formatChannelImport));
    dom.channelDetail.appendChild(agentListBlock("导出 agent", arrayValue(agents.exports), formatChannelExport));
    dom.channelDetail.appendChild(agentListBlock("允许 agent", arrayValue(agents.allowedAgentKeys), function (key) {
      return String(key || "");
    }));
  }

  function appendChannelStatusCell(row, status) {
    var cell = document.createElement("td");
    var chip = create("span", "chip");
    chip.classList.add(channelStatusClass(status));
    setText(chip, status || "unknown");
    cell.appendChild(chip);
    row.appendChild(cell);
  }

  function channelStatusClass(status) {
    var normalized = String(status || "").toLowerCase();
    if (normalized === "connected") {
      return "chip-active";
    }
    if (normalized === "unavailable") {
      return "chip-unavailable";
    }
    return "chip-closed";
  }

  function detailBlock(title, rows) {
    var block = create("section", "channel-detail-block");
    var heading = document.createElement("h4");
    var list = create("div", "channel-detail-lines");
    setText(heading, title);
    rows.forEach(function (row) {
      var line = create("div", "channel-detail-line");
      var label = create("span", "muted");
      var value = create("strong", "mono");
      setText(label, row[0]);
      setText(value, row[1]);
      line.append(label, value);
      list.appendChild(line);
    });
    block.append(heading, list);
    return block;
  }

  function agentListBlock(title, values, formatter) {
    var block = create("section", "channel-detail-block");
    var heading = document.createElement("h4");
    var list = create("div", "channel-agent-list");
    setText(heading, title + " (" + values.length + ")");
    if (!values.length) {
      var empty = create("div", "muted");
      setText(empty, "无");
      list.appendChild(empty);
    } else {
      values.slice(0, 80).forEach(function (item) {
        var pill = create("span", "channel-agent-pill mono");
        setText(pill, formatter(item));
        list.appendChild(pill);
      });
      if (values.length > 80) {
        var more = create("span", "channel-agent-pill mono");
        setText(more, "+" + (values.length - 80));
        list.appendChild(more);
      }
    }
    block.append(heading, list);
    return block;
  }

  function formatChannelImport(item) {
    item = objectValue(item);
    return [
      firstValue(item.name, item.agentKey),
      item.remoteAgentKey ? "remote=" + item.remoteAgentKey : "",
    ].filter(Boolean).join(" · ");
  }

  function formatChannelExport(item) {
    item = objectValue(item);
    return [
      firstValue(item.name, item.agentKey),
      item.externalAgentKey ? "external=" + item.externalAgentKey : "",
      "allow=" + allowedOperations(objectValue(item.allow)).join(","),
    ].filter(Boolean).join(" · ");
  }

  function allowedOperations(allow) {
    return ["query", "submit", "steer", "interrupt", "fileTransfer"].filter(function (key) {
      return parseBooleanFlag(allow[key]) === true;
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

  function getChannels() {
    var snapshot = state.channelsSnapshot || {};
    return arrayValue(firstValue(snapshot.items, snapshot.channels, snapshot.list));
  }

  function getVisibleChannels(channels) {
    var mode = String(state.channelModeFilter || "").toLowerCase();
    var status = String(state.channelStatusFilter || "").toLowerCase();
    var query = String(state.channelSearch || "").trim().toLowerCase();
    return channels.filter(function (channel) {
      var channelMode = String(channel.mode || "").toLowerCase();
      var channelStatus = String(channel.status || "").toLowerCase();
      if (mode && channelMode !== mode) {
        return false;
      }
      if (status && channelStatus !== status) {
        return false;
      }
      if (!query) {
        return true;
      }
      var haystack = [
        channel.id,
        channel.name,
        channel.type,
        channel.mode,
        channel.transport,
        channel.protocol,
        objectValue(channel.config).endpointUrl,
        objectValue(channel.config).endpointPath,
      ].map(function (value) {
        return String(value || "").toLowerCase();
      }).join(" ");
      return haystack.indexOf(query) !== -1;
    });
  }

  function channelCounts(channels) {
    return channels.reduce(function (counts, channel) {
      var mode = String(channel.mode || "").toLowerCase();
      var status = String(channel.status || "").toLowerCase();
      var connection = objectValue(channel.connection);
      var agents = objectValue(channel.agents);
      if (mode === "client") {
        counts.client += 1;
      } else if (mode === "server") {
        counts.server += 1;
      }
      if (status === "connected") {
        counts.connected += 1;
      }
      counts.activeConnections += numericValue(connection.activeCount);
      counts.imports += numericValue(agents.importCount);
      counts.exports += numericValue(agents.exportCount);
      return counts;
    }, {
      client: 0,
      connected: 0,
      server: 0,
      activeConnections: 0,
      imports: 0,
      exports: 0,
    });
  }

  function findChannelById(channels, channelId) {
    var target = String(channelId || "");
    if (!target) {
      return null;
    }
    for (var i = 0; i < channels.length; i += 1) {
      if (String(channels[i].id || "") === target) {
        return channels[i];
      }
    }
    return null;
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
    if (dom.channelModeFilter) {
      dom.channelModeFilter.disabled = loading;
    }
    if (dom.channelStatusFilter) {
      dom.channelStatusFilter.disabled = loading;
    }
    if (dom.channelSearch) {
      dom.channelSearch.disabled = loading;
    }
    renderConnectionViewControls();
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

  function numericValue(value) {
    var number = Number(value);
    return Number.isFinite(number) && number > 0 ? number : 0;
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

  function formatSeconds(value) {
    var seconds = Number(value);
    if (!Number.isFinite(seconds) || seconds <= 0) {
      return "未提供";
    }
    return seconds + "s";
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
