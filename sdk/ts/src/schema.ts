/**
 * Schema builders for a capability declaration — the TS mirror of Go's
 * `baseworker.ObjectSchema` / `ArraySchema` / `StringSchema`.
 *
 * `parameters` describes the request, `response` describes the reply. Both are
 * plain JSON Schema objects on the wire (`worker.ready` -> `watch[]`), so hand
 * writing them is possible but noisy; these helpers build the same thing in a
 * line or two:
 *
 *   worker.register({
 *     event: "lark.send",
 *     description: "Send a message",
 *     parameters: s.object({
 *       target: s.string("chat_id, open_id or user_id"),
 *       text: s.string("message body"),
 *       file: s.optional(s.string("local path or http(s) URL")),
 *       size: s.optional(s.integer("max characters")),
 *     }),
 *     response: s.object({ messageId: s.string(), ok: s.boolean() }),
 *   }, handler);
 *
 * They are documentation, not validation: nothing on the bus checks a payload
 * against them. Validate in the handler if you need to (zod/valibot work fine —
 * `z.toJSONSchema(schema)` also produces a `parameters` value directly).
 */

export type JsonSchema = Record<string, unknown>;

/** Schemas marked optional; a WeakSet keeps the marker out of the JSON. */
const optionalSchemas = new WeakSet<object>();

function withDesc(schema: JsonSchema, desc?: string): JsonSchema {
  if (desc) schema.description = desc;
  return schema;
}

function objectSchema(
  props: Record<string, JsonSchema>,
  desc?: string,
): JsonSchema {
  const properties: Record<string, JsonSchema> = {};
  const required: string[] = [];
  for (const [name, prop] of Object.entries(props)) {
    properties[name] = prop;
    if (!optionalSchemas.has(prop)) required.push(name);
  }
  const out: JsonSchema = { type: "object", properties };
  if (required.length > 0) out.required = required;
  return withDesc(out, desc);
}

function arraySchema(items: JsonSchema, desc?: string): JsonSchema {
  return withDesc({ type: "array", items }, desc);
}

function scalar(
  type: "string" | "number" | "integer" | "boolean",
  desc?: string,
): JsonSchema {
  return withDesc({ type }, desc);
}

/**
 * Marks a property optional. It also reads as a no-op — the schema itself is
 * unchanged, so an optional property stays a perfectly normal schema.
 */
function optional<T extends JsonSchema>(schema: T): T {
  optionalSchemas.add(schema);
  return schema;
}

export const s = {
  object: objectSchema,
  array: arraySchema,
  string: (desc?: string) => scalar("string", desc),
  number: (desc?: string) => scalar("number", desc),
  integer: (desc?: string) => scalar("integer", desc),
  boolean: (desc?: string) => scalar("boolean", desc),
  /** A string restricted to a fixed set of values. */
  enumOf: (values: readonly string[], desc?: string): JsonSchema =>
    withDesc({ type: "string", enum: [...values] }, desc),
  /** Anything (an object with no declared properties, a free-form value). */
  any: (desc?: string): JsonSchema => withDesc({}, desc),
  optional,
};

/** True when `schema` was marked optional by `s.optional`. */
export function isOptionalSchema(schema: JsonSchema): boolean {
  return optionalSchemas.has(schema);
}
