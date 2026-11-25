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
	"context"
	"fmt"
	"os"
	"time"

	"golang.org/x/term"

	"github.com/niq-run/niq/app/client/tui/live"
	"github.com/niq-run/niq/app/client/tui/stdin"
	"github.com/niq-run/niq/app/client/tui/text"
	"github.com/niq-run/niq/app/standalone/contract"
)

const (
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

type historyResult struct {
	events []contract.Event
	err    error
	limit  int
}

type sendResult struct {
	text  string
	draft *input
	err   error
}

// providerResult is an async provider fetch or model switch, delivered back to
// the chat loop. switched distinguishes a switch reply (sw) from a list fetch
// (list).
type providerResult struct {
	worker   string
	list     contract.ProviderList
	sw       contract.ProviderSwitch
	switched bool
	err      error
}

// chat is the whole view's state. Rendering is a pure function of it, so the
// main loop only mutates state and calls render.
type chat struct {
	scr       *screen
	client    *contract.Client
	projectID string
	sends     chan sendResult

	width, height int
	in            *input
	inputN        int  // dock rows the input occupies, from the last render
	uploadFiles   bool // file attachments travel to the gateway (remote)

	partners []contract.Worker
	targets  []menuItem // the target picker's rows
	target   string     // where the next message goes
	filter   string     // "" = all workers, else one worker

	events     []contract.Event
	seen       map[string]bool
	lines      []string
	linesDirty bool

	vp        viewport  // transcript scroll window (standalone component)
	sel       selection // mouse text selection over the transcript
	watermark string

	// menu is the modal list currently open (target picker or input mode); menuCur
	// is its highlighted row. menuStart/menuCount window it to the frame, and
	// menuTopY/menuX0/menuX1 are the screen cells of its first option row — recorded
	// by the last render so a mouse click can select an option.
	menu                     menuKind
	menuCur                  int
	menuStart, menuCount     int
	menuTopY, menuX0, menuX1 int
	// hintRow is the screen row of the status rule; spans is the clickable
	// segments on it, recorded by the last render.
	hintRow int
	spans   []hintSpan
	// cardRect is the block the open modal covers, so a click outside it closes
	// the modal.
	cardRect rect
	// normal is vim's normal mode: keys move through the transcript and do not
	// type. It starts off, so the view opens ready to write; esc enters it, i or a
	// click in the composer leaves it.
	normal bool
	// help is the keybinding popup. Any key closes it.
	help bool
	// focus is the status-rule button the keyboard focus is on (Tab / Shift+Tab
	// cycle them); hintNone means the composer has the keyboard. While a button is
	// focused, Enter activates it instead of sending.
	focus hintAct
	// ctxRow is the screen row of the context/model line; ctxModelX0/X1 bracket the
	// clickable model at its right. -1 when not drawn.
	ctxRow                 int
	ctxModelX0, ctxModelX1 int
	// providers caches each reason worker's selectable providers and current
	// model, keyed by worker id; modelWorker is the worker the model picker is
	// switching. provResults carries the async fetch/switch back to the loop.
	providers         map[string]contract.ProviderList
	fetchingProviders map[string]bool
	providerRetry     map[string]time.Time
	modelWorker       string
	provResults       chan providerResult

	initialLoaded bool
	historyDone   bool
	loading       bool

	// liveThinking is the reasoning streamed since the last final trace: deltas are
	// live-only, so they are buffered here and drawn as the transcript's last block.
	liveThinking       string
	liveThinkingWorker string

	status      string
	statusUntil time.Time // when a flashed status should clear itself
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
		scr:               scr,
		client:            client,
		projectID:         projectID,
		width:             scr.w,
		height:            scr.h,
		in:                newInput(),
		partners:          partners,
		targets:           buildTargets(workers),
		target:            pickTarget(partners),
		uploadFiles:       uploadsFiles(baseURL),
		vp:                viewport{follow: true},
		seen:              map[string]bool{},
		providers:         map[string]contract.ProviderList{},
		fetchingProviders: map[string]bool{},
		providerRetry:     map[string]time.Time{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := make(chan contract.Event, 128)
	opened := make(chan string, 4)
	notes := make(chan string, 16)
	histories := make(chan historyResult, 4)
	c.sends = make(chan sendResult, 4)
	c.provResults = make(chan providerResult, 8)
	keys := make(chan press, 32)
	readDone := make(chan struct{})

	go live.Follow(ctx, client, projectID, events, opened, notes)
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
		case res := <-c.provResults:
			c.applyProviderResult(res)
			c.render()
		case <-ticker.C:
			c.scr.measure()
			if c.scr.w != c.width || c.scr.h != c.height {
				c.width, c.height = c.scr.w, c.scr.h
				c.linesDirty = true
				c.sel.clear() // re-wrapping invalidates line indices
				c.render()
			}
			if !c.statusUntil.IsZero() && time.Now().After(c.statusUntil) {
				c.status, c.statusUntil = "", time.Time{}
				c.render()
			}
			c.syncProviders(ctx)
		case p := <-keys:
			quit := c.handleKey(ctx, p, histories)
			c.render()
			if quit {
				break loop
			}
		}
	}

	// Leave the alternate screen the moment the user quits. Waiting on the
	// network pump first would keep a dead chat frame up, which reads as "ctrl+c
	// did nothing" until the connection tears down.
	cancel()
	_ = stdin.SetDeadline(time.Now()) // wake a reader blocked in ReadRune
	<-readDone
	_ = stdin.SetDeadline(time.Time{})
	c.scr.stop()
	return nil
}

// ── State transitions ──

// addLive appends one live event. Thinking deltas are transient and never stored:
// they accumulate into a buffer that is drawn as the last block, so a trace
// streams in as it is produced, and the final reason.thinking event (which carries
// the whole text) clears the buffer.
func (c *chat) addLive(ev contract.Event) bool {
	switch ev.Type {
	case "reason.thinking_delta":
		s := text.ContentText(ev.Payload["content"], ev.Payload["text"])
		if s == "" {
			return false
		}
		c.liveThinking += s
		c.liveThinkingWorker = ev.WorkerID
		c.linesDirty = true
		return true
	case "reason.thinking":
		c.liveThinking, c.liveThinkingWorker = "", ""
	}
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

// send delivers one composed message. A file attachment is only a reference at
// the far end, so when the gateway is not on this machine the bytes are uploaded
// first and the reply's path is what the message names.
func (c *chat) send(ctx context.Context, in *input, target string) error {
	blocks := in.blocks()
	if c.uploadFiles {
		for i := range blocks {
			b := &blocks[i]
			if b.Type != "file" {
				continue
			}
			data, err := os.ReadFile(b.Path)
			if err != nil {
				return fmt.Errorf("read %s: %w", b.Name, err)
			}
			up, err := c.client.Upload(ctx, c.projectID, b.Name, data)
			if err != nil {
				return fmt.Errorf("upload %s: %w", b.Name, err)
			}
			b.Path = up.Path
			if up.Size > 0 {
				b.Size = up.Size
			}
		}
	}
	return c.client.SendInput(c.projectID, contract.Input{
		Content:   blocks,
		Targets:   []string{target},
		InputMode: in.mode,
	})
}

func (c *chat) applySend(res sendResult) {
	if res.err == nil {
		return
	}
	c.status = "send failed: " + res.err.Error()
	// Keep the draft if the user has not started another message.
	if c.in.empty() {
		c.in.setDraft(res.text)
	}
}

// syncProviders starts a fetch for the dock's bound worker when its providers
// are not cached yet. A failed fetch backs off for a few seconds so the ticker
// does not hammer an offline worker.
func (c *chat) syncProviders(ctx context.Context) {
	w := c.contextWorker()
	if w == "" || c.client == nil || c.provResults == nil {
		return
	}
	if _, ok := c.providers[w]; ok {
		return
	}
	if t, ok := c.providerRetry[w]; ok && time.Now().Before(t) {
		return
	}
	c.startProviders(ctx, w, c.provResults)
}

// startProviders fetches a worker's providers once, in the background.
func (c *chat) startProviders(ctx context.Context, worker string, out chan providerResult) {
	if worker == "" || c.client == nil || out == nil || c.fetchingProviders[worker] {
		return
	}
	if _, ok := c.providers[worker]; ok {
		return
	}
	if c.fetchingProviders == nil {
		c.fetchingProviders = map[string]bool{}
	}
	c.fetchingProviders[worker] = true
	client, projectID := c.client, c.projectID
	go func() {
		list, err := client.WorkerProviders(projectID, worker)
		select {
		case out <- providerResult{worker: worker, list: list, err: err}:
		case <-ctx.Done():
		}
	}()
}

// startSwitch asks a worker to change its active provider/model, in the
// background.
func (c *chat) startSwitch(ctx context.Context, worker, provider, model string, out chan providerResult) {
	if c.client == nil || out == nil {
		return
	}
	client, projectID := c.client, c.projectID
	go func() {
		sw, err := client.SetWorkerProvider(projectID, worker, provider, model)
		select {
		case out <- providerResult{worker: worker, sw: sw, switched: true, err: err}:
		case <-ctx.Done():
		}
	}()
}

// applyProviderResult folds a provider fetch or model switch back into the cache.
func (c *chat) applyProviderResult(res providerResult) {
	if res.switched {
		if res.err != nil {
			c.flash("model switch failed: " + res.err.Error())
			return
		}
		if !res.sw.Done || res.sw.Error != "" {
			msg := res.sw.Error
			if msg == "" {
				msg = "not applied"
			}
			c.flash("model: " + msg)
			return
		}
		list := c.providers[res.worker]
		list.Current = contract.ProviderSelection{Provider: res.sw.Provider, Model: res.sw.Model}
		if c.providers == nil {
			c.providers = map[string]contract.ProviderList{}
		}
		c.providers[res.worker] = list
		c.flash("model → " + res.sw.Model)
		return
	}

	delete(c.fetchingProviders, res.worker)
	if res.err != nil {
		// Back off so the ticker does not spin on an offline worker; retry later.
		c.providerRetry[res.worker] = time.Now().Add(5 * time.Second)
		return
	}
	if c.providers == nil {
		c.providers = map[string]contract.ProviderList{}
	}
	c.providers[res.worker] = res.list
	if c.menu == menuModel && c.modelWorker == res.worker {
		c.menuCur = c.menuIndex(menuModel)
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

func orStr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
