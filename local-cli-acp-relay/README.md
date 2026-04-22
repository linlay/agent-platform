# Local CLI ACP Relay

V1 relay for the web code assistant path:

`agent-webclient -> agent-platform WebSocket -> local-cli-acp-relay /ws -> claude-code-acp stdio -> local Claude Code`

## Run

```bash
cp local-cli-acp-relay/.env.example local-cli-acp-relay/.env
node local-cli-acp-relay/relay.mjs
```

The local machine must already have Claude Code installed and logged in. The default `.env.example`
starts the ACP adapter through `npx -y @zed-industries/claude-code-acp`; replace it with a local
`claude-code-acp` path if you have the adapter installed globally.

Point the `codeAssistant` agent `proxyConfig.baseUrl` at:

```yaml
proxyConfig:
  baseUrl: http://127.0.0.1:3220
  token: ""
  timeoutMs: 600000
```
