// Package layout is the reusable layout layer that stands on its own,
// independent of the chat session:
//
//   - Viewport — the transcript's vertical scroll window (content length, visible
//     height, first line, follow-the-bottom), with all the scroll moves.
//
// It owns nothing but scroll geometry: no events, no styling, no rendering. The
// content it scrolls is produced elsewhere (see ../render for markdown/code).
package layout
