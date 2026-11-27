# `Features/Models` — model providers (provider.json)

Two tabs, mirroring the WebUI's split:

- **可视化** — one card per provider (name, base URL, type, default model and a
  model grid), with the active provider marked 默认. Read-only by design.
- **JSON** — the whole `provider.json` document in a monospaced editor; 保存
  PUTs it (`PUT /api/providers`) and refreshes the visual tab.

The JSON editor is the write path; a field-by-field form (the WebUI's visual
editor) is the remaining gap. Providers are a control-plane resource.
