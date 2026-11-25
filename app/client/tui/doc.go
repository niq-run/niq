// Package tui is the terminal surface: the interactive picker (`niq i`) and the
// conversation view (`niq c <project>`).
//
// It is hand-written escape codes and a diff renderer, with no TUI dependency.
//
// A few layers are standalone subpackages, so they carry their own tests and can
// be read on their own:
//
//	clipboard/  the native clipboard fallback next to OSC 52
//	live/       the live subscription pump and its reconnect backoff
//	stdin/      the stdin pump and its userspace read deadlines
//	text/       event-payload reading and the control/ANSI sanitising of text
//
// The rest is the view itself, grouped by responsibility (bottom layer first):
//
//	cells.go     cell measurement: display/visible width, wide runes, truncation
//	             (no other file's symbols; safe to read first)
//	styles.go    ANSI styling (dim, brand, code tokens, …) and the version tag
//	theme.go     the dark/light palettes, band surfaces, and the terminal
//	             colour report that picks between them
//	workers.go   the roster: who is addressable, target choice and naming,
//	             reading an event's addressed worker out of the envelope
//	viewport.go  the transcript's scroll window (content length, height, follow)
//	selection.go the mouse text selection: cell ranges → clipboard text
//	screen.go    the alternate-screen framebuffer: line diffing, clipping
//	layout.go    text → lines: wrapping, the inline/answer shapes, section rules
//	render.go    event → block: what each event means, tool cards, filters
//	usage.go     the context-usage numbers a reason.response carries, formatted
//	transcript.go  ordering blocks into lines: grouping, gaps, surfaces
//	picker.go    the `niq i` menu
//	dialog.go    the bordered, centred card the modals are drawn in
//	menu.go      the modal lists: target, view and input-mode pickers
//	help.go      the `?` / ctrl+? keybinding page
//	dock.go      the pinned bottom area: status/mode rule, composer rows, closing
//	             rule
//	input.go     the composer component: paste chips, attachments, delivery mode,
//	             and how its rows are painted
//	editor.go    the message buffer behind the composer: lines, cursor, kill/yank
//	keys.go      raw input decoding (C0, CSI, Kitty, modifyOtherKeys, paste, mouse,
//	             terminal reports) and the key reader
//	keymap.go    what a decoded key means in each mode (insert / normal / help)
//	chat.go      the chat session: state, the event loop, sending, history paging
//
// The remaining layering is a convention rather than an enforced compile boundary:
// one package keeps the render helpers unexported and free to change together. The
// next seams to cut along are styles/theme, screen, viewport/selection and input —
// they are promoted when a second consumer appears or a layer needs its own tests.
package tui
