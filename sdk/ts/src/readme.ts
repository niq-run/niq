/**
 * Helpers for `worker.readme` — the TS mirror of Go's `baseworker/readme.go`.
 *
 * A worker's readme is its own guide (what it is for, which events it serves).
 * A declaration can customize the built-in text without code changes:
 *
 *   readme         replaces the built-in guide
 *   readme_append  is appended after it
 *
 * Both render `{{key}}` placeholders from the declaration params (top-level
 * keys; list values join with ", "; an unknown key is left untouched).
 */

/**
 * Apply a declaration's readme customization over a worker's built-in guide.
 * Mirrors Go's `readmeText`.
 */
export function readmeText(
  builtin: () => string,
  params?: Record<string, unknown>,
): string {
  let text = builtin();
  const override = params?.["readme"];
  if (typeof override === "string" && override !== "") {
    text = override;
  }
  const appendText = params?.["readme_append"];
  if (typeof appendText === "string" && appendText !== "") {
    if (text !== "" && !text.endsWith("\n")) text += "\n";
    text += appendText;
  }
  return renderParams(text, params);
}

/**
 * Replace `{{key}}` with the params value. A placeholder with no matching key
 * stays untouched, so ordinary prose and JSON braces are safe. Mirrors Go's
 * `renderParams`.
 */
export function renderParams(
  text: string,
  params?: Record<string, unknown>,
): string {
  if (!params || Object.keys(params).length === 0 || !text.includes("{{")) {
    return text;
  }
  let out = "";
  let rest = text;
  for (;;) {
    const i = rest.indexOf("{{");
    if (i < 0) break;
    const after = rest.slice(i + 2);
    const j = after.indexOf("}}");
    if (j < 0) break;
    out += rest.slice(0, i);
    const key = after.slice(0, j).trim();
    if (Object.prototype.hasOwnProperty.call(params, key)) {
      out += paramText(params[key]);
    } else {
      out += rest.slice(i, i + j + 4);
    }
    rest = after.slice(j + 2);
  }
  out += rest;
  return out;
}

/** Render one params value: strings verbatim, lists comma-joined, else JSON. */
function paramText(v: unknown): string {
  if (v === null || v === undefined) return "";
  if (typeof v === "string") return v;
  if (Array.isArray(v)) return v.map(paramText).join(", ");
  try {
    return JSON.stringify(v) ?? "";
  } catch {
    return "";
  }
}
