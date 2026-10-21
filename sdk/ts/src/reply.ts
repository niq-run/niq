/**
 * The reply conventions of a `request.completed` payload — the TS mirror of Go's
 * `event.ReplyValue` / `event.ReplyText`.
 *
 * The bus only requires a payload to be a JSON object. What a caller is after is
 * either the payload itself (an OBJECT reply) or, when the reply is not an object
 * (text, a number, an array), its `result` entry. `content` is the second
 * reserved key: multi-modal blocks a reason worker decodes into llm content
 * blocks. A structured reply must therefore not use `result` or `content` as a
 * top-level field name.
 */

/** True for a plain object (not null, not an array). */
function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/**
 * The value carried by a `request.completed` payload: the payload itself for an
 * object reply, otherwise its `result` value.
 */
export function replyValue(payload: Record<string, unknown>): unknown {
  return "result" in payload ? payload.result : payload;
}

/**
 * Render that value as text: a string verbatim, anything else JSON. This is what
 * goes into a transcript or a log line. An empty payload means "nothing to say"
 * rather than the JSON text `{}`.
 */
export function replyText(payload: Record<string, unknown>): string {
  const value = replyValue(payload);
  if (typeof value === "string") return value;
  if (value === null || value === undefined) return "";
  if (isRecord(value) && Object.keys(value).length === 0) return "";
  return JSON.stringify(value);
}
