// The transcript: how a chronological event slice becomes terminal lines, and how
// one block is painted on its surface. The event interpretation itself lives in
// render.go.
package tui

import (
	"github.com/niq-run/niq/app/standalone/contract"
)

// renderEvents lays a chronological event slice out as transcript lines, scoped
// to filter. It is a pure function of the events and the width, so a filter
// change just re-renders the retained events instead of re-fetching.
//
// A tool call and its terminal answer are one card: the call on the first line,
// a blank spacer, then the reply. Cards always stand alone — unlike thinking
// traces, two calls are two panels with a gap between them.
func renderEvents(events []contract.Event, filter string, width int) []string {
	return renderTranscript(events, filter, width, "", "")
}

// renderTranscript is renderEvents plus the reasoning streamed but not yet final:
// it is live-only text, so it is drawn as one more block at the end rather than
// stored in the event list.
func renderTranscript(events []contract.Event, filter string, width int, liveThinking, liveWorker string) []string {
	// Index calls and answers by request id so a card is assembled in one pass.
	calls := map[string]contract.Event{}
	answers := map[string]contract.Event{}
	for _, ev := range events {
		if ev.RequestID == "" {
			continue
		}
		if terminalAnswer(ev.Type) {
			answers[ev.RequestID] = ev
		} else if toolLike(ev) {
			calls[ev.RequestID] = ev
		}
	}

	type block struct {
		lines []string
		band  bandKind
		chat  bool
		solo  bool
	}
	var blocks []block
	for _, ev := range events {
		if !matchesFilter(ev, filter) {
			continue
		}
		if toolLike(ev) {
			var ans *contract.Event
			if a, ok := answers[ev.RequestID]; ok && ev.RequestID != "" {
				ans = &a
			}
			r := toolBlock(ev, ans, width)
			blocks = append(blocks, block{lines: r.body, band: r.band, solo: true})
			continue
		}
		if terminalAnswer(ev.Type) && ev.RequestID != "" {
			// Already drawn inside its call's card — unless that call is filtered
			// out of this view, in which case the reply stands on its own.
			if inv, ok := calls[ev.RequestID]; ok && matchesFilter(inv, filter) {
				continue
			}
		}
		r, ok := describe(ev, width)
		if !ok {
			continue
		}
		body := blockBody(r, width)
		if r.chat {
			blocks = append(blocks, block{lines: body, band: r.band, chat: true})
			continue
		}
		// Consecutive plain event rows on the same surface form one panel, so a
		// thinking trace is one block rather than a stripe per line.
		if n := len(blocks); n > 0 && !blocks[n-1].chat && !blocks[n-1].solo && blocks[n-1].band == r.band {
			blocks[n-1].lines = append(blocks[n-1].lines, body...)
			continue
		}
		blocks = append(blocks, block{lines: body, band: r.band})
	}

	if liveThinking != "" && (filter == "" || filter == liveWorker) {
		r := thinkingBlock(liveThinking, width)
		blocks = append(blocks, block{lines: r.body, band: r.band})
	}

	var out []string
	for i, b := range blocks {
		if i > 0 {
			out = append(out, "") // gap between blocks
		}
		out = append(out, renderBlock(b.lines, b.band, width)...)
	}
	return out
}

// renderBlock paints one block's lines on its surface. A banded block is inset by
// one column and padded by one row top and bottom, like pi's Box; a plain block
// (a thinking trace or a worker's answer) is left on the terminal background.
func renderBlock(lines []string, band bandKind, width int) []string {
	bg := band.bg()
	if bg == "" {
		return lines
	}
	bg = blockSurface(band)
	padX := 1
	if band == bandCode {
		padX = 0
	}
	padded := make([]string, 0, len(lines)+2)
	padded = append(padded, "")
	padded = append(padded, lines...)
	padded = append(padded, "")
	out := make([]string, len(padded))
	for i, ln := range padded {
		out[i] = bandLine(ln, width, padX, bg)
	}
	return out
}
