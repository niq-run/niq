# provider — provider configuration semantics for the standalone form

`provider` turns a parsed `provider.json` into decisions: given a `*Config` it
resolves the entry and model a project uses (`Find`, `Default`), reports
whether anything usable exists (`Configured`), builds the wire client (`Build`),
exposes the switchable sources (`NewProviderSources`), and refuses startup
when an LLM is required but none is configured (`EnsureLLMConfigured`).

It is a pure value layer. `store/provider.go` owns the file (`LoadProvider`,
`SaveProvider`, `EnsureProviderExample`) and passes in a parsed `*Config` or a
`Loader`. Nothing here opens a file or owns an instance: a `Build`ed client
belongs to its caller, and `core/impl/provider/*` supplies the HTTP dialects.

## Configuration shape

`Config` is the file: an optional `Active` name plus `Providers`. An `Entry`
carries `Name`, `Type`, credentials (`APIKey`, `Headers`), `BaseURL`, a default
`Model`, a configured `Models` list, and provider-wide defaults (`ContextWindow`,
`MaxOutput`, `InputTypes`) inherited by models that declare none. `Models` reads
`["a","b"]` and `[{"name":"a","context_window":8}]`, writing the string form
back only when no model carries metadata, so a capability is never flattened.

## Resolution

| function | answers |
|---|---|
| `Find(cfg, name)` | the entry with that name, or `false` |
| `Default(cfg)` | `Active` if it resolves, else the first entry; `false` for nil/empty |
| `Configured(cfg)` | whether any entry has a non-empty `api_key` |
| `Entry.ResolveDefaultModel()` | `Model`, else `Models[0].Name`, else `""` |
| `Entry.ListModels()` | the `Models` names, else `[Model]`, else `nil` |

## Build

`Build` copies the entry, pins `Model` to `ResolveDefaultModel`, expands
`${VAR}` / `$VAR` in `api_key` and every header value (a missing variable
becomes the empty string), then dispatches on `Type`:

| `Type` | adapter | base URL / model defaults |
|---|---|---|
| `claude`, `anthropic` | `anthropic.New` | `api.deepseek.com/anthropic`, `claude-sonnet-4-20250514` |
| `openai-responses`, `responses`, `deepseek-responses` | `openairesponses.New` | `api.deepseek.com`, `deepseek-flash` |
| `deepseek` | `openai.New` | `api.deepseek.com/v1`, `deepseek-flash` |
| anything else | `openai.New` | `openai-compatible` → `api.deepseek.com/v1`, else `api.openai.com/v1`; `gpt-4o` |

An empty key falls back to `ANTHROPIC_API_KEY` on the claude branch, else
`OPENAI_API_KEY`; custom headers are applied after the built-in auth headers.

## Sources and the startup gate

`NewProviderSources(load, name, model)` returns a `reason.ProviderSources` whose
`Default`, `Build` and `List` methods re-read through the `Loader` on each call,
so a running worker sees edits without a restart; `InitialProviderInfo` reports
the spawn-time pair the same way. Selection is by name only — the entry owns the
credentials. `List` serves a non-empty `Models` list verbatim and never queries
the API; only an empty list triggers the endpoint, bounded at three seconds.
`EnsureLLMConfigured(cfg, workers, path)` demands a provider only when a `reason`
or `coding` worker is declared, failing with an error naming `path`; seeding the
example file is the caller's job.

## Files

| File | Responsibility |
|---|---|
| `providercfg.go` | The value model and its JSON forms (`Entry`, `ModelSpec`, `Models`), `Config`/`Loader`, the example skeleton, and `Find`/`Default`/`Configured`/`Build` with `${VAR}` expansion. |
| `providers.go` | The `reason.ProviderSources` implementation (`NewProviderSources`, `Default`, `Build`, `List`), `InitialProviderInfo`, and the `EnsureLLMConfigured` gate. |
| `providercfg_test.go` | `Default`/`Find`/`Configured`/example-template behaviour, and the `Models` round-trip that keeps per-model metadata. |
| `providers_test.go` | The gate's worker-set cases, `modelsFor`'s configured-list-skips-API contract against a stub `/models` server, and `InitialProviderInfo`'s selection table. |

## Invariants

- No file IO, no lifecycle: `store/provider.go` serializes `Config`, and this
  package only ever receives a `*Config` or a `Loader`.
- Dependency inverted on purpose: `store` imports `Config` to serialize it,
  `provider` imports only `core`, and its `Loader` is implemented by the assembly.
- `Configured` reads `api_key` literally, so a `${VAR}` reference counts even
  when the variable is unset; expansion and defaults happen only in `Build`.
- A non-empty `Models` list is authoritative and suppresses discovery; the API is
  consulted only for an empty list, and never without the timeout.
