// Shared form primitives: the input / label / field / hint shapes every dialog
// and detail panel uses, plus the two outline button variants.
//
// They used to be declared inline in each caller — nine byte-identical input
// blocks across six files, with the label margin drifting between 3 and 4 and
// the buttons between radius 2, 3 and 4. One factory keeps them in step; a
// component that genuinely needs a different shape (the picker's search box,
// the approvals row input) still declares its own.
import type { CSSProperties } from 'react'
import { fontSizes, type Palette } from '../theme'

export interface FormStyles {
  // Standard field surface: the page/dialog background.
  input: CSSProperties
  // Field on a raised card (the provider cards): the card's own background.
  inputOnCard: CSSProperties
  // Single-line field that shares a row with a button (mount path, event args).
  inlineInput: CSSProperties
  // Monospace multi-line editor (params / JSON-ish input).
  textarea: CSSProperties
  label: CSSProperties
  field: CSSProperties
  hint: CSSProperties
}

export function formStyles(colors: Palette): FormStyles {
  const base: CSSProperties = {
    width: '100%',
    boxSizing: 'border-box',
    background: colors.bg,
    border: '1px solid ' + colors.border,
    borderRadius: 4,
    padding: '5px 8px',
    color: colors.text,
    fontSize: fontSizes.sm,
    outline: 'none',
  }
  return {
    input: base,
    inputOnCard: { ...base, background: colors.bgLight },
    inlineInput: { ...base, width: undefined, flex: 1, minWidth: 0, background: colors.bgLight },
    textarea: {
      ...base,
      padding: '6px 8px',
      fontSize: fontSizes.xs,
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
      lineHeight: 1.5,
      resize: 'vertical',
    },
    label: { display: 'block', fontSize: fontSizes.xs, color: colors.textDimmed, marginBottom: 4 },
    field: { marginBottom: 10 },
    hint: { fontSize: fontSizes.xs, color: colors.textDimmed, marginTop: 4, lineHeight: 1.5 },
  }
}

// The two outline buttons every dialog footer ends with: the accent "commit"
// action and the neutral "cancel/close" one.
export function buttonStyles(colors: Palette): { accent: CSSProperties; ghost: CSSProperties } {
  return {
    accent: {
      cursor: 'pointer',
      border: '1px solid ' + colors.accent,
      borderRadius: 4,
      padding: '4px 14px',
      color: colors.accent,
      fontSize: fontSizes.sm,
      userSelect: 'none',
    },
    ghost: {
      cursor: 'pointer',
      border: '1px solid ' + colors.border,
      borderRadius: 2,
      padding: '4px 14px',
      color: colors.textDim,
      fontSize: fontSizes.sm,
      userSelect: 'none',
    },
  }
}
