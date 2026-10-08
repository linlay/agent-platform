# Configuration maintenance

Use this reference for explicit requests to maintain Provider/Model YAML or external connector component files. Read their exact Runtime Context source directories and current contents. Work only through available file tools with normal path permissions; do not edit generated runtime packages or use source edits to bypass a rejected management operation. Catalog discovery is sanitized and read-only for Provider/Model/MCP; it is not a source editing API.

## Provider YAML

Use providers_dir and models_dir. Existing definitions are .yml or .yaml files. Provider entries use key, baseUrl, apiKey, defaultModel and protocols. Protocol entries may contain endpointPath, headers and compat. Some definitions use provider embedding configuration; preserve it when unrelated to the requested change.

- Keep the provider key stable unless the user requests a rename, and verify references before changing it. Do not infer a provider's identity from the display name or its endpoint.
- Verify the intended model/protocol binding before changing defaultModel, baseUrl or a protocol endpointPath. Retain unrelated protocols and fields.
- Credentials and private endpoints are sensitive: use only the necessary source, make a targeted edit, and never paste the full file or secret into the response. Use connector authorization for connector credentials.
- Provider memory configuration is retired. Do not restore it from older examples. Field order is a readability choice, not a loader header contract.

## Model YAML

Use models_dir. Core identity and binding fields are key, provider, protocol and modelId; name and icon are display fields. Type is chat, embedding, image-generation or vl. Select actual supported protocols and an existing provider rather than inventing enum values.

Current options include isFunction, isReasoner, isVision, maxInputTokens, l1KeepRecentRounds, timeout, headers, compat, reasoningEfforts, reasoningEffortMapping, serviceTiers, embedding and image. Check the current loader and target model type before adding an option; preserve existing unrelated options. l1KeepRecentRounds, when configured, is an integer 5–10. maxInputTokens represents the context window; do not copy the old maxOutputTokens field as a supported Model loader setting.

Pricing uses currency, unit, inputCacheHit, inputCacheMiss and output. Old promptPointsPer1k/completionPointsPer1k/perCallPoints/priceRatio/tiers examples are not the current loader pricing contract. Image generation and editing have their own image.generation/image.edit configuration; old image.endpointPath is rejected.

The current code contracts live in internal/models/model_registry.go. Read the relevant current definition before changing protocol compatibility, embedding or image options; this compact reference does not replace their type-specific schemas.

## External connector components

Use connectors_center_dir/<connector-id>, not a mounted @connectors execution package. builtin.* sources are immutable. The package manifest is connector.json; MCP components use mcp.json and CLI components use cli.json. Catalog apply edits connector.json, and its HTTP-MCP creation flow generates the initial component. Editing an existing component requires the separately authorized source/editor workflow.

- mcp.json contains mcpServers keyed by component. A streamableHttp component uses its full url; a stdio component uses command and optional args. Keep transport fields distinct and preserve existing component configuration.
- Read the actual component and current package schema before editing. Do not manufacture an independent mcp_servers_dir YAML, use the removed mcp-server catalog resource type, or revive the old connector-migrate command.
- Component templates, staticHeaders/staticEnv and external connector authorization have distinct boundaries. Never move deployment credentials into package files, read protected connector state with ordinary tools, or modify a shared mounted runtime package.
- A successful local save or package load is not evidence of remote authentication, MCP availability or discovered tools. Inspect the affected Agent's actual runtime connector status when verification requires it.

## Verify and report

Read back the edited source, verify the requested fields without echoing secrets, and check the affected loaded configuration or connector status. Existing registry/catalog watchers may reload resource sources; configs/*.yml are startup configuration and require a runtime restart. Do not restart a service merely to inspect a candidate, and do not report a source save as a completed remote capability check.

Owner profile maintenance follows the existing Owner/Memory workflow and explicit user instructions; this reference does not recreate the old inferred-profile or BOOTSTRAP deletion rules. Historical Viewport-server YAML is not a current configuration contract.
