# Agent and Skill Actions

| Action | Args |
| --- | --- |
| `desktop.agent.open` | `{agentKey}`; `id` is an alias |
| `desktop.skill.open` | `{skillKey}`; `id` is an alias |
| `desktop.agent.update` | `{agentKey, definition?, soulPrompt?, agentsPrompt?}` |
| `desktop.skill.update` | `{skillKey, path?, content, baseSha256?}` |

Open actions navigate to the selected Agent/Skill and return its key and route. Update actions call Platform admin APIs; inspect nested business status as well as the bridge response. Use the actual Agent definition schema for `definition`, not a returned summary. Prompt strings are limited to 100,000 characters by Desktop. Skill `path` defaults to `SKILL.md`; content is required, limited to 1 MiB of text, and must target an editable skill file. Preserve `baseSha256` from the prior read when available to detect concurrent changes. These actions use Desktop's normal confirmation flow.
