// The chat view's shorthand for the ../keys decoder: type and constant aliases
// plus thin readers, so keymap.go and the tests keep naming presses as
// `press` / `pressXxx` and decoding as `decodePress`. The decoder itself lives in
// ../keys (and is reusable by any line-mode terminal app).
package tui

import (
	"bufio"
	"context"
	"time"

	"github.com/niq-run/niq/app/client/tui/keys"
)

type press = keys.Press
type pressKind = keys.PressKind

const (
	wheelStep    = keys.WheelStep
	maxInputRows = keys.MaxInputRows

	pressOther     = keys.PressOther
	pressRune      = keys.PressRune
	pressEnter     = keys.PressEnter
	pressNewline   = keys.PressNewline
	pressPaste     = keys.PressPaste
	pressMode      = keys.PressMode
	pressBackspace = keys.PressBackspace
	pressDelete    = keys.PressDelete
	pressLeft      = keys.PressLeft
	pressRight     = keys.PressRight
	pressUp        = keys.PressUp
	pressDown      = keys.PressDown
	pressHome      = keys.PressHome
	pressEnd       = keys.PressEnd
	pressPageUp    = keys.PressPageUp
	pressPageDown  = keys.PressPageDown
	pressLineUp    = keys.PressLineUp
	pressLineDown  = keys.PressLineDown
	pressKillEnd   = keys.PressKillEnd
	pressKillWord  = keys.PressKillWord
	pressYank      = keys.PressYank
	pressAlt       = keys.PressAlt
	pressWheelUp   = keys.PressWheelUp
	pressWheelDown = keys.PressWheelDown
	pressClick     = keys.PressClick
	pressDrag      = keys.PressDrag
	pressRelease   = keys.PressRelease
	pressTerminal  = keys.PressTerminal
	pressTarget    = keys.PressTarget
	pressView      = keys.PressView
	pressHelp      = keys.PressHelp
	pressEsc       = keys.PressEsc
	pressInterrupt = keys.PressInterrupt
	pressTab       = keys.PressTab
	pressBacktab   = keys.PressBacktab
	pressPrevUser  = keys.PressPrevUser
	pressNextUser  = keys.PressNextUser
)

func readKeys(ctx context.Context, out chan<- press) { keys.ReadKeys(ctx, out) }

func readKeysFrom(ctx context.Context, in *bufio.Reader, setDeadline func(time.Time) error, out chan<- press) {
	keys.ReadKeysFrom(ctx, in, setDeadline, out)
}

func decodePress(in *bufio.Reader, r rune) press { return keys.DecodePress(in, r) }

func decodeMouse(in *bufio.Reader) press { return keys.DecodeMouse(in) }

func controlPress(r rune) (press, bool) { return keys.ControlPress(r) }

func keyPress(code, mod int) press { return keys.KeyPress(code, mod) }
