import { BaseWorker } from "./baseworker.js";
import { createEvent } from "./createEvent.js";
import { newId } from "./id.js";
import { ReplyError } from "./reply.js";
import type { HelpFunc } from "./help.js";
import {
  EventType,
  type Event,
  type EventTypeName,
  type Extension,
  type ExtensionHandler,
  type WorkerSideChannel,
} from "./types/index.js";

/**
 * InterfaceWorker — the TypeScript half of niq's interface worker.
 *
 * A worker whose job is not a domain of its own but a seam between the bus and
 * an external world (a chat platform bridge, an admin UI, a notifier). It faces
 * the bus like every worker (identity, ACL, presence, discover, tools) and holds
 * one or more {@link Backend}s behind it: the things that implement its
 * capability.
 *
 * Mirrors Go's `core/impl/iface`:
 *
 *   Backend.setHandler(handler)   the worker injects the handles, once, before
 *                                 any event is delivered
 *   Handler.send/broadcast        outbound as this worker
 *   Handler.request               send + await the correlated reply
 *   Handler.onEvent(filter, fn)   interest in inbound events (fan-out, isolated)
 *
 * It is NOT a bus control plane: editing ACLs and rendering the global event
 * stream belong to the app's own bus handles, not here.
 */

/** A filter for inbound interest: event types, or omitted for everything. */
export type EventFilter = readonly string[];

/** What a backend may do on the bus, as this worker's identity. */
export interface Handler {
  /** The worker id this handler speaks as. */
  readonly id: string;
  /** Send a directed event to the given targets. */
  send(evt: Event, ...targets: string[]): Promise<void>;
  /** Publish to every identity subscribing to the event type. */
  broadcast(evt: Event): Promise<void>;
  /**
   * Send and wait for the correlated `request.completed`, with the reply's error
   * message surfaced for `request.failed` / `request.rejected`.
   *
   * The correlation is not a new protocol — the reply is an ordinary event
   * addressed to this worker — but only the side holding the channel can pair it,
   * so this is the one place that owns ids, the wait and the timeout.
   */
  request(target: string, evt: Event, opts?: RequestOptions): Promise<Event>;
  /**
   * Register interest in inbound events. Returns an unsubscribe function.
   * Delivery is per registration, in arrival order; a throwing subscriber cannot
   * break the worker's loop or another subscriber.
   */
  onEvent(fn: (evt: Event) => void, filter?: EventFilter): () => void;
}

export interface RequestOptions {
  /** Override the worker's default request timeout. */
  timeoutMs?: number;
}

/** One outward-facing surface: the thing behind the worker. */
export interface Backend {
  /**
   * Receive the handles for reaching the bus, and register inbound interest.
   *
   * Called exactly ONCE, before the worker starts its loop and before any event
   * is delivered, so the backend can build its outward side already knowing how
   * to reach inward.
   */
  setHandler(handler: Handler): void;
}

/**
 * A backend that needs a lifetime of its own: a WebSocket connection, a render
 * loop, a subscription pump. `start()` runs after every backend has its handler;
 * `stop()` runs in reverse order when the worker stops.
 */
export interface Runnable {
  start(): Promise<void>;
  stop(): Promise<void>;
}

/**
 * The part of the worker a backend needs to serve its own tools: register the
 * extension (schema included, so it rides `worker.ready`), then reply. The
 * worker itself satisfies it, so a backend is wired by
 * `backend.registerTools(worker)` — mirroring Go's `iface.ToolHost`.
 */
export interface ToolHost {
  register(ext: Extension, handler: ExtensionHandler): void;
  registerReadme(
    description: string,
    text: () => string,
    params?: Record<string, unknown>,
  ): void;
  registerHelp(guides?: Record<string, HelpFunc>): void;
  replyCompleted(
    callerID: string,
    callID: string,
    result: unknown,
    traceID?: string,
  ): Promise<void>;
  replyFailed(
    callerID: string,
    callID: string,
    errorMessage: string,
    traceID?: string,
  ): Promise<void>;
}

export interface InterfaceWorkerOptions {
  /** Worker identity on the bus. */
  id: string;
  /** Worker-side channel (already constructed; `start` connects it). */
  channel: WorkerSideChannel;
  /** The outward-facing surfaces this worker bridges to. */
  backends?: Backend[];
  /** Type label announced via `worker.ready` (e.g. "interface"). */
  type?: string;
  /** Presence contract: the events this worker declares it emits. */
  publishes?: Record<string, unknown>[];
  /** Default request timeout in ms (default 30s, mirroring Go). */
  requestTimeoutMs?: number;
}

/** The default request timeout, matching Go's `iface.DefaultRequestTimeout`. */
export const DEFAULT_REQUEST_TIMEOUT_MS = 30_000;

interface Pending {
  resolve: (evt: Event) => void;
  reject: (err: Error) => void;
  timer: ReturnType<typeof setTimeout>;
}

/** A failed or rejected reply. Carries the reply event for inspection. */
export { ReplyError } from "./reply.js";

interface Subscription {
  fn: (evt: Event) => void;
  filter?: EventFilter;
  active: boolean;
}

export class InterfaceWorker extends BaseWorker {
  private readonly backends: Backend[];
  private readonly typ: string;
  private readonly publishes?: Record<string, unknown>[];
  private readonly requestTimeoutMs: number;

  private readonly handler: Handler;
  private readonly pending = new Map<string, Pending>();
  private readonly subs = new Set<Subscription>();
  private loop?: Promise<void>;
  private stopping = false;

  constructor(opts: InterfaceWorkerOptions) {
    super({ id: opts.id, channel: opts.channel });
    this.backends = opts.backends ?? [];
    this.typ = opts.type ?? "interface";
    this.publishes = opts.publishes;
    this.requestTimeoutMs = opts.requestTimeoutMs ?? DEFAULT_REQUEST_TIMEOUT_MS;
    this.handler = this.newHandler();
  }

  /**
   * Connect, inject the handler into every backend, then serve.
   *
   * Injection happens BEFORE the loop starts, so a backend can build its outward
   * side already knowing how to reach inward.
   */
  async start(): Promise<void> {
    await this.channel.connect();
    for (const backend of this.backends) {
      backend.setHandler(this.handler);
    }
    // Then give each backend its own lifetime: a backend whose start fails would
    // leave the worker deaf, so it is a start error.
    for (const backend of this.backends) {
      const runnable = backend as Partial<Runnable>;
      if (typeof runnable.start === "function") {
        await runnable.start();
      }
    }
    await this.announceReady(this.typ, this.publishes);
    this.loop = this.serve();
  }

  /** Leave the bus: stop backends, fail in-flight requests, close. */
  async stop(): Promise<void> {
    this.stopping = true;
    // Backends stop first, in reverse, so a backend's last delivery can still
    // reach the bus.
    for (let i = this.backends.length - 1; i >= 0; i -= 1) {
      const runnable = this.backends[i] as Partial<Runnable>;
      if (typeof runnable.stop === "function") {
        try {
          await runnable.stop();
        } catch (err) {
          console.warn(`[iface ${this.id}] backend stop failed:`, err);
        }
      }
    }
    for (const [id, p] of this.pending) {
      clearTimeout(p.timer);
      p.reject(new Error(`interface worker stopped while waiting for request ${id}`));
    }
    this.pending.clear();
    for (const sub of this.subs) sub.active = false;
    this.subs.clear();
    await this.channel.close();
  }

  /** Waits for the serving loop to end (it ends when the channel closes). */
  async done(): Promise<void> {
    await this.loop;
  }

  private async serve(): Promise<void> {
    for await (const evt of this.channel.events()) {
      if (this.stopping) return;
      this.dispatch(evt);
    }
  }

  /**
   * One inbound event: a reply resolves its pending request, a served tool is
   * answered by the extension registry, everything else is a message for the
   * backends.
   */
  private dispatch(evt: Event): void {
    if (this.resolve(evt)) {
      // A reply is also an inbound event: a backend watching the conversation may
      // want to render it.
      this.publish(evt);
      return;
    }
    // Order matters. A served tool is answered by the extension registry; any
    // other event belongs to the backends, and a message a backend consumes is NOT
    // a "no handler" error — that reply is only for events nobody wants. Asking
    // the subscriptions first keeps a forwarded message from being answered with
    // request.failed just because it carried a request id.
    if (!this.serves(evt.type) && this.publish(evt)) return;
    this.dispatchExtension(evt);
  }

  /** Whether the extension registry handles this event type. */
  private serves(type: string): boolean {
    return this.extensions().some((ext) => ext.event === type);
  }

  private resolve(evt: Event): boolean {
    if (
      evt.type !== EventType.RequestCompleted &&
      evt.type !== EventType.RequestFailed &&
      evt.type !== EventType.RequestRejected
    ) {
      return false;
    }
    const id = evt.request_id;
    if (!id) return false;
    const p = this.pending.get(id);
    if (!p) return false;
    this.pending.delete(id);
    clearTimeout(p.timer);
    if (evt.type === EventType.RequestCompleted) p.resolve(evt);
    else p.reject(new ReplyError(evt));
    return true;
  }

  /** Fan out to interested subscriptions, reporting whether any took it. */
  private publish(evt: Event): boolean {
    let taken = false;
    for (const sub of this.subs) {
      if (!sub.active) continue;
      if (sub.filter && sub.filter.length > 0 && !sub.filter.includes(evt.type)) {
        continue;
      }
      taken = true;
      this.deliver(sub, evt);
    }
    return taken;
  }

  /** Isolates a failing subscriber: one broken backend must not stop the rest. */
  private deliver(sub: Subscription, evt: Event): void {
    try {
      sub.fn(evt);
    } catch (err) {
      console.warn(`[iface ${this.id}] subscriber failed on ${evt.type}:`, err);
    }
  }

  private newHandler(): Handler {
    return {
      id: this.id,
      send: (evt, ...targets) => {
        if (targets.length === 0) {
          return Promise.reject(
            new Error("iface: send needs at least one target (use broadcast for everyone)"),
          );
        }
        return this.channel.send(evt, ...targets);
      },
      broadcast: (evt) => this.channel.broadcast(evt),
      request: (target, evt, opts) => this.request(target, evt, opts),
      onEvent: (fn, filter) => this.onEvent(fn, filter),
    };
  }

  private request(target: string, evt: Event, opts?: RequestOptions): Promise<Event> {
    if (!target) return Promise.reject(new Error("iface: request needs a target"));
    if (!evt.trace_id) evt.trace_id = newId();
    if (!evt.request_id) evt.request_id = newId();
    const id = evt.request_id;
    const timeout = opts?.timeoutMs ?? this.requestTimeoutMs;

    return new Promise<Event>((resolve, reject) => {
      if (this.stopping) {
        reject(new Error("iface: worker is stopped"));
        return;
      }
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new Error(`iface: timed out after ${timeout}ms waiting for a reply from ${target}`));
      }, timeout);
      this.pending.set(id, { resolve, reject, timer });
      this.channel.send(evt, target).catch((err: unknown) => {
        clearTimeout(timer);
        this.pending.delete(id);
        reject(err instanceof Error ? err : new Error(String(err)));
      });
    });
  }

  private onEvent(fn: (evt: Event) => void, filter?: EventFilter): () => void {
    const sub: Subscription = { fn, filter, active: true };
    this.subs.add(sub);
    return () => {
      sub.active = false;
      this.subs.delete(sub);
    };
  }
}

/** Convenience: build a reply event for a pending request. */
export function replyEvent(type: EventTypeName, requestId: string, payload: Record<string, unknown>, traceId?: string): Event {
  const evt = createEvent(type, payload);
  evt.request_id = requestId;
  if (traceId) evt.trace_id = traceId;
  return evt;
}
