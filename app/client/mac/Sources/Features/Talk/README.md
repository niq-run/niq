# `Features/Talk` — the conversation view

The core surface. It is a WebUI-faithful projection of the bus plus a thin
renderer.

| File | Responsibility |
|---|---|
| `Transcript.swift` | **no SwiftUI.** Projects raw events into `TalkItem`s (human / assistant / thinking / tool / notice), splits a `<system-reminder>` envelope out of an input, pairs each tool call with its terminal `request.*` reply, resolves a reply's quoted input by trace, and folds runs of process rows into `TalkBlock.run` blocks. |
| `TalkRows.swift` | the row renderers: big blue speaker header, human card (sender · @targets · mode · time · 调用链), reply card (quote + Markdown + 复制 + usage line), thinking disclosure, tool card (代码 / 参数 / 请求结果), notice rows, the `N 条过程事件` fold chip, and the context-usage popover. |
| `Markdown.swift` | block-aware Markdown: prose via `AttributedString`, fenced code blocks with wrap/copy, `|`-tables in monospace. |
| `TalkView.swift` | the page: header (正在监听 chips, 审批 pill, follow toggle), the scrolling timeline with 载入更早的消息 paging, the live streaming tail, and the composer. |
| `Composer.swift` | recipient chips (send targets), attachments, mode, 停止, 发送, expandable editor. |

## Two different selections (important)

- **Sidebar checkboxes (`AppModel.viewFilter`)** = *which events this
  conversation shows*. Empty = every **talk partner** (reason / coding / niq),
  matching the WebUI's default scope.
- **Composer chips (`AppModel.sendTargets`)** = *who a message is sent to*.

`send()` posts `worker.input` to `sendTargets` (with attachment blocks), and
`abort()` aborts those same workers.

## Alignment rule

The human's own messages (`worker_id == "webui"`) sit right; everything else
sits left under the speaker's name. The human's own 中止 also reads as an
outgoing row. This is a simplification of the WebUI's rule (which additionally
right-aligns any system worker talking to a reason worker).

## Attachments

Images ride inline (base64, ≤5 MB, ≤4 attachments); other files are uploaded to
`POST /p/{id}/api/upload` (multipart field `file`) and referenced by their
returned path. Both become blocks of the structured `worker.input` content
array. Drag-and-drop onto the composer stages files the same way (no folder
expansion yet).

## Streaming & paging

`reason.text_delta` / `reason.thinking_delta` are transient: `AppModel.append`
accumulates them into `streamingText` / `streamingThinking` (only while
流式模式 is on) and clears them when `reason.response` lands. History backfills
60 events at the watermark; 载入更早的消息 pages 100 more at a time from the
oldest held id.

## Follow-ups

Scroll-to-quoted-message, a native @-mention popup inside the editor, inline
approval actions in the timeline, and folder drops.
