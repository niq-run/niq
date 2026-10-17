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

  constructor(opts: {
    id: string;
    subscriptions?: EventPattern[];
    channel: WorkerSideChannel;
  }) {
    this.id = opts.id;
    this.subscriptions = opts.subscriptions ?? [];
    this.channel = opts.channel;
  }

  /** Bind an extension to a handler. Safe at any time, including at runtime. */
  register(ext: Extension, handler: ExtensionHandler): void {
    this.regs.set(extensionKey(ext), { ext, handler });
  }

  /**
   * Route an event to the registered extension matching its event type.
   * Returns whether a handler ran; the first matching registration wins. Async
   * handlers are not awaited.
   */
  dispatchExtension(evt: Event): boolean {
    for (const { ext, handler } of this.regs.values()) {
      if (ext.event !== evt.type) continue;
      void handler(evt);
      return true;
    }
    return false;
  }

  /** Snapshot of the registered extensions (insertion order). */
  extensions(): Extension[] {
    return [...this.regs.values()].map((r) => r.ext);
  }

  /**
   * Render the registry into the `worker.ready` "watch" wire format.
   * `includeSelfOnly` controls whether SelfOnly extensions are rendered: a
   * peer-facing broadcast leaves them out (they are not callable by peers).
   */
  watchEntries(includeSelfOnly: boolean): Record<string, unknown>[] {
    const out: Record<string, unknown>[] = [];
    for (const ext of this.extensions()) {
      if (ext.selfOnly && !includeSelfOnly) continue;
      const entry: Record<string, unknown> = {
        event: ext.event,
        desc: ext.description,
      };
      if (ext.parameters) {
        entry["parameters"] = ext.parameters;
      }
      out.push(entry);
    }
    return out;
  }

  /**
   * Broadcast this worker's presence on the bus: the externally callable
   * contract (SelfOnly left out), self excluded via `exclude_worker_id`.
   * `publishes` is the wire form of the events this worker declares it emits
   * (`worker.ready` "publishes").
   */
  async announceReady(
    workerType: string,
    publishes?: Record<string, unknown>[],
  ): Promise<void> {
    const payload: Record<string, unknown> = {
      type: workerType,
      watch: this.watchEntries(false),
    };
    if (publishes && publishes.length > 0) {
      payload["publishes"] = publishes;
    }
    const presence = createEvent(EventType.WorkerReady, payload);
    presence.exclude_worker_id = this.id;
    try {
      await this.channel.broadcast(presence);
    } catch {
      // Presence announcements are best-effort, mirroring Go.
    }
  }

  // ── Tool-serving helpers ──
  //
  // The repetitive parts every tool-serving worker repeats: replying to a
  // tool-invocation with a request.completed / request.failed /
  // request.rejected result, echoing the request's id and propagating the
  // trace ID. Reply errors are ignored, mirroring Go.

  /** Answer a request caller with a request.completed result. */
  async replyCompleted(
    callerID: string,
    callID: string,
    result: string,
    traceID?: string,
  ): Promise<void> {
    await this.reply(
      EventType.RequestCompleted,
      callerID,
      callID,
      { result },
      traceID,
    );
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

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}
