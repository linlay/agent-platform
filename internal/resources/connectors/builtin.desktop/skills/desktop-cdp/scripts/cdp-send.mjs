#!/usr/bin/env node

import { mkdir, writeFile } from "node:fs/promises";
import { dirname } from "node:path";

const usage = `Usage:
  node cdp-send.mjs <ws-url> <method> [params-json] [options]

Options:
  --wait-event <event>  Wait for one CDP event after sending the command
  --save <file>        Decode result.data as base64 and save it to a file
  --timeout <ms>       Timeout in milliseconds (default: 10000)

Examples:
  node cdp-send.mjs "$WS" Page.navigate '{"url":"https://example.com"}'
  node cdp-send.mjs "$WS" Runtime.evaluate '{"expression":"document.title","returnByValue":true}'
  node cdp-send.mjs "$WS" Page.captureScreenshot '{"format":"png"}' --save /tmp/page.png
`;

function fail(message, code = 2) {
  console.error(message);
  console.error("");
  console.error(usage.trim());
  process.exit(code);
}

function parseArgs(argv) {
  if (argv.length < 2) {
    fail("Missing required arguments.");
  }

  const wsUrl = argv[0];
  const method = argv[1];
  let cursor = 2;
  let params = {};

  if (argv[cursor] && !argv[cursor].startsWith("--")) {
    try {
      params = JSON.parse(argv[cursor]);
    } catch (error) {
      fail(`Invalid params JSON: ${error.message}`);
    }
    cursor += 1;
  }

  const options = {
    timeoutMs: 10000,
    waitEvent: null,
    saveFile: null,
  };

  while (cursor < argv.length) {
    const option = argv[cursor];
    const value = argv[cursor + 1];

    if (option === "--wait-event") {
      if (!value) fail("Missing value for --wait-event.");
      options.waitEvent = value;
      cursor += 2;
      continue;
    }

    if (option === "--save") {
      if (!value) fail("Missing value for --save.");
      options.saveFile = value;
      cursor += 2;
      continue;
    }

    if (option === "--timeout") {
      if (!value) fail("Missing value for --timeout.");
      const timeoutMs = Number(value);
      if (!Number.isFinite(timeoutMs) || timeoutMs <= 0) {
        fail("--timeout must be a positive number of milliseconds.");
      }
      options.timeoutMs = timeoutMs;
      cursor += 2;
      continue;
    }

    fail(`Unknown option: ${option}`);
  }

  if (!method.includes(".")) {
    fail("CDP method should look like Domain.method, for example Page.navigate.");
  }

  return { wsUrl, method, params, options };
}

async function saveBase64Data(response, saveFile) {
  const data = response?.result?.data;
  if (typeof data !== "string") {
    throw new Error("--save was set, but response.result.data is not a base64 string.");
  }

  const outputDir = dirname(saveFile);
  if (outputDir && outputDir !== ".") {
    await mkdir(outputDir, { recursive: true });
  }

  await writeFile(saveFile, Buffer.from(data, "base64"));

  return {
    ...response,
    result: {
      ...response.result,
      data: `<${data.length} base64 chars omitted>`,
      saved: saveFile,
    },
  };
}

async function main() {
  if (typeof WebSocket === "undefined") {
    console.error("Native WebSocket is not available in this Node.js runtime.");
    process.exit(1);
  }

  const { wsUrl, method, params, options } = parseArgs(process.argv.slice(2));
  const id = 1;
  const enableId = options.waitEvent ? id : null;
  const commandId = options.waitEvent ? id + 1 : id;
  const command = { id: commandId, method, params };
  const eventDomain = options.waitEvent?.split(".")[0] || null;
  const enableCommand = eventDomain
    ? { id: enableId, method: `${eventDomain}.enable`, params: {} }
    : null;

  let settled = false;
  let enableResponse = null;
  let response = null;
  let event = null;
  let timeoutHandle;
  let ws;

  const finish = async (error = null) => {
    if (settled) return;
    settled = true;
    clearTimeout(timeoutHandle);

    try {
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.close();
      }
    } catch {
      // Ignore close errors while exiting.
    }

    if (error) {
      console.error(error.message || String(error));
      process.exit(1);
    }

    try {
      let printableResponse = response;
      if (options.saveFile) {
        printableResponse = await saveBase64Data(response, options.saveFile);
      }

      const payload = options.waitEvent
        ? { enableResponse, response: printableResponse, event }
        : printableResponse;

      console.log(JSON.stringify(payload, null, 2));
      process.exit(response?.error ? 1 : 0);
    } catch (saveError) {
      console.error(saveError.message || String(saveError));
      process.exit(1);
    }
  };

  const maybeFinish = () => {
    if (!response) return;
    if (response.error) {
      finish();
      return;
    }
    if (options.waitEvent && !event) return;
    finish();
  };

  timeoutHandle = setTimeout(() => {
    const waitingFor = options.waitEvent && response
      ? `event ${options.waitEvent}`
      : `response to ${method}`;
    finish(new Error(`Timed out after ${options.timeoutMs}ms waiting for ${waitingFor}.`));
  }, options.timeoutMs);

  try {
    ws = new WebSocket(wsUrl);
  } catch (error) {
    await finish(error);
    return;
  }

  ws.addEventListener("open", () => {
    ws.send(JSON.stringify(enableCommand || command));
  });

  ws.addEventListener("message", (messageEvent) => {
    let message;
    try {
      message = JSON.parse(messageEvent.data);
    } catch {
      return;
    }

    if (enableCommand && message.id === enableId) {
      enableResponse = message;
      if (message.error) {
        response = message;
        finish();
        return;
      }
      ws.send(JSON.stringify(command));
      return;
    }

    if (message.id === commandId) {
      response = message;
      maybeFinish();
      return;
    }

    if (options.waitEvent && message.method === options.waitEvent) {
      event = message;
      maybeFinish();
    }
  });

  ws.addEventListener("error", () => {
    finish(new Error(`WebSocket connection failed: ${wsUrl}`));
  });

  ws.addEventListener("close", () => {
    if (!settled && !response) {
      finish(new Error(`WebSocket closed before receiving response: ${wsUrl}`));
    }
  });
}

main().catch((error) => {
  console.error(error.message || String(error));
  process.exit(1);
});
