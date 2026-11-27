# `app/client/tui`

The terminal client: a picker (`niq i`) and a full-screen conversation view
(`niq c <project>`).

Everything here is hand-written escape codes over `golang.org/x/term`. There is
no TUI framework — no bubbletea, no tcell — because the two screens are small and
the rendering model (a full frame, diffed row by row) is what pi does and what
keeps a keystroke cheap. The only data import is `app/standalone/contract`: the
same HTTP client the WebUI's data comes from, so a terminal is just another client
of a project's bus.

## Files, bottom layer first

The reusable layers are subpackages, each with its own tests:

| Package | Responsibility |
|---|---|
| `clipboard/` | native clipboard fallback next to OSC 52 |
| `term/` | the styling foundation: ANSI styles, the dark/light palette and band surfaces, cell measurement, ANSI-aware clip/slice |
| `screen/` | the alternate-screen framebuffer: line diffing, clipping |
| `keys/` | raw input decoding (C0, CSI, Kitty, modifyOtherKeys, paste, mouse, reports) and the key reader |
| `layout/` | the layout layer: the transcript's scroll viewport |
| `render/` | the renderers: the markdown viewer (with GFM tables) and the code highlighter |
| `ui/` | the reusable small controls: the modal card the pickers drape over |
| `picker/` | the `niq i` picker app: its own menu, key reader and table output |
| `live/` | the live subscription pump and its reconnect backoff |
| `stdin/` | the stdin pump and its userspace read deadlines |
| `text/` | event-payload reading, control/ANSI sanitising |

The conversation view itself, grouped by responsibility:

| File | Responsibility |
|---|---|
| `styles.go` | thin forwarders onto `term`, so the view keeps reading `dim(…)`, `brand(…)`, `displayWidth(…)` |
| `keys_shim.go` | thin aliases/readers onto `keys`, so keymap keeps naming presses `press` / `pressXxx` |
| `blocks.go` | text → lines: wrapping, inline/answer shapes, section rules |
| `render.go` | event → block (what each event means, tool cards, filters) and the transcript (blocks → lines, gaps, surfaces) |
| `usage.go` | the context-usage numbers a reason.response carries, formatted, and their detail panel |
| `selection.go` | the mouse text selection over the transcript, mapping cell ranges back to clipboard text (the scroll window is `layout.Viewport`) |
| `keymap.go` | what a decoded key means in each mode (insert / normal / help) |
| `input.go` | the composer: paste chips, attachments, delivery mode, its rows, and the line-editor buffer underneath |
| `dock.go` | the pinned bottom area: status/mode rule, composer rows, context/model line |
| `menu.go` | the modal lists: target, view, input-mode and model pickers, plus the keybinding page |
| `chat.go` | the session: state, event loop, sending, history paging, worker roster/routing |

The split is a convention, not a compile boundary. The genuinely reusable pieces
are lifted into `term/` (styling + cells), `screen/` (framebuffer), `keys/`
(input decoding), `render/` (markdown + highlight), `layout/` (scroll viewport),
`ui/` (the modal card) and `picker/` (the whole `niq i` app). The one stateful
widget left in this package — `selection` — is app plumbing: the session pokes its
fields directly. It moves only when a second consumer wants it.

## The frame

`screen` owns the alternate buffer and a `prev []string` of painted rows. Each
render composes exactly `height` rows, clips them to the width (ANSI-aware, so a
long line can never soft-wrap and push the dock off screen), and repaints only the
rows that changed, inside a synchronized-update block (`?2026`). The dock is the
bottom of that frame: a blank spacer, the status/rule border, the composer rows, a
divider rule under the composer, and the context/model line at the very bottom
(below that divider). The composer has no surface of its own; the rules frame it.

## Context usage and model

A `reason.response` carries the round's token accounting in `payload.meta.usage`
(the same numbers the WebUI draws). Each response trails a dim label with its
occupancy, e.g. `ctx 12% · 36K/128K · in 3.2K · out 800 · cache 68%`.

The line under the composer mirrors that for the view: on the left, the worker and
its latest occupancy — the worker the view is scoped to, or the newest response's
worker when the view is `all`; on the right, that worker's current model
(`provider/model ▾`). The model is a click target: it opens a picker of the
worker's models (fetched from `GET /p/<id>/api/workers/<id>/providers`) and a
selection switches the worker through `POST /p/<id>/api/workers/<id>/provider`.

## Keys

The conversation view is **modal**, like vim. It opens in *insert* mode, where
every key goes to the message. `esc` switches to *normal* mode, where keys scroll
the transcript; `i` (or a click in the composer) goes back. Clicks pick the mode
by where they land: the transcript is normal, the composer is insert. The rule
above the input has target, view and input mode on the left, and the vim state
plus `help` on the right; every one of those segments is a click target, and the
shortcut reminders live in the help card. `?` in normal mode or `⌃?` in either
mode — or a click on `help` — opens a keybinding card over the transcript. Every
popup closes on `esc` or a click outside it; a click on `target:`, `view:` or
`mode:` opens the matching picker, and a click on the vim state toggles insert /
normal (the two states need no popup).

| insert mode | Action |
|---|---|
| `enter` | send |
| `shift+enter`, `⌃J`, `⌥enter` | newline |
| `←` `→` `↑` `↓` | move the cursor |
| `⌃A` / `⌃E` | line start / end |
| `⌃B` / `⌃F`, `⌥b` / `⌥f` | character / word left-right |
| `⌃P` / `⌃N` | previous / next line |
| `⌫` / `⌦` | delete before / after the cursor |
| `⌃K` / `⌃W` | kill to end of line / previous word |
| `⌘⌫` (macOS) / `⌃⌫` (Win/Linux) | kill to line start |
| `⌃Y` | yank the last kill |
| `⌃?` | keybinding help |
| `esc` | normal mode |

| normal mode | Action |
|---|---|
| `j` / `k`, `⌃E` / `⌃Y` | line down / up |
| `⌃D` / `⌃U`, `⌃F` / `⌃B`, `PgUp` / `PgDn` | page |
| `g` / `G` | oldest / newest |
| `i` | insert mode |
| `?` | keybinding help |

`⌃T` / `⌃V` (target / view pickers), `⌃X` (input-mode picker), `⌃?` (help),
`⌃C` / `⌃Q` (leave), the wheel and drag-selection work in both modes. Every popup
closes on `esc`, a click outside it, or its own key again; clicking an option in a
picker selects it; clicking the vim state toggles insert / normal.

`Tab` / `Shift+Tab` move a keyboard focus across the rule's buttons — target, view,
input mode, vim state and help — and `Enter` activates the focused one (opening its
picker, the help page, or toggling the vim mode), so the whole bar is reachable
without the mouse. Any other key or `esc` drops the focus and typing resumes;
`Tab` / `Shift+Tab` also move the highlight inside an open picker.

`Esc` never quits: switching or cancelling a Chinese IME sends one, and treating
it as quit closed the picker under the user's hands. Only `q`/`⌃C` quit the
picker, `⌃C`/`⌃Q` quit the conversation.

## Decisions worth knowing

- **Input decoding covers three spellings.** C0 bytes, the classic `CSI` forms,
  the Kitty keyboard protocol (`CSI code;mod u`, enabled with `>1u`) and xterm's
  `modifyOtherKeys` (`CSI 27;mod;code~`). That is how `shift+enter` is
  distinguishable from `enter` at all. Bracketed paste (`?2004`) delivers a paste
  as one unit, so a large block can become a chip instead of thousands of keys.
- **Nothing from the terminal is ever typed by accident.** OSC strings (colour
  replies), bracketed paste, and CSI reports are consumed as events; a stray
  control byte is replaced with `·` before it is drawn, and pasted escape
  sequences are stripped whole. A message with `ESC[2J` in it cannot punch a hole
  in the frame.
- **Large pastes are chips.** A block of ≥4 lines or ≥400 cells is kept behind one
  private-use rune shown as `[pasted text #1 · 24 lines]`: it moves and deletes as
  one character and expands only when the message is sent.
- **Attachments are references or uploads, by locality.** A `file` block is only
  a note naming a path to the reason worker, so the path must be valid where the
  worker runs. A loopback gateway (or the gateway's own hostname) keeps the local
  path; anything else uploads through `POST /api/upload` first. `NIQ_ATTACH`
  overrides the guess (SSH tunnels, shared mounts). Images ride inline as base64
  either way — that is a real multimodal block.
- **Themes are asked for, not guessed.** `COLORFGBG` is only set by some
  terminals, so the view queries the background colour (OSC 11) and the colour
  scheme (`CSI ? 997 n`), subscribes to changes (mode 2031), and falls back to
  `COLORFGBG` then dark. Surfaces carry a matching ink so text stays legible
  whatever the terminal's default foreground is.
- **The transcript is layered like pi's**: a tinted card for the human, one green
  card per tool call (call line, spacer, reply in a lighter tone), a code surface
  for fenced blocks, an amber surface for warnings and a red one for failures.
  A worker's answer is unbanded: a dotted rule with its name at the left edge, a
  blank row, then prose inset by one cell so it lines up with the cards' text.
  Thinking is grey with no surface, up to ten lines: it streams from
  `reason.thinking_delta` and past ten lines keeps the first three and the newest
  seven with an omission row between them.
