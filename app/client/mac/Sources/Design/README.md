# `Sources/Design` — design system

Two layers:

## Tokens (`Theme.swift`)

`Niq` holds the WebUI's proportions in a native form: the single blue accent
(rgb 60,120,180), adaptive surfaces derived from semantic `NSColor`s (so light and
dark both work without a colour scheme lookup), the outcome palette
(completed/failed/pending/reason), monospaced/sans font helpers, and the shared
`radius` / `bubbleMax` metrics.

Shared controls: `Chip` (@targets, type tags), `Dot` (status), `Ident`
(monospaced ids), `Pill` / `OutlineButton` (square chrome buttons), `SectionBox`,
`FormRow`, `SquareField`, `SquareTextEditor`, `SquareTabs`, `PageHeader`
(`Components.swift`), plus the `niqCard` / `niqSectionLabel` / `niqHairline`
modifiers.

## Square by design

The WebUI's blocks are rectangles: 1px borders, no rounded corners, small
monospaced caps for identifiers, one blue accent. `niqCard` draws a `Rectangle`
with a 1px stroke; every chip, button and panel follows. Rounding is reserved
for genuinely native affordances (`.switch`, `Menu`, `.popover`, sheets).

## Type scale

The WebUI's 12–14px body reads as cramped on a desktop window, so the native
client sits one step larger: 15px prose (`Niq.body`), 17px view titles, 26px
speaker names, 11–13px metadata. `Niq.headerHeight` / `sidebarWidth` /
`pagePadding` / `columnMax` keep the proportions consistent across screens — the
conversation is a centered reading column, tables span the full width.

## Liquid Glass (`Glass.swift`)

| Helper | Responsibility |
|---|---|
| `niqGlass(_:tint:interactive:)` | Liquid Glass on macOS 26+ (`glassEffect`), `regularMaterial` before. |
| `niqGlassButton()` | `.buttonStyle(.glass)` on 26+, `.bordered` before. |
| `niqContentSurface(_:)` | A standard content-layer surface. |

HIG split: glass is for the **control/navigation layer** (chips, pills, floating
controls); **content** (transcript cards, tables, tabular data) uses flat
surfaces and hairlines — which is also what makes it read like the WebUI.
