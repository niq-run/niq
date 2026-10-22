import type { Event } from "./event.js";

/**
 * What a declared schema may be. Anything object-shaped is accepted here and
 * normalized at registration time, because the accepted sources are:
 *
 *  - a plain JSON Schema object (also how TypeBox schemas arrive — TypeBox *is*
 *    JSON Schema);
 *  - a schema exposing `toJSONSchema()` / `toJsonSchema()` — zod 4, arktype — so
 *    the library converts itself;
 *  - a schema converted by hand — valibot (`toJsonSchema` from
 *    `@valibot/to-json-schema`) and zod 3 (`zod-to-json-schema`).
 *
 * A library schema with no converter is REJECTED at registration (it carries
 * library-specific top-level keys, and announcing it would put library internals
 * on the wire). Typed as `object` on purpose: the concrete library types do not
 * structurally extend `Record<string, unknown>`, and only the runtime check can
 * tell a real schema from a lookalike.
 */
export type SchemaSource = object;

/**
 * Extension describes an event a worker responds to. It is how a worker is
 * extended: each registration teaches the base to recognize one more event
 * and answer it with a handler. The event type IS the extension's identity —
 * distinct things are distinct events. Mirrors `baseworker.Extension`.
 */
export interface Extension {
  /** The event type the extension responds to — its identity. */
  event: string;
  /**
   * Internal marks an extension that belongs to the worker's OWN view: it is
   * not advertised in the peer-facing `worker.ready` announcement (directory /
   * WebUI / PTC), and a request from any other worker is refused before the
   * handler runs (a `request.rejected` when the event carries a request id). The
   * declaring worker still invokes it normally, and the self-directed ready
   * keeps it in its own tool list.
   *
   * This is a scope rule, not a general access control: it says the capability
   * is meaningful only to its owner. Whether another worker may address this one
   * at all is still the bus ACL's job.
   */
  internal?: boolean;
  description?: string;
  /**
   * The request's JSON Schema — what a caller must pass as the event payload.
   * Build it with `s.object({...})` (see schema.ts) or hand it in.
   */
  parameters?: SchemaSource;
  /**
   * The REPLY's JSON Schema: the shape a caller gets back. Documentation only —
   * nothing validates it — but it is what lets an agent (in particular a PTC
   * script, written before anything runs) plan its parse. Declare it whenever the
   * reply is structured; a prose reply can leave it out.
   *
   * It describes the reply VALUE: an object reply is the payload itself, while a
   * text/number/array reply travels under `result` (see reply.ts).
   */
  response?: SchemaSource;
}

/**
 * Handler executed when the registered event arrives on the bus. A pure
 * closure — it captures whatever state it needs, so the signature carries no
 * base-type dependency. Mirrors `baseworker.ExtensionHandler`.
 */
export type ExtensionHandler = (evt: Event) => void | Promise<void>;

/** Parsed, common fields of a tool-invocation event. Mirrors `baseworker.ToolCall`. */
export interface ToolCall {
  /** The request's `request_id` — echo it back on the reply. */
  callID: string;
  name: string;
  /** The worker that sent the invocation (bus-stamped `worker_id`). */
  callerID: string;
  /** Always a non-empty object so handlers can read/write safely. */
  args: Record<string, unknown>;
  traceID: string;
}
