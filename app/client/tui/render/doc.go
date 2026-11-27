// Package render is the reusable content-rendering layer that stands on its own,
// independent of the chat session:
//
//   - the markdown viewer (headings, emphasis, lists, quotes, fenced code,
//     GFM pipe tables, …) that turns a formatted answer into styled, width-wrapped
//     lines;
//   - the fenced-code syntax highlighter the viewer hands code to.
//
// It depends only on ../term for styling and width math, so it can render any text
// for any screen: the output is plain, already-styled lines at a given cell width,
// and the caller owns placement and scrolling (see ../layout for the viewport).
package render
