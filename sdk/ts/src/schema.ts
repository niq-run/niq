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

/** Keys that make an object recognizably a JSON Schema. */
const SCHEMA_HINTS = [
  "type",
  "properties",
  "items",
  "anyOf",
  "oneOf",
  "allOf",
  "$ref",
  "enum",
  "const",
  "additionalProperties",
];

function looksLikeJsonSchema(v: unknown): v is JsonSchema {
  if (typeof v !== "object" || v === null || Array.isArray(v)) return false;
  const keys = Object.keys(v);
  // An empty object is the JSON Schema for "anything" (s.any()).
  return keys.length === 0 || keys.some((k) => SCHEMA_HINTS.includes(k));
}

/**
 * Normalizes a declared schema to plain JSON Schema.
 *
 * A JSON Schema object passes through untouched, and so does anything that can
 * produce one — zod v4 (`z.object({…})` has `.toJSONSchema()`), arktype
 * (`.toJsonSchema()`), or a wrapper you wrote. That is why the SDK depends on no
 * schema library: bring whichever you already use, and the wire form stays a
 * plain JSON Schema either way.
 *
 * Throws when the value is neither, naming what was expected — silently
 * announcing a non-schema would poison every peer's tool list.
 */
export function toJsonSchema(value: unknown, what: string): JsonSchema {
  // Converters first: a library schema can look like a JSON Schema at the top
  // level (a zod object carries `type: "object"`) while its real shape lives in
  // library-specific keys, so passing it through would announce garbage.
  const holder = value as { toJSONSchema?: unknown; toJsonSchema?: unknown } | null;
  for (const fn of [holder?.toJSONSchema, holder?.toJsonSchema]) {
    if (typeof fn === "function") {
      const out = fn.call(value);
      if (looksLikeJsonSchema(out)) return out;
    }
  }
  if (looksLikeJsonSchema(value)) return value;
  throw new TypeError(
    `${what}: expected a JSON Schema object or a schema with toJSONSchema() (zod 4, arktype); got ${describe(value)}`,
  );
}

function describe(v: unknown): string {
  if (v === null) return "null";
  if (Array.isArray(v)) return "an array";
  return typeof v;
}
