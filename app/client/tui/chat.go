// The conversation view: `niq c <project>`, or the picker's Chat action.
//
// It is a full-screen alternate-buffer view in the shape pi uses: the transcript
// fills the frame and scrolls in a viewport, while the bottom two rows (a hint
// line and the input) stay pinned. The renderer repaints only changed rows, so a
// keystroke touches one row and an event touches the transcript it changed.
//
// The scope covers what a conversation needs and nothing more: send as the
// human's interface worker, watch the live stream, scroll back through paged
// history, and scope the view to one worker. It is not the WebUI's full talk
// view (no attachments, approvals, or per-event detail panes).
package tui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/niq-run/niq/app/standalone/contract"
)

const (
	// idleRead bounds how long the key reader lingers after the view closes, so
	// it never steals a keystroke from whatever runs next.
	idleRead = 200 * time.Millisecond
	// escRead tells a lone Esc from the start of an arrow sequence.
	escRead = 40 * time.Millisecond
	// initialHistoryLimit is the first page loaded when the stream opens; a
	// later page is fetched each time scrolling reaches the top.
	initialHistoryLimit = 80
	historyPageLimit    = 60
)

// historyQuery keeps lifecycle noise out of paging, mirroring the WebUI's talk
// history: worker.* is dropped except input/abort, and live-only deltas are
// never persisted anyway. The client-side filter then narrows to one worker.
var historyQuery = contract.HistoryQuery{
	Exclude:       []string{"event.delivered", "reason.start", "reason.end", "reason.thinking_delta", "reason.text_delta", "request.progressed"},
	ExcludePrefix: []string{"worker."},
	Keep:          []string{"worker.input", "worker.abort"},
}

// choice is one entry in the worker menu: the "all" view (empty id), a talk
// partner, or any other worker. Picking a partner both scopes the transcript to
// it and aims the next message at it; picking another worker only scopes the
// view (a message cannot be addressed to a non-partner).
type choice struct {
	label   string
	id      string
	partner bool
}

type historyResult struct {
	events []contract.Event
	err    error
	limit  int
}

type sendResult struct {
	text string
	err  error
}

// chat is the whole view's state. Rendering is a pure function of it, so the
// main loop only mutates state and calls render.
type chat struct {
	scr       *screen
	client    *contract.Client
	projectID string
	sends     chan sendResult

	width, height int
	ed            *editor

	partners []contract.Worker
	choices  []choice
	target   string // where the next message goes
	filter   string // "" = all workers, else one worker

	events     []contract.Event
	seen       map[string]bool
	lines      []string
	linesDirty bool

	vp        viewport  // transcript scroll window (standalone component)
	sel       selection // mouse text selection over the transcript
	watermark string

	selecting bool // picking a worker in the dock
	selCur    int

	initialLoaded bool
	historyDone   bool
	loading       bool

	status string
}

// RunChat opens the conversation view for one project on a gateway.
func RunChat(baseURL, projectID string) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return errNotTerminal
	}

	client := contract.New(baseURL)
	workers, err := client.Workers(projectID)
	if err != nil {
		return err
	}
	partners := talkPartners(workers)
	if len(partners) == 0 {
		return fmt.Errorf("project %s has no talk partner (a reason, coding or niq worker)", projectID)
	}

	state, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	defer term.Restore(fd, state)

	scr := newScreen(os.Stdout, fd)
	scr.start()
	defer scr.stop()
	scr.setTitle("niq chat · " + projectID)

	c := &chat{
		scr:       scr,
		client:    client,
		projectID: projectID,
		width:     scr.w,
		height:    scr.h,
		ed:        &editor{},
		partners:  partners,
		choices:   buildChoices(workers),
		target:    pickTarget(partners),
		vp:        viewport{follow: true},
		seen:      map[string]bool{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := make(chan contract.Event, 128)
	opened := make(chan string, 4)
	notes := make(chan string, 16)
	histories := make(chan historyResult, 4)
	c.sends = make(chan sendResult, 4)
	keys := make(chan press, 32)
	followDone, readDone := make(chan struct{}), make(chan struct{})

	go func() {
		defer close(followDone)
		follow(ctx, client, projectID, events, opened, notes)
	}()
	go func() {
		defer close(readDone)
		readKeys(ctx, keys)
	}()

	c.render()

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

loop:
	for {
		select {
		case ev := <-events:
			if c.addLive(ev) {
				c.render()
			}
		case wm := <-opened:
			c.status = ""
			c.watermark = wm
			if !c.initialLoaded {
				c.initialLoaded = true
				c.startHistory(ctx, wm, initialHistoryLimit, histories)
			}
			c.render()
		case res := <-histories:
			c.applyHistory(res)
			c.render()
		case msg := <-notes:
			c.status = msg
			c.render()
		case res := <-c.sends:
			c.applySend(res)
			c.render()
		case <-ticker.C:
			c.scr.measure()
			if c.scr.w != c.width || c.scr.h != c.height {
				c.width, c.height = c.scr.w, c.scr.h
				c.linesDirty = true
				c.sel.clear() // re-wrapping invalidates line indices
				c.render()
			}
		case p := <-keys:
			quit := c.handleKey(ctx, p, histories)
			c.render()
			if quit {
				break loop
			}
		}
	}

	// Stop the pumps and let the reader finish before the terminal is restored,
	// so it cannot consume a keystroke meant for whatever runs next.
	cancel()
	<-readDone
	<-followDone
	return nil
}

// ── Rendering ──

func (c *chat) viewHeight() int {
	// The dock is a blank spacer, the status rule and the input; tall terminals
	// also get a closing rule under the input.
	bottom := 3
	switch {
	case c.height >= 9:
		bottom = 4
	case c.height < 5:
		bottom = 2
	}
	v := c.height - bottom
	if v < 1 {
		v = 1
	}
	return v
}

// rebuild recomputes the transcript lines from the retained events and filter.
// It is the only place that walks the event list, so live events and filter
// changes cost one render rather than one per event.
func (c *chat) rebuild() {
	c.lines = renderEvents(c.events, c.filter, c.width)
	c.linesDirty = false
	c.vp.resize(c.viewHeight())
	c.vp.setTotal(len(c.lines))
}

// render paints one full frame: the visible transcript window, then the dock — a
// status/rule border, the input on its surface, and a closing rule.
func (c *chat) render() {
	if c.width <= 0 || c.height <= 0 {
		return
	}
	if c.linesDirty {
		c.rebuild()
	}
	vh := c.viewHeight()
	c.vp.resize(vh)
	c.vp.setTotal(len(c.lines))
	start, _ := c.vp.window()

	dock := bandDock.bg()
	rows := make([]string, c.height)
	for i := 0; i < vh && i < c.height; i++ {
		idx := start + i
		if idx >= len(c.lines) {
			break
		}
		line := c.lines[idx]
		if from, to, ok := c.sel.columns(idx, visibleWidth(line)); ok {
			line = selectRange(line, from, to)
		}
		rows[i] = line
	}

	// Dock, from the top: a blank spacer, the status/rule border, the input, and
	// (on a tall terminal) a closing rule under the input.
	bottom := c.height - vh
	curRow, curCol := -1, 0
	idx := vh
	if bottom >= 3 && idx < c.height {
		rows[idx] = ""
		idx++
	}
	if idx < c.height {
		rows[idx] = bandLine(c.hint(), c.width, 0, dock)
		idx++
	}
	if idx < c.height {
		line, col := c.inputLine()
		rows[idx] = bandLine(line, c.width, 0, dock)
		curRow, curCol = idx, col
		idx++
	}
	if bottom >= 4 && idx < c.height {
		rows[idx] = bandLine(c.rule(), c.width, 0, dock)
	}
	if c.selecting {
		curRow = -1 // no text caret while picking a worker
	}
	c.scr.render(rows, curRow, curCol)
}

// hint is the dock's top border, pi-style: the status is drawn *into* the rule
// (`── → target · view: all ───── ctrl+u/d … ──`), so the target, scope and keys
// stay visible without costing the transcript a row. Keys move to the right when
// there is room; otherwise the rule simply fills.
func (c *chat) hint() string {
	if c.width < 1 {
		return ""
	}
	var left strings.Builder
	left.WriteString(brandBold("→ " + orStr(c.target, "?")))
	view := "all"
	if c.filter != "" {
		view = c.filter
	}
	left.WriteString(dim(" · view: ") + brandBold(view))
	if !c.vp.follow {
		above, below := c.vp.offset()
		left.WriteString(dim(fmt.Sprintf(" · ↑%d ↓%d", above, below)))
	} else if c.loading {
		left.WriteString(dim(" · loading…"))
	}
	if c.status != "" {
		left.WriteString(dim(" · " + c.status))
	}
	l := left.String()
	lead := dim("── ")
	used := 3 + visibleWidth(l)
	if used >= c.width {
		return clipStyled(lead+l, c.width)
	}
	right := dim("ctrl+u/d page · ctrl+y/e line · ctrl+t worker · ctrl+c quit")
	rw := visibleWidth(right)
	if used+rw+6 <= c.width {
		fill := c.width - used - rw - 5
		return lead + l + dim(" ") + dim(strings.Repeat("─", fill)) + dim(" ") + right + dim(" ──")
	}
	fill := c.width - used - 3
	if fill < 0 {
		fill = 0
	}
	return lead + l + dim(" ") + dim(strings.Repeat("─", fill)) + dim("──")
}

// rule is a full-width dim horizontal rule.
func (c *chat) rule() string {
	w := c.width
	if w < 1 {
		w = 1
	}
	return dim(strings.Repeat("─", w))
}

// inputLine renders the input row and the caret's cell offset. The editor shows
// a horizontal window so a long line stays on one row.
func (c *chat) inputLine() (string, int) {
	if c.selecting {
		return c.selectorLine(), 0
	}
	const promptCells = 2
	avail := c.width - promptCells - 1
	if avail < 1 {
		avail = 1
	}
	start, end, curCells := c.ed.window(avail)
	return brand("› ") + string(c.ed.buf[start:end]), promptCells + curCells
}

func (c *chat) selectorLine() string {
	if len(c.choices) == 0 {
		return ""
	}
	ch := c.choices[c.selCur]
	label := ch.label
	if ch.id == "" {
		label = "all"
	}
	return dim("worker ") + brandBold("["+label+"]") +
		dim(fmt.Sprintf("  (%d/%d) ↑/↓ j/k ctrl+p/n · enter · esc", c.selCur+1, len(c.choices)))
}

// ── State transitions ──

func (c *chat) addLive(ev contract.Event) bool {
	if ev.ID != "" {
		if c.seen[ev.ID] {
			return false
		}
		c.seen[ev.ID] = true
	}
	c.events = append(c.events, ev)
	c.linesDirty = true
	return true
}

// mergeHistory prepends older events (server order is newest-first, so it is
// reversed here) and returns how many were new. It is the dedupe point: an event
// may arrive both live and in a history page after a reconnect.
func (c *chat) mergeHistory(older []contract.Event) int {
	var batch []contract.Event
	for i := len(older) - 1; i >= 0; i-- {
		ev := older[i]
		if ev.ID != "" {
			if c.seen[ev.ID] {
				continue
			}
			c.seen[ev.ID] = true
		}
		batch = append(batch, ev)
	}
	if len(batch) == 0 {
		return 0
	}
	c.events = append(batch, c.events...)
	return len(batch)
}

func (c *chat) applyHistory(res historyResult) {
	c.loading = false
	if res.err != nil {
		c.status = "history: " + res.err.Error()
		return
	}
	if c.linesDirty {
		c.rebuild()
	}
	before := len(c.lines)
	c.mergeHistory(res.events)
	c.linesDirty = true
	c.rebuild()
	if !c.vp.follow {
		// Keep the same content under the cursor when older lines are prepended.
		c.vp.scrollLines(len(c.lines) - before)
	}
	if len(res.events) < res.limit {
		c.historyDone = true
	}
	c.sel.clear()
}

func (c *chat) applySend(res sendResult) {
	if res.err == nil {
		return
	}
	c.status = "send failed: " + res.err.Error()
	// Keep the draft if the user has not started another message.
	if c.ed.text() == "" {
		for _, r := range res.text {
			c.ed.insert(r)
		}
	}
}

func (c *chat) startHistory(ctx context.Context, anchor string, limit int, out chan<- historyResult) {
	if anchor == "" || c.loading {
		return
	}
	c.loading = true
	go func() {
		evs, err := c.client.EventsBefore(ctx, c.projectID, anchor, historyQuery, limit)
		select {
		case out <- historyResult{events: evs, err: err, limit: limit}:
		case <-ctx.Done():
		}
	}()
}

func (c *chat) oldestAnchor() string {
	if len(c.events) > 0 {
		return c.events[0].ID
	}
	return c.watermark
}

// maybeLoadOlder asks for the previous page once the transcript is scrolled to
// its top, so every scroll path (page keys, line keys, wheel, drag) pages history
// the same way.
func (c *chat) maybeLoadOlder(ctx context.Context, histories chan<- historyResult) {
	if c.vp.atTop() && !c.historyDone && !c.loading {
		c.startHistory(ctx, c.oldestAnchor(), historyPageLimit, histories)
	}
}

// cellAt maps a screen cell inside the transcript (0-based) to an absolute
// transcript point, clamping inside the content so a drag past either edge still
// has a target.
func (c *chat) cellAt(row, col int) (selPoint, bool) {
	if len(c.lines) == 0 || row < 0 || row >= c.viewHeight() {
		return selPoint{}, false
	}
	line := c.vp.first() + row
	if line >= len(c.lines) {
		line = len(c.lines) - 1
	}
	if line < 0 {
		return selPoint{}, false
	}
	w := visibleWidth(c.lines[line])
	if col < 0 {
		col = 0
	}
	if col > w {
		col = w
	}
	return selPoint{line: line, col: col}, true
}

// dragTo extends the mouse selection, scrolling the transcript when the pointer
// is dragged past the top or the bottom edge so a selection can span more than
// one screen.
func (c *chat) dragTo(ctx context.Context, row, col int, histories chan<- historyResult) {
	if !c.sel.active {
		return
	}
	vh := c.viewHeight()
	for row < 0 {
		c.vp.lineUp()
		row++
	}
	for row >= vh {
		c.vp.lineDown()
		row--
	}
	if p, ok := c.cellAt(row, col); ok {
		c.sel.extend(p)
	}
	c.maybeLoadOlder(ctx, histories)
}

func (c *chat) applyChoice() {
	c.selecting = false
	if len(c.choices) == 0 {
		return
	}
	ch := c.choices[c.selCur]
	switch {
	case ch.id == "":
		c.filter = ""
	case ch.partner:
		c.filter = ch.id
		c.target = ch.id
	default:
		// A non-partner can be watched but not messaged; keep the send target.
		c.filter = ch.id
	}
	c.linesDirty = true
	c.vp.followBottom()
	c.sel.clear()
	c.status = ""
}

func (c *chat) currentChoice() int {
	for i, ch := range c.choices {
		if ch.id == c.filter {
			return i
		}
	}
	return 0
}

// handleKey applies one keystroke. It returns true when the user asked to leave.
func (c *chat) handleKey(ctx context.Context, p press, histories chan<- historyResult) bool {
	if c.selecting {
		if len(c.choices) == 0 {
			c.selecting = false
			return false
		}
		switch {
		case p.kind == pressInterrupt:
			return true
		case p.kind == pressEsc:
			c.selecting = false
		case p.kind == pressUp || p.kind == pressPrev || (p.kind == pressRune && p.r == 'k'):
			c.selCur = (c.selCur - 1 + len(c.choices)) % len(c.choices)
		case p.kind == pressDown || p.kind == pressNext || (p.kind == pressRune && p.r == 'j'):
			c.selCur = (c.selCur + 1) % len(c.choices)
		case p.kind == pressEnter:
			c.applyChoice()
		}
		return false
	}

	switch p.kind {
	case pressInterrupt:
		return true
	case pressTarget:
		if len(c.choices) <= 1 {
			c.status = "no other workers"
			return false
		}
		c.selecting = true
		c.selCur = c.currentChoice()
	case pressPageUp: // ctrl+u, ctrl+b, pgup
		c.vp.pageUp()
		c.maybeLoadOlder(ctx, histories)
	case pressPageDown: // ctrl+d, ctrl+f, pgdn
		c.vp.pageDown()
	case pressLineUp, pressPrev: // ctrl+y / ctrl+p
		c.vp.lineUp()
		c.maybeLoadOlder(ctx, histories)
	case pressLineDown, pressNext: // ctrl+e / ctrl+n
		c.vp.lineDown()
	case pressWheelUp:
		c.vp.scrollLines(-wheelStep)
		c.maybeLoadOlder(ctx, histories)
	case pressWheelDown:
		c.vp.scrollLines(wheelStep)
	case pressClick:
		// A click clears any old selection and anchors a new one.
		c.sel.clear()
		if pt, ok := c.cellAt(p.y-1, p.x-1); ok {
			c.sel.begin(pt)
		}
	case pressDrag:
		c.dragTo(ctx, p.y-1, p.x-1, histories)
	case pressRelease:
		if !c.sel.active {
			return false
		}
		if !c.sel.dragging {
			c.sel.clear()
			return false
		}
		if text := selectionText(c.lines, &c.sel); text != "" {
			c.scr.copyToClipboard(text)
			c.status = fmt.Sprintf("copied %d chars", utf8.RuneCountInString(text))
		}
	case pressEnter:
		text := strings.TrimSpace(c.ed.text())
		if text == "" {
			return false
		}
		target := c.target
		c.ed.clear()
		go func(msg, to string) {
			err := c.client.SendInput(c.projectID, contract.Input{
				Content: []contract.Block{{Type: "text", Text: msg}},
				Targets: []string{to},
			})
			select {
			case c.sends <- sendResult{text: msg, err: err}:
			case <-ctx.Done():
			}
		}(text, target)
	case pressRune:
		c.ed.insert(p.r)
	case pressBackspace:
		c.ed.backspace()
	case pressDelete:
		c.ed.delete()
	case pressLeft:
		c.ed.left()
	case pressRight:
		c.ed.right()
	case pressHome:
		c.ed.home()
	case pressEnd:
		c.ed.end()
	case pressKill:
		c.ed.clear()
	}
	return false
}

// buildChoices is the worker menu: "all", then every talk partner, then the
// remaining workers (watchable but not addressable). Ordering keeps the
// conversational workers at the top of the cycle.
func buildChoices(workers []contract.Worker) []choice {
	ch := make([]choice, 0, len(workers)+1)
	ch = append(ch, choice{label: "all", id: ""})
	var others []contract.Worker
	for _, w := range workers {
		if talkPartnerTypes[w.Type] {
			ch = append(ch, choice{label: workerName(w), id: w.ID, partner: true})
			continue
		}
		others = append(others, w)
	}
	for _, w := range others {
		ch = append(ch, choice{label: workerName(w), id: w.ID})
	}
	return ch
}

func orStr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// ── Live stream ──

// follow keeps a live subscription open, posting the watermark on each connect
// and every event as it arrives. A stopped or restarting project answers with an
// error rather than hanging, which is what the backoff is for.
func follow(ctx context.Context, client *contract.Client, projectID string, events chan<- contract.Event, opened chan<- string, notes chan<- string) {
	backoff := time.Second
	const maxBackoff = 5 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		stream, err := client.Follow(ctx, projectID)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			notify(ctx, notes, "stream: "+err.Error())
			if !sleep(ctx, backoff) {
				return
			}
			if backoff < maxBackoff {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		if wm := stream.Watermark(); wm != "" {
			select {
			case opened <- wm:
			case <-ctx.Done():
				stream.Close()
				return
			}
		}
		for {
			ev, err := stream.Next()
			if err != nil {
				stream.Close()
				if ctx.Err() != nil {
					return
				}
				notify(ctx, notes, "stream dropped: "+err.Error())
				break
			}
			select {
			case events <- ev:
			case <-ctx.Done():
				stream.Close()
				return
			}
		}
		if !sleep(ctx, backoff) {
			return
		}
	}
}

func notify(ctx context.Context, notes chan<- string, msg string) {
	select {
	case notes <- msg:
	case <-ctx.Done():
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// ── Keys ──

type pressKind int

const (
	pressOther pressKind = iota
	pressRune
	pressEnter
	pressBackspace
	pressDelete
	pressLeft
	pressRight
	pressUp
	pressDown
	pressHome
	pressEnd
	pressKill
	pressPageUp
	pressPageDown
	pressLineUp
	pressLineDown
	pressPrev
	pressNext
	pressWheelUp
	pressWheelDown
	pressClick
	pressDrag
	pressRelease
	pressTarget
	pressEsc
	pressInterrupt
)

// wheelStep is how many lines one wheel notch moves. One line keeps a trackpad
// or wheel feeling smooth and slow, as pi's does.
const wheelStep = 1

type press struct {
	kind pressKind
	r    rune
	x, y int // terminal cell (1-based) for mouse reports
}

// readKeys decodes stdin keystrokes onto out until the context ends. Reads use a
// short deadline so the goroutine can notice cancellation while idle; a keypress
// itself returns immediately.
func readKeys(ctx context.Context, out chan<- press) {
	in := bufio.NewReader(os.Stdin)
	for {
		_ = os.Stdin.SetReadDeadline(time.Now().Add(idleRead))
		r, _, err := in.ReadRune()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, os.ErrDeadlineExceeded) {
				continue
			}
			select {
			case out <- press{kind: pressInterrupt}:
			case <-ctx.Done():
			}
			return
		}
		_ = os.Stdin.SetReadDeadline(time.Time{})
		select {
		case out <- decodePress(in, r):
		case <-ctx.Done():
			return
		}
	}
}

// decodePress maps one rune to a press, reading the rest of an escape sequence
// when the rune is Esc. Paging follows vim: ctrl+u/d and ctrl+b/f page, ctrl+y/e
// scroll one line, ctrl+p/n also step a line; ctrl+k clears the input; ctrl+t
// opens the worker menu; ctrl+c leaves.
func decodePress(in *bufio.Reader, r rune) press {
	switch r {
	case '\r', '\n':
		return press{kind: pressEnter}
	case 0x7f, 0x08:
		return press{kind: pressBackspace}
	case 0x03:
		return press{kind: pressInterrupt}
	case 0x04: // Ctrl+D
		return press{kind: pressPageDown}
	case 0x15: // Ctrl+U
		return press{kind: pressPageUp}
	case 0x02: // Ctrl+B
		return press{kind: pressPageUp}
	case 0x06: // Ctrl+F
		return press{kind: pressPageDown}
	case 0x19: // Ctrl+Y
		return press{kind: pressLineUp}
	case 0x05: // Ctrl+E
		return press{kind: pressLineDown}
	case 0x10: // Ctrl+P
		return press{kind: pressPrev}
	case 0x0e: // Ctrl+N
		return press{kind: pressNext}
	case 0x0b: // Ctrl+K
		return press{kind: pressKill}
	case 0x14: // Ctrl+T
		return press{kind: pressTarget}
	case 0x01: // Ctrl+A
		return press{kind: pressHome}
	case 0x1b:
		return decodeEscape(in)
	}
	if r >= 0x20 && r != 0x7f {
		return press{kind: pressRune, r: r}
	}
	return press{kind: pressOther}
}

// decodeEscape classifies an escape sequence. A lone Esc cancels the worker
// menu; the arrows, Home/End/Delete, and PageUp/PageDown map to their keys.
func decodeEscape(in *bufio.Reader) press {
	_ = os.Stdin.SetReadDeadline(time.Now().Add(escRead))
	defer os.Stdin.SetReadDeadline(time.Time{})
	next, _, err := in.ReadRune()
	if err != nil {
		return press{kind: pressEsc}
	}
	if next != '[' && next != 'O' {
		return press{kind: pressEsc}
	}
	final, _, err := in.ReadRune()
	if err != nil {
		return press{kind: pressOther}
	}
	switch final {
	case 'A':
		return press{kind: pressUp}
	case 'B':
		return press{kind: pressDown}
	case 'C':
		return press{kind: pressRight}
	case 'D':
		return press{kind: pressLeft}
	case 'H':
		return press{kind: pressHome}
	case 'F':
		return press{kind: pressEnd}
	case '1', '7':
		return tildePress(in, pressHome)
	case '4', '8':
		return tildePress(in, pressEnd)
	case '3':
		return tildePress(in, pressDelete)
	case '5':
		return tildePress(in, pressPageUp)
	case '6':
		return tildePress(in, pressPageDown)
	case '<':
		return decodeMouse(in)
	}
	return press{kind: pressOther}
}

// decodeMouse parses the rest of an SGR mouse report (“CSI < b ; x ; y M|m“).
// The wheel scrolls; a left button press, drag and release drive the transcript's
// text selection so the mouse can select and copy inside the alternate screen.
func decodeMouse(in *bufio.Reader) press {
	var vals [3]int
	field := 0
	b, err := in.ReadByte()
	if err != nil {
		return press{kind: pressOther}
	}
	for {
		switch {
		case b >= '0' && b <= '9':
			vals[field] = vals[field]*10 + int(b-'0')
		case b == ';' && field < 2:
			field++
		default:
			goto done
		}
		b, err = in.ReadByte()
		if err != nil {
			return press{kind: pressOther}
		}
	}
done:
	button, x, y := vals[0], vals[1], vals[2]
	release := b == 'm'
	wheel := button&64 != 0
	motion := button&32 != 0
	left := button&3 == 0
	switch {
	case wheel && !release:
		if button&1 == 0 {
			return press{kind: pressWheelUp}
		}
		return press{kind: pressWheelDown}
	case wheel, !left:
		return press{kind: pressOther}
	case motion:
		return press{kind: pressDrag, x: x, y: y}
	case release:
		return press{kind: pressRelease, x: x, y: y}
	default:
		return press{kind: pressClick, x: x, y: y}
	}
}

// tildePress consumes the "~" that ends a CSI 1~/4~/3~/5~/6~ key.
func tildePress(in *bufio.Reader, kind pressKind) press {
	if r, _, err := in.ReadRune(); err == nil && r == '~' {
		return press{kind: kind}
	}
	return press{kind: pressOther}
}

// ── Line editor ──

// editor is the single-line input at the bottom of the view.
type editor struct {
	buf []rune
	cur int
}

func (e *editor) insert(r rune) {
	e.buf = append(e.buf, 0)
	copy(e.buf[e.cur+1:], e.buf[e.cur:])
	e.buf[e.cur] = r
	e.cur++
}

func (e *editor) backspace() {
	if e.cur == 0 {
		return
	}
	e.buf = append(e.buf[:e.cur-1], e.buf[e.cur:]...)
	e.cur--
}

func (e *editor) delete() {
	if e.cur >= len(e.buf) {
		return
	}
	e.buf = append(e.buf[:e.cur], e.buf[e.cur+1:]...)
}

func (e *editor) left() {
	if e.cur > 0 {
		e.cur--
	}
}

func (e *editor) right() {
	if e.cur < len(e.buf) {
		e.cur++
	}
}

func (e *editor) home() { e.cur = 0 }
func (e *editor) end()  { e.cur = len(e.buf) }

func (e *editor) clear() {
	e.buf = e.buf[:0]
	e.cur = 0
}

func (e *editor) text() string { return string(e.buf) }

// window returns the runes to show so that the cursor stays visible within avail
// cells, plus the cursor's cell offset inside that window.
func (e *editor) window(avail int) (start, end, curCells int) {
	if avail < 1 {
		avail = 1
	}
	cellsToCursor := 0
	start = e.cur
	for start > 0 {
		w := runeCells(e.buf[start-1])
		if cellsToCursor+w > avail-1 {
			break
		}
		cellsToCursor += w
		start--
	}
	curCells = cellsToCursor
	end = e.cur
	cells := curCells
	for end < len(e.buf) {
		w := runeCells(e.buf[end])
		if cells+w > avail {
			break
		}
		cells += w
		end++
	}
	return start, end, curCells
}
