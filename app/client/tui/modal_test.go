package tui

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/niq-run/niq/app/client/tui/layout"
	"github.com/niq-run/niq/app/client/tui/screen"
	"github.com/niq-run/niq/app/client/tui/term"
	"github.com/niq-run/niq/app/client/tui/ui"
)

// helpBox is the help card padded into a standalone block, the shape the tests
// inspect. The view paints through paintCard instead.
func helpBox(width, maxRows int) []string {
	card, boxW := helpCard(width, maxRows)
	totalW := boxW + 2*ui.FrameGapX + 2*ui.FrameLineX
	startX := (width - totalW) / 2
	if startX < 0 {
		startX = 0
	}
	pad := startX + ui.FrameLineX + ui.FrameGapX
	out := make([]string, len(card))
	for i, ln := range card {
		out[i] = strings.Repeat(" ", pad) + ln
	}
	return out
}

// spanFor returns the recorded clickable segment with act.
func spanFor(c *chat, act hintAct) (hintSpan, bool) {
	for _, s := range c.spans {
		if s.act == act {
			return s, true
		}
	}
	return hintSpan{}, false
}

// newTestChat builds a chat with a real (detached) screen at 80x24.
func newTestChat(t *testing.T) (*chat, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	scr := screen.NewScreen(buf, 0)
	return &chat{
		scr: scr, width: scr.W, height: scr.H, in: newInput(),
		vp: layout.Viewport{Follow: true}, seen: map[string]bool{}, target: "niq",
	}, buf
}

// TestCtrlQuestionIsHelp pins the extra way into the keymap: ctrl+? reaches us
// as the 0x1f control byte in the classic encodings, and as codepoint '?' with a
// ctrl modifier under Kitty / modifyOtherKeys. In insert mode it must open help
// rather than type.
func TestCtrlQuestionIsHelp(t *testing.T) {
	if p, ok := controlPress(0x1f); !ok || p.Kind != pressHelp {
		t.Fatalf("0x1f = %+v ok=%v, want pressHelp", p, ok)
	}
	for _, mod := range []int{5, 6} { // ctrl, ctrl+shift
		if p := keyPress('?', mod); p.Kind != pressHelp {
			t.Fatalf("ctrl+? mod %d decoded as %+v, want pressHelp", mod, p)
		}
	}

	c := &chat{in: newInput(), vp: layout.Viewport{Follow: true}}
	c.handleKey(t.Context(), press{Kind: pressHelp}, nil)
	if !c.help {
		t.Fatal("pressHelp did not open the help")
	}
	if got := c.in.text(); got != "" {
		t.Fatalf("the help key typed into the composer: %q", got)
	}
}

// TestRuleSegmentsAreClickable pins the unified rule: target, view, input mode,
// vim state and help each open a popup when clicked.
func TestRuleSegmentsAreClickable(t *testing.T) {
	cases := []struct {
		act  hintAct
		want menuKind
	}{
		{hintTargetPick, menuTarget},
		{hintViewPick, menuView},
		{hintInputModePick, menuMode},
	}
	for _, tc := range cases {
		c, _ := newTestChat(t)
		c.targets = []menuItem{{label: "niq", value: "niq", note: "reason"}}
		c.render()
		sp, ok := spanFor(c, tc.act)
		if !ok {
			t.Fatalf("segment %v is not clickable", tc.act)
		}
		mid := (sp.x0 + sp.x1) / 2
		c.handleKey(t.Context(), press{Kind: pressClick, X: mid + 1, Y: c.hintRow + 1}, nil)
		if c.menu != tc.want {
			t.Fatalf("clicking segment %v opened menu %v, want %v", tc.act, c.menu, tc.want)
		}
	}

	// Help is the fifth segment and opens the page.
	c, _ := newTestChat(t)
	c.render()
	sp, ok := spanFor(c, hintHelp)
	if !ok {
		t.Fatal("help segment is not clickable")
	}
	c.handleKey(t.Context(), press{Kind: pressClick, X: (sp.x0+sp.x1)/2 + 1, Y: c.hintRow + 1}, nil)
	if !c.help {
		t.Fatal("clicking help did not open the page")
	}
}

// TestTargetPicker pins the target popup: ctrl+t opens it on the current target,
// and a pick aims the next message without touching the view scope.
func TestTargetPicker(t *testing.T) {
	c, buf := newTestChat(t)
	c.targets = []menuItem{
		{label: "niq", value: "niq", note: "reason"},
		{label: "coder", value: "code", note: "coding"},
	}
	c.handleKey(t.Context(), press{Kind: pressTarget}, nil)
	if c.menu != menuTarget {
		t.Fatal("ctrl+t did not open the target popup")
	}
	if c.menuCur != 0 {
		t.Fatalf("target popup opened on row %d, want the current target", c.menuCur)
	}
	c.handleKey(t.Context(), press{Kind: pressDown}, nil)
	c.render()
	if frame := stripANSI(buf.String()); !strings.Contains(frame, "┌ target") {
		t.Fatalf("target popup not drawn:\n%s", frame)
	}
	c.handleKey(t.Context(), press{Kind: pressEnter}, nil)
	if c.menu != menuNone {
		t.Fatal("enter did not close the target popup")
	}
	if c.target != "code" {
		t.Fatalf("target = %q, want code", c.target)
	}
	if c.filter != "" {
		t.Fatalf("target pick changed the view filter to %q", c.filter)
	}
}

// TestViewPicker pins the view popup: ctrl+v opens it, "all" is the first row,
// and a pick scopes the transcript without retargeting the message.
func TestViewPicker(t *testing.T) {
	c, buf := newTestChat(t)
	c.targets = []menuItem{
		{label: "niq", value: "niq", note: "reason"},
		{label: "coder", value: "code", note: "coding"},
	}
	c.handleKey(t.Context(), press{Kind: pressView}, nil)
	if c.menu != menuView {
		t.Fatal("ctrl+v did not open the view popup")
	}
	if got := c.menuItems(); len(got) != 3 || got[0].value != "" {
		t.Fatalf("view rows = %+v, want all + partners", got)
	}
	c.handleKey(t.Context(), press{Kind: pressDown}, nil) // "all" -> niq
	c.render()
	if frame := stripANSI(buf.String()); !strings.Contains(frame, "┌ view") {
		t.Fatalf("view popup not drawn:\n%s", frame)
	}
	c.handleKey(t.Context(), press{Kind: pressEnter}, nil)
	if c.filter != "niq" {
		t.Fatalf("view = %q, want niq", c.filter)
	}
	if c.target != "niq" {
		t.Fatalf("view pick retargeted the message: %q", c.target)
	}
}

// TestVimStateClickToggles pins the two-state shortcut: clicking insert/normal
// flips it directly, with no popup.
func TestVimStateClickToggles(t *testing.T) {
	c, _ := newTestChat(t)
	c.render()
	sp, ok := spanFor(c, hintVimPick)
	if !ok {
		t.Fatal("vim state is not clickable")
	}
	click := func() {
		c.handleKey(t.Context(), press{Kind: pressClick, X: (sp.x0+sp.x1)/2 + 1, Y: c.hintRow + 1}, nil)
	}
	click()
	if !c.normal || c.menu != menuNone {
		t.Fatalf("clicking insert: normal=%v menu=%v", c.normal, c.menu)
	}
	// The rule is redrawn after the toggle, so re-read the span before clicking.
	c.render()
	if sp, ok = spanFor(c, hintVimPick); !ok {
		t.Fatal("vim state vanished after toggling")
	}
	click()
	if c.normal {
		t.Fatal("clicking normal did not go back to insert")
	}
}

// TestQuestionMarkOpensHelpInNormalMode pins `?` as the normal-mode help key,
// both as a plain rune and as the alt+? that a fast esc-then-? decodes to.
func TestQuestionMarkOpensHelpInNormalMode(t *testing.T) {
	for _, p := range []press{
		{Kind: pressRune, R: '?'},
		{Kind: pressAlt, R: '?'}, // esc then ? inside the 40 ms window
	} {
		c := &chat{in: newInput(), vp: layout.Viewport{Follow: true}, normal: true}
		c.handleKey(t.Context(), p, nil)
		if !c.help {
			t.Fatalf("%+v did not open help in normal mode", p)
		}
	}
}

// TestInputModePicker pins the delivery-mode popup: its rule segment, ctrl+x, the
// four rows and applying a pick.
func TestInputModePicker(t *testing.T) {
	c, buf := newTestChat(t)
	c.render()
	sp, ok := spanFor(c, hintInputModePick)
	if !ok {
		t.Fatal("input mode is not clickable")
	}
	c.handleKey(t.Context(), press{Kind: pressClick, X: (sp.x0+sp.x1)/2 + 1, Y: c.hintRow + 1}, nil)
	if c.menu != menuMode {
		t.Fatalf("click on input mode opened %v, want menuMode", c.menu)
	}

	buf.Reset()
	c.scr = screen.NewScreen(buf, 0)
	c.render()
	frame := stripANSI(buf.String())
	for _, want := range []string{"┌ input mode", "default", "append", "interrupt", "schedule"} {
		if !strings.Contains(frame, want) {
			t.Errorf("input-mode picker missing %q:\n%s", want, frame)
		}
	}

	c.handleKey(t.Context(), press{Kind: pressDown}, nil)
	c.handleKey(t.Context(), press{Kind: pressDown}, nil) // default -> append -> interrupt
	c.handleKey(t.Context(), press{Kind: pressEnter}, nil)
	if c.menu != menuNone {
		t.Fatal("enter did not close the input-mode picker")
	}
	if c.in.mode != "interrupt" {
		t.Fatalf("mode = %q, want interrupt", c.in.mode)
	}
	if h := stripANSI(c.hint()); !strings.Contains(h, "mode: interrupt") {
		t.Fatalf("the rule did not update: %q", h)
	}

	c.handleKey(t.Context(), press{Kind: pressMode}, nil)
	if c.menu != menuMode {
		t.Fatal("ctrl+x did not open the input-mode picker")
	}
	c.handleKey(t.Context(), press{Kind: pressMode}, nil)
	if c.menu != menuNone {
		t.Fatal("a second ctrl+x did not close the picker")
	}
}

// TestClickOutsideClosesPopups pins the shared closing rule: a click outside the
// card closes it, a click inside does not, and esc closes it.
func TestClickOutsideClosesPopups(t *testing.T) {
	c, _ := newTestChat(t)
	c.openMenu(menuMode)
	c.render()
	r := c.cardRect
	if r.X1 == 0 || r.Y1 == 0 {
		t.Fatalf("card rect not recorded: %+v", r)
	}
	// Inside: stays open.
	c.handleKey(t.Context(), press{Kind: pressClick, X: r.X0 + 1, Y: r.Y0 + 1}, nil)
	if c.menu != menuMode {
		t.Fatal("a click inside the card closed it")
	}
	// Outside: closes.
	c.handleKey(t.Context(), press{Kind: pressClick, X: 1, Y: c.height}, nil)
	if c.menu != menuNone {
		t.Fatal("a click outside the card did not close it")
	}

	// esc closes a freshly opened menu.
	c.openMenu(menuMode)
	c.handleKey(t.Context(), press{Kind: pressEsc}, nil)
	if c.menu != menuNone {
		t.Fatal("esc did not close the menu")
	}

	// Help: inside stays, outside closes.
	c.help = true
	c.render()
	hr := c.cardRect
	c.handleKey(t.Context(), press{Kind: pressClick, X: hr.X0 + 1, Y: hr.Y0 + 1}, nil)
	if !c.help {
		t.Fatal("a click inside help closed it")
	}
	c.handleKey(t.Context(), press{Kind: pressClick, X: 1, Y: 1}, nil)
	if c.help {
		t.Fatal("a click outside help did not close it")
	}
}

// TestPickerScrollsWithALongList pins the picker's window: with more rows than
// fit, the card shows a slice around the highlight and a position footer.
func TestPickerScrollsWithALongList(t *testing.T) {
	c := &chat{in: newInput(), vp: layout.Viewport{Follow: true}, seen: map[string]bool{}}
	c.width, c.height = 80, 12
	for i := 0; i < 30; i++ {
		c.targets = append(c.targets, menuItem{label: fmt.Sprintf("w%02d", i), value: fmt.Sprintf("w%02d", i), note: "reason"})
	}
	c.menu, c.menuCur = menuTarget, 25
	card, _ := c.menuCard()
	plain := stripANSI(strings.Join(card, "\n"))
	if !strings.Contains(plain, "26/30") {
		t.Fatalf("picker footer missing the position: %q", plain)
	}
	if !strings.Contains(plain, "w25") {
		t.Fatalf("the highlighted row is not in the window: %q", plain)
	}
	if strings.Contains(plain, "w00") {
		t.Fatalf("the window did not scroll away from the top: %q", plain)
	}
	if len(card) > c.height {
		t.Fatalf("card is %d rows, taller than the %d-row frame", len(card), c.height)
	}
}

// TestCardKeepsTheSurroundingFrame pins the overlay: the card covers only its own
// columns and rows, so transcript text around it stays on screen.
func TestCardKeepsTheSurroundingFrame(t *testing.T) {
	var buf bytes.Buffer
	scr := screen.NewScreen(&buf, 0)
	scr.W, scr.H = 80, 40
	c := &chat{
		scr: scr, width: 80, height: 40, in: newInput(),
		vp: layout.Viewport{Follow: true}, seen: map[string]bool{}, help: true,
	}
	c.lines = []string{"LEFT-EDGE-MARKER goes here and keeps going to the right"}
	c.render()
	frame := stripANSI(buf.String())
	if !strings.Contains(frame, "LEFT-EDGE-MARKER") {
		t.Fatalf("the overlay hid content beside the card:\n%s", frame)
	}
	if !strings.Contains(frame, "┌ help") {
		t.Fatalf("the card was not drawn:\n%s", frame)
	}
}

// TestSliceStyledKeepsTheColourAtTheCut pins the ANSI-aware slice: a background
// set before the cut still covers the sliced cells.
func TestSliceStyledKeepsTheColourAtTheCut(t *testing.T) {
	oldAnsi, oldTC := term.Ansi, term.Truecolor
	defer func() { term.Ansi, term.Truecolor = oldAnsi, oldTC }()
	term.Ansi, term.Truecolor = true, true

	line := bandLine("abcdef", 6, 0, bandTool.Bg())
	got := sliceStyled(line, 2, 4)
	if !strings.Contains(got, "48;2;37;65;49") {
		t.Fatalf("the sliced span lost its background: %q", got)
	}
	if plain := stripANSI(got); plain != "cd" {
		t.Fatalf("sliceStyled text = %q, want cd", plain)
	}
}

// TestHelpFitsTheSmallestFrame pins that a default 80x24 frame still shows the
// whole keymap: a truncated page would silently hide the leave key.
func TestHelpFitsTheSmallestFrame(t *testing.T) {
	plain := stripANSI(strings.Join(helpBox(80, 24), "\n"))
	for _, want := range []string{"any mode", "ctrl+t / ctrl+v", "ctrl+x", "leave", "wheel / drag", "esc or click outside"} {
		if !strings.Contains(plain, want) {
			t.Errorf("80x24 help is missing %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "…") {
		t.Error("80x24 help is truncated")
	}
}

// TestModeWordCarriesTheBrandColour pins the theme colour on the vim-state word.
func TestModeWordCarriesTheBrandColour(t *testing.T) {
	oldAnsi, oldTC := term.Ansi, term.Truecolor
	defer func() { term.Ansi, term.Truecolor = oldAnsi, oldTC }()
	term.Ansi, term.Truecolor = true, true

	insert := &chat{width: 120, target: "niq", vp: layout.Viewport{Follow: true}, in: newInput()}
	if h := insert.hint(); !strings.Contains(h, "minsert\x1b[0m") {
		t.Fatalf("insert state is not brand-coloured: %q", h)
	}
	normal := &chat{width: 120, target: "niq", vp: layout.Viewport{Follow: true}, normal: true, in: newInput()}
	if h := normal.hint(); !strings.Contains(h, "mnormal\x1b[0m") {
		t.Fatalf("normal state is not brand-coloured: %q", h)
	}
}
