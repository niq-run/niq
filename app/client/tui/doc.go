// Package tui is the conversation view (`niq c <project>`): the chat screen, its
// composers and dock, and the shared styling forwarders. The `niq i` picker is a
// sibling subpackage (../picker).
//
// It is hand-written escape codes and a diff renderer, with no TUI dependency.
//
// The reusable layers are their own subpackages, so they carry their own tests and
// can be read (and imported) without the session:
//
//	clipboard/  the native clipboard fallback next to OSC 52
//	term/       the styling foundation: ANSI styles, the dark/light palette and
//	            background bands, cell measurement, and ANSI-aware clip/slice
//	screen/     the alternate-screen framebuffer: line diffing, clipping
//	keys/       raw input decoding (C0, CSI, Kitty, modifyOtherKeys, paste, mouse,
//	            terminal reports) and the key reader
//	layout/     the layout layer: the transcript's scroll viewport
//	render/     the renderers: the markdown viewer (with GFM tables) and the code
//	            highlighter it hands fenced blocks to
//	ui/         the reusable small controls: the modal card the pickers drape over
//	picker/     the `niq i` picker app, with its own key reader and table output
//	live/       the live subscription pump and its reconnect backoff
//	stdin/      the stdin pump and its userspace read deadlines
//	text/       event-payload reading and the control/ANSI sanitising of text
//
// The rest is the chat view itself, grouped by responsibility (bottom layer first):
//
//	styles.go    thin forwarders onto term, so the view keeps reading `dim(…)`,
//	             `brand(…)`, `displayWidth(…)`
//	keys_shim.go thin aliases/readers onto keys, so keymap keeps naming presses
//	             `press` / `pressXxx`
//	blocks.go    text → lines: wrapping, the inline/answer shapes, section rules
//	render.go    event → block (what each event means, tool cards, filters) and
//	             the transcript: ordering blocks into lines, gaps, surfaces
//	usage.go     the context-usage numbers a reason.response carries, formatted,
//	             and the detail panel their click opens
//	selection.go the mouse text selection over the transcript, mapping cell
//	             ranges back to clipboard text (the scroll window is layout.Viewport)
//	keymap.go    what a decoded key means in each mode (insert / normal / help)
//	input.go     the composer: paste chips, attachments, delivery mode, how its
//	             rows are painted, and the line-editor buffer underneath
//	dock.go      the pinned bottom area: status/mode rule, composer rows, and the
//	             context/model line under the divider
//	menu.go      the modal lists: target, view, input-mode and model pickers, plus
//	             the `?` / ctrl+? keybinding page
//	chat.go      the chat session: state, the event loop, sending, history paging,
//	             and the worker roster/routing helpers
//
// The stateful widget that remains in this package (selection) is entangled with
// the session — chat reads and writes its fields directly — so it is kept here as
// app plumbing rather than exported. term/, screen/, keys/, render/, layout/, ui/
// and picker/ are the pieces that stand on their own.
package tui
