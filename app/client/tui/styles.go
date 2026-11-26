// The chat view's styling surface: thin forwarders onto ./term.
//
// The actual ANSI codes, palette and cell maths live in ../term so they can be
// reused by the renderer/widget packages. The chat view keeps reading as `dim(…)`,
// `brand(…)`, `displayWidth(…)` through these forwarders, which keeps the diff of
// the extraction to this one file. (The `niq i` picker's own wording lives in
// ../picker, not here.)
package tui

import "github.com/niq-run/niq/app/client/tui/term"

// ── Styling forwarders → term ──

func bold(s string) string         { return term.Bold(s) }
func dim(s string) string          { return term.Dim(s) }
func green(s string) string        { return term.Green(s) }
func red(s string) string          { return term.Red(s) }
func underline(s string) string    { return term.Underline(s) }
func italic(s string) string       { return term.Italic(s) }
func codeText(s string) string     { return term.CodeText(s) }
func reverse(s string) string      { return term.Reverse(s) }
func gray(s string) string         { return term.Gray(s) }
func grayBold(s string) string     { return term.GrayBold(s) }
func warnText(s string) string     { return term.WarnText(s) }
func codeKeyword(s string) string  { return term.CodeKeyword(s) }
func codeString(s string) string   { return term.CodeString(s) }
func codeNumber(s string) string   { return term.CodeNumber(s) }
func codeComment(s string) string  { return term.CodeComment(s) }
func greenBold(s string) string    { return term.GreenBold(s) }
func magentaBold(s string) string  { return term.MagentaBold(s) }
func cyanBold(s string) string     { return term.CyanBold(s) }
func yellowBright(s string) string { return term.YellowBright(s) }
func redBold(s string) string      { return term.RedBold(s) }
func brand(s string) string        { return term.Brand(s) }
func brandBold(s string) string    { return term.BrandBold(s) }
func brandBg() string              { return term.BrandBg() }

func displayWidth(s string) int              { return term.DisplayWidth(s) }
func visibleWidth(s string) int              { return term.VisibleWidth(s) }
func runeCells(r rune) int                   { return term.RuneCells(r) }
func isWide(r rune) bool                     { return term.IsWide(r) }
func truncateCells(s string, max int) string { return term.TruncateCells(s, max) }

func clipStyled(s string, width int) string     { return term.Clip(s, width) }
func sliceStyled(s string, from, to int) string { return term.Slice(s, from, to) }

// ── Bands (an alias so the view keeps its own band vocabulary) ──

type bandKind = term.BandKind

const (
	bandNone  = term.BandNone
	bandUser  = term.BandUser
	bandTool  = term.BandTool
	bandCode  = term.BandCode
	bandError = term.BandError
	bandWarn  = term.BandWarn
)

func blockSurface(band bandKind) string                    { return term.BlockSurface(band) }
func bandLine(s string, width, padX int, bg string) string { return term.BandLine(s, width, padX, bg) }
func toolCall(s string) string                             { return term.ToolCall(s) }
func toolOut(s string) string                              { return term.ToolOut(s) }
func toolReply(s string) string                            { return term.ToolReply(s) }
func toolFail(s string) string                             { return term.ToolFail(s) }

func terminalReport(text string) bool { return term.TerminalReport(text) }
