import type {
  Event,
  EventPattern,
  Extension,
  ExtensionHandler,
  ToolCall,
  WorkerSideChannel,
} from "./types/index.js";
import { EventType } from "./types/index.js";
import { createEvent } from "./createEvent.js";
import { eventHelp, helpIndex, visibleEventNames } from "./help.js";
import type { HelpFunc } from "./help.js";
import { readmeText } from "./readme.js";
import { s, toJsonSchema } from "./schema.js";

/**
 * BaseWorker — the shared base implementation every worker builds on,
 * mirroring Go `pkg/baseworker.BaseWorker`. It holds identity, subscription
 * declarations and the worker-side bus channel; the extension registry (the
 * uniform way to declare what the worker responds to and how); tool-request
 * reply plumbing; and the `worker.ready` presence announcement.
 *
 * Registering an extension is safe at any time — before connecting or at
 * runtime from within a handler. Registering the same (event, keyField, key)
 * replaces the previous registration.
 */
export class BaseWorker {
  readonly id: string;
  readonly subscriptions: EventPattern[];
  readonly channel: WorkerSideChannel;

  private readonly regs = new Map<
    string,
    { ext: Extension; handler: ExtensionHandler }
  >();

  /** The contract last announced, so a `worker.discover` can be answered. */
  private announced: {
    workerType: string;
    publishes?: Record<string, unknown>[];
  } | null = null;

  constructor(opts: {
    id: string;
    subscriptions?: EventPattern[];
    channel: WorkerSideChannel;
  }) {
    this.id = opts.id;
    this.subscriptions = opts.subscriptions ?? [];
    this.channel = opts.channel;
  }

  /**
   * Bind an extension to a handler. Safe at any time, including at runtime.
   *
   * Declared schemas are normalized here: `parameters` / `response` may be plain
   * JSON Schema, or anything that converts to one (zod 4, arktype, …), so the
   * announced contract — and therefore every peer's tool list — is always plain
   * JSON Schema.
   */
  register(ext: Extension, handler: ExtensionHandler): void {
    const normalized: Extension = { ...ext };
    if (ext.parameters !== undefined) {
      normalized.parameters = toJsonSchema(ext.parameters, `parameters of ${ext.event}`);
    }
    if (ext.response !== undefined) {
      normalized.response = toJsonSchema(ext.response, `response of ${ext.event}`);
    }
    this.regs.set(extensionKey(ext), { ext: normalized, handler });
  }

  /**
   * Register the generic `worker.readme` capability: a directed worker.readme
   * call is answered with this worker's own guide.
   *
   * `text` runs per request so the guide can include live state. `params` is the
   * declaration's construction params, read for two optional keys that let a
   * declaration customize the guide without touching the worker's code:
   *
   *   readme         replaces the built-in guide
   *   readme_append  is appended after it
   *
   * Both render `{{key}}` placeholders from params (see readme.ts). Mirrors Go's
   * `BaseWorker.RegisterReadme`.
   */
  registerReadme(
    description: string,
    text: () => string,
    params?: Record<string, unknown>,
  ): void {
    this.register(
      {
        event: EventType.WorkerReadme,
        response: s.string("the worker's self-description (its readme text)"),
        description,
        parameters: s.object({}),
      },
      (evt) => {
        const tc = parseToolCall(evt);
        if (!tc.callID) return;
        void this.replyCompleted(
          tc.callerID,
          tc.callID,
          readmeText(text, params),
          tc.traceID,
        );
      },
    );
  }

  /**
   * Register the generic `worker.help` capability: a directed worker.help call
   * is answered with help for ONE of this worker's events.
   *
   * The request shape is:
   *
   *   { event_name: "spawn", params: { worker: "reason" } }
   *
   * `event_name` names an event this worker serves; `params` is an optional
   * selector a `HelpFunc` may use. Omitting `event_name` returns an index of
   * every served event.
   *
   * `guides` is keyed by event and may be omitted — without one, every event
   * still gets a generic guide rendered from its declared description,
   * parameters and response. A guide wins only when it returns a string; a
   * selector it does not recognize falls back to the generic guide. The map is
   * read-only after registration; call `registerHelp` again to replace it.
   *
   * Mirrors Go's `BaseWorker.RegisterHelp`.
   */
  registerHelp(guides?: Record<string, HelpFunc>): void {
    this.register(
      {
        event: EventType.WorkerHelp,
        response: s.string("the requested help text (markdown)"),
        description:
          "Read help for one of this worker's events, like a CLI's -h. Pass " +
          "event_name to explain that event; params may select a more specific " +
          'guide (e.g. { worker: "reason" } for spawn). Omit event_name to list ' +
          "every event this worker serves.",
        parameters: s.object({
          event_name: s.optional(
            s.string("Event to explain (e.g. spawn). Empty lists all served events."),
          ),
          params: s.optional(
            s.object(
              {},
              'Optional selector for an event-specific guide, e.g. { worker: "reason" }.',
            ),
          ),
        }),
      },
      (evt) => {
        const tc = parseToolCall(evt);
        if (!tc.callID) return;
        try {
          const text = this.helpText(evt, tc.args, guides);
          void this.replyCompleted(tc.callerID, tc.callID, text, tc.traceID);
        } catch (err) {
          void this.replyFailed(
            tc.callerID,
            tc.callID,
            errorMessage(err),
            tc.traceID,
          );
        }
      },
    );
  }

  /**
   * Select the guide for one worker.help request. Internal extensions are only
   * visible to their owner, mirroring dispatchExtension's scope rule. Throws for
   * an event the worker does not serve (answered as request.failed).
   */
  private helpText(
    evt: Event,
    args: Record<string, unknown>,
    guides?: Record<string, HelpFunc>,
  ): string {
    const includeInternal = evt.worker_id === this.id;
    const exts = this.extensions();
    const name = argString(args, "event_name").trim();
    const rawParams = args["params"];
    const params: Record<string, unknown> = isRecord(rawParams) ? rawParams : {};

    if (name === "") return helpIndex(this.id, exts, includeInternal);

    for (const ext of exts) {
      if (ext.event !== name) continue;
      if (ext.internal && !includeInternal) {
        throw new Error(
          `worker.help: ${this.id} does not expose event ${JSON.stringify(name)}`,
        );
      }
      const guide = guides?.[ext.event];
      if (typeof guide === "function") {
        const text = guide(params);
        if (text !== null && text !== undefined) return text;
      }
      return eventHelp(ext, Object.keys(params).length > 0);
    }

    throw new Error(
      `worker.help: ${this.id} does not serve event ${JSON.stringify(name)}; ` +
        `served events: ${visibleEventNames(exts, includeInternal).join(", ")}`,
    );
  }

  /**
   * Route an event to the registered extension matching its event type.
   * Returns whether a handler ran; the first matching registration wins. Async
   * handlers are not awaited.
   *
   * An `internal` extension is self-scoped: a request from any other worker is
   * refused before the handler runs (a `request.rejected` when the event carries
   * a request id). The bus stamps the sender's worker id, so a peer cannot pose
   * as the owner.
   *
   * A request that matches no extension is answered with `request.failed` rather
   * than dropped, so the caller's pending call cannot wait forever on a worker
   * that does not serve that event.
   *
   * `worker.discover` is protocol traffic rather than an extension: it is
   * answered here with the contract last passed to {@link announceReady} (a
   * directed, transient `worker.ready` to the asker), mirroring Go's
   * AnnounceReadyTo. Presence is live-only and never persisted, so a peer that
   * starts later — or a UI that cannot read presence from history — learns the
   * worker's tools by asking.
   */
  dispatchExtension(evt: Event): boolean {
    if (evt.type === EventType.WorkerDiscover) {
      const asker = evt.worker_id;
      if (asker && asker !== this.id) void this.announceReadyTo(asker);
      return true;
    }
    for (const { ext, handler } of this.regs.values()) {
      if (ext.event !== evt.type) continue;
      if (ext.internal && evt.worker_id !== this.id) {
        if (evt.request_id) {
          void this.replyRejected(
            evt.worker_id,
            evt.request_id,
            `internal capability: only ${this.id} may invoke it`,
            evt.trace_id,
          );
        }
        return true;
      }
      void handler(evt);
      return true;
    }
    // No registration matched: answer a request so the caller cannot hang on a
    // silent drop. Notifications have nothing waiting.
    if (evt.request_id) {
      void this.replyFailed(
        evt.worker_id,
        evt.request_id,
        `no handler for event ${evt.type}`,
        evt.trace_id,
      );
    }
    return false;
  }

  /** Snapshot of the registered extensions (insertion order). */
  extensions(): Extension[] {
    return [...this.regs.values()].map((r) => r.ext);
  }

  /**
   * Render the registry into the `worker.ready` "watch" wire format.
   * `includeInternal` controls whether Internal extensions are rendered: a
   * peer-facing broadcast leaves them out (they are not advertised to peers;
   * visibility only — the bus ACL, not this parameter, gates dispatch).
   */
  watchEntries(includeInternal: boolean): Record<string, unknown>[] {
    const out: Record<string, unknown>[] = [];
    for (const ext of this.extensions()) {
      if (ext.internal && !includeInternal) continue;
      const entry: Record<string, unknown> = {
        event: ext.event,
        desc: ext.description,
      };
      if (ext.parameters) {
        entry["parameters"] = ext.parameters;
      }
      if (ext.response) {
        entry["response"] = ext.response;
      }
      out.push(entry);
    }
    return out;
  }

  /**
   * Broadcast this worker's presence on the bus: the externally callable
   * contract (Internal left out), self excluded via `exclude_worker_id`.
   * `publishes` is the wire form of the events this worker declares it emits
   * (`worker.ready` "publishes").
   *
   * The contract is remembered, so {@link dispatchExtension} can answer a
   * `worker.discover` with it. The event is transient: presence is delivered
   * live and never written to the durable event log (mirroring Go).
   */
  async announceReady(
    workerType: string,
    publishes?: Record<string, unknown>[],
  ): Promise<void> {
    this.announced = { workerType, publishes };
    const presence = this.presenceEvent(this.announced);
    presence.exclude_worker_id = this.id;
    try {
      await this.channel.broadcast(presence);
    } catch {
      // Presence announcements are best-effort, mirroring Go.
    }
  }

  /**
   * Send this worker's presence directly to one target instead of broadcasting
   * it — the answer to a `worker.discover`, and the direct equivalent of Go's
   * AnnounceReadyTo. Omitted arguments reuse the contract last announced with
   * {@link announceReady}; with no contract announced yet, nothing is sent.
   */
  async announceReadyTo(
    target: string,
    workerType?: string,
    publishes?: Record<string, unknown>[],
  ): Promise<void> {
    const announced = workerType !== undefined
      ? { workerType, publishes }
      : this.announced;
    if (!announced) return;
    try {
      await this.channel.send(this.presenceEvent(announced), target);
    } catch {
      // Presence announcements are best-effort, mirroring Go.
    }
  }

  /** Render one `worker.ready` event from a declared contract. */
  private presenceEvent(announced: {
    workerType: string;
    publishes?: Record<string, unknown>[];
  }): Event {
    const payload: Record<string, unknown> = {
      type: announced.workerType,
      watch: this.watchEntries(false),
    };
    if (announced.publishes && announced.publishes.length > 0) {
      payload["publishes"] = announced.publishes;
    }
    const presence = createEvent(EventType.WorkerReady, payload);
    presence.transient = true; // presence: live-only, not durable history
    return presence;
  }

  // ── Tool-serving helpers ──
  //
  // The repetitive parts every tool-serving worker repeats: replying to a
  // tool-invocation with a request.completed / request.failed /
  // request.rejected result, echoing the request's id and propagating the
  // trace ID. Reply errors are ignored, mirroring Go.

  /**
   * Answer a request caller with a request.completed result.
   *
   * A convenience, not the protocol: the bus only requires the payload to be a
   * JSON object, and a worker may build one by hand. What this does:
   *
   *  - an OBJECT reply is the payload — no wrapper, so the event log shows the
   *    reply itself;
   *  - anything else (a string, a number, an array) cannot be a payload on its
   *    own, so it is wrapped as `{ result: value }`.
   *
   * `result` and `content` are therefore reserved top-level keys of a reply
   * payload (the latter for multi-modal blocks). replyObject / replyString say
   * which shape you mean.
   */
  async replyCompleted(
    callerID: string,
    callID: string,
    result: unknown,
    traceID?: string,
  ): Promise<void> {
    const payload = isRecord(result) ? result : { result };
    await this.reply(EventType.RequestCompleted, callerID, callID, payload, traceID);
  }

  /** Answer with a structured payload: the object IS the payload. */
  async replyObject(
    callerID: string,
    callID: string,
    payload: Record<string, unknown>,
    traceID?: string,
  ): Promise<void> {
    await this.replyCompleted(callerID, callID, payload, traceID);
  }

  /** Answer with text: the payload is `{ result: text }`. */
  async replyString(
    callerID: string,
    callID: string,
    text: string,
    traceID?: string,
  ): Promise<void> {
    await this.replyCompleted(callerID, callID, text, traceID);
  }

  /** Answer a request caller with a request.failed error message. */
  async replyFailed(
    callerID: string,
    callID: string,
    error: string,
    traceID?: string,
  ): Promise<void> {
    await this.reply(
      EventType.RequestFailed,
      callerID,
      callID,
      { error },
      traceID,
    );
  }

  /**
   * Answer a request caller with a request.rejected, carrying the reason.
   * Used when the worker declines a call based on its own rules (e.g. a
   * safety guard) or after a human-in-the-loop approval is denied.
   */
  async replyRejected(
    callerID: string,
    callID: string,
    reason: string,
    traceID?: string,
  ): Promise<void> {
    await this.reply(
      EventType.RequestRejected,
      callerID,
      callID,
      { reason },
      traceID,
    );
  }

  /** Reply to a tool-invocation whose name no handler matched. */
  async replyUnknownTool(tc: ToolCall): Promise<void> {
    await this.replyFailed(
      tc.callerID,
      tc.callID,
      "unknown tool: " + tc.name,
      tc.traceID,
    );
  }

  private async reply(
    type: string,
    callerID: string,
    callID: string,
    payload: Record<string, unknown>,
    traceID?: string,
  ): Promise<void> {
    const evt = createEvent(type, payload);
    evt.request_id = callID;
    evt.trace_id = traceID;
    try {
      await this.channel.send(evt, callerID);
    } catch {
      // Reply failures are ignored, mirroring Go `_ = w.Channel.Send(...)`.
    }
  }
}

/** Extract the common fields from a tool-invocation event. Mirrors `baseworker.ParseToolCall`. */
export function parseToolCall(evt: Event): ToolCall {
  return {
    callID: evt.request_id ?? "",
    name: evt.type,
    callerID: evt.worker_id,
    args: isRecord(evt.payload) ? evt.payload : {},
    traceID: evt.trace_id ?? "",
  };
}

/** Return the string value of a tool argument, or "" if absent. */
export function argString(args: Record<string, unknown>, key: string): string {
  const v = args[key];
  return typeof v === "string" ? v : "";
}

/**
 * Return the numeric value of a tool argument truncated to an integer, or
 * `def` if absent. JSON numbers decode as float64, so all numeric forms are
 * accepted. Mirrors `baseworker.ArgInt`.
 */
export function argInt(
  args: Record<string, unknown>,
  key: string,
  def: number,
): number {
  const v = args[key];
  if (typeof v === "number" && Number.isFinite(v)) return Math.trunc(v);
  return def;
}

// The registry key is the event type with a NUL (\x00) suffix, which never
// occurs in an event-type identifier, so distinct extensions can never
// collide. Internal-only; never surfaced on the wire.
function extensionKey(ext: Extension): string {
  return `${ext.event}\x00`;
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}
