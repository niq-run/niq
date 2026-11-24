/**
 * InterfaceSession — the stateless half of the interface worker.
 *
 * `InterfaceWorker` (iface.ts) is the stateful form: it announces presence,
 * serves tools, owns backends and lives for the process. `InterfaceSession` is
 * the ephemeral form used by CLIs, scripts and one-shot tools: dial, do the
 * work, close. It keeps no presence, no backend and no state after `close()`;
 * the SSE session itself still exists for the lifetime of the object.
 *
 * Both forms share the same request/reply convention: a request carries a fresh
 * `request_id`, and the reply is an ordinary `request.completed` /
 * `request.failed` / `request.rejected` addressed back to this identity.
 *
 * ```ts
 * const session = new InterfaceSession({ channel });
 * await session.connect();
 * const reply = await session.request("hello@bus", createEvent("hello.greet", { name: "niq" }));
 * for await (const evt of session.events()) { ... }
 * await session.close();
 * ```
 */
import { newId } from "./id.js";
import { ReplyError } from "./reply.js";
import {
  EventType,
  type Event,
  type WorkerSideChannel,
} from "./types/index.js";

/** Default timeout for one request's reply. */
export const DEFAULT_SESSION_REQUEST_TIMEOUT_MS = 30_000;
/** Default timeout for the channel handshake. */
export const DEFAULT_SESSION_CONNECT_TIMEOUT_MS = 15_000;

export interface InterfaceSessionOptions {
  /** The transport to dial (already constructed; `connect()` dials it). */
  channel: WorkerSideChannel;
  /** Default reply timeout for `request` (default 30s). */
  requestTimeoutMs?: number;
  /** Handshake timeout (default 15s). */
  connectTimeoutMs?: number;
}

export interface SessionRequestOptions {
  /** Override the session's default reply timeout. */
  timeoutMs?: number;
}

interface Pending {
  resolve: (evt: Event) => void;
  reject: (err: Error) => void;
  timer: ReturnType<typeof setTimeout>;
}

/**
 * A stateless interface worker: connect, send/request/stream, close.
 *
 * A request and a listener can share one session (the read loop always runs),
 * but `events()` is a single-consumer stream. Events that arrive before a
 * consumer reads them are buffered.
 */
export class InterfaceSession {
  /** The identity this session speaks as. */
  readonly id: string;

  private readonly channel: WorkerSideChannel;
  private readonly requestTimeoutMs: number;
  private readonly connectTimeoutMs: number;
  private readonly pending = new Map<string, Pending>();
  private readonly queue = new EventQueue();
  private loop?: Promise<void>;
  private connected = false;
  private closed = false;

  constructor(opts: InterfaceSessionOptions) {
    this.channel = opts.channel;
    this.id = opts.channel.id;
    this.requestTimeoutMs =
      opts.requestTimeoutMs ?? DEFAULT_SESSION_REQUEST_TIMEOUT_MS;
    this.connectTimeoutMs =
      opts.connectTimeoutMs ?? DEFAULT_SESSION_CONNECT_TIMEOUT_MS;
  }

  /** Dial the channel and start the read loop. Idempotent. */
  async connect(): Promise<void> {
    if (this.connected) return;
    if (this.closed) throw new Error("niq: interface session is closed");

    const connecting = this.channel.connect();
    // The timeout may win the race; a late rejection still needs a handler.
    connecting.catch(() => {});

    let timer: ReturnType<typeof setTimeout> | undefined;
    try {
      await Promise.race([
        connecting,
        new Promise<never>((_, reject) => {
          timer = setTimeout(() => {
            void this.channel.close().catch(() => {});
            reject(
              new Error(
                `niq: interface session connect timed out after ${this.connectTimeoutMs}ms`,
              ),
            );
          }, this.connectTimeoutMs);
        }),
      ]);
    } finally {
      if (timer !== undefined) clearTimeout(timer);
    }

    this.connected = true;
    this.loop = this.serve();
  }

  /** Send a directed event. At least one target is required. */
  async send(evt: Event, ...targets: string[]): Promise<void> {
    if (targets.length === 0) {
      throw new Error(
        "niq: interface session send needs at least one target; use broadcast() for undirected delivery",
      );
    }
    this.assertOpen();
    await this.channel.send(evt, ...targets);
  }

  /** Publish to every identity subscribing to the event type. */
  async broadcast(evt: Event): Promise<void> {
    this.assertOpen();
    await this.channel.broadcast(evt);
  }

  /**
   * Send an event and await the correlated reply. A `request.failed` /
   * `request.rejected` reply rejects with a {@link ReplyError} carrying the
   * event. A timeout rejects with a plain Error.
   *
   * `request_id` and `trace_id` are filled in when absent, so the caller can
   * read them back from the event it passed in.
   */
  request(
    target: string,
    evt: Event,
    opts: SessionRequestOptions = {},
  ): Promise<Event> {
    if (!target) {
      return Promise.reject(
        new Error("niq: interface session request needs a target"),
      );
    }
    try {
      this.assertOpen();
    } catch (err) {
      return Promise.reject(err);
    }

    const traceID = evt.trace_id ?? newId();
    evt.trace_id = traceID;
    const requestID = evt.request_id ?? newId();
    evt.request_id = requestID;
    const timeout = opts.timeoutMs ?? this.requestTimeoutMs;

    return new Promise<Event>((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(requestID);
        reject(
          new Error(
            `niq: interface session timed out after ${timeout}ms waiting for ` +
              `${target} to answer ${evt.type} (request ${requestID})`,
          ),
        );
      }, timeout);
      this.pending.set(requestID, { resolve, reject, timer });

      void this.channel.send(evt, target).catch((err: unknown) => {
        clearTimeout(timer);
        this.pending.delete(requestID);
        reject(err instanceof Error ? err : new Error(String(err)));
      });
    });
  }

  /**
   * Live inbound events, in arrival order. Single consumer; buffered until
   * read. Iteration ends when the channel ends or `close()` is called.
   */
  events(): AsyncIterable<Event> {
    return this.queue;
  }

  /** Leave the bus, fail in-flight requests and end `events()`. */
  async close(): Promise<void> {
    if (this.closed) return;
    this.closed = true;
    this.connected = false;
    this.failPending(
      new Error("niq: interface session closed while waiting for a reply"),
    );
    this.queue.close();
    await this.channel.close().catch(() => {});
  }

  /** Resolves when the read loop ends (channel closed or session closed). */
  async done(): Promise<void> {
    await this.loop;
  }

  private async serve(): Promise<void> {
    try {
      for await (const evt of this.channel.events()) {
        this.dispatch(evt);
      }
    } catch (err) {
      if (!this.closed) {
        this.failPending(
          err instanceof Error ? err : new Error(String(err)),
        );
      }
    } finally {
      if (!this.closed) {
        this.failPending(
          new Error("niq: the bus closed the interface session"),
        );
      }
      this.queue.close();
    }
  }

  private dispatch(evt: Event): void {
    this.resolve(evt);
    this.queue.push(evt);
  }

  private resolve(evt: Event): void {
    if (
      evt.type !== EventType.RequestCompleted &&
      evt.type !== EventType.RequestFailed &&
      evt.type !== EventType.RequestRejected
    ) {
      return;
    }
    const id = evt.request_id;
    if (!id) return;
    const pending = this.pending.get(id);
    if (!pending) return;

    this.pending.delete(id);
    clearTimeout(pending.timer);
    if (evt.type === EventType.RequestCompleted) pending.resolve(evt);
    else pending.reject(new ReplyError(evt));
  }

  private failPending(err: Error): void {
    for (const [, pending] of this.pending) {
      clearTimeout(pending.timer);
      pending.reject(err);
    }
    this.pending.clear();
  }

  private assertOpen(): void {
    if (!this.connected || this.closed) {
      throw new Error(`niq: interface session ${this.id} is not connected`);
    }
  }
}

/**
 * Type match mirroring the bus: exact, `*`, and `prefix.*` (which also matches
 * the bare prefix, e.g. `reason.*` matches `reason`).
 */
export function matchesType(pattern: string, type: string): boolean {
  if (pattern === "*") return true;
  if (pattern.endsWith(".*")) {
    const prefix = pattern.slice(0, -2);
    return type === prefix || type.startsWith(`${prefix}.`);
  }
  return type === pattern;
}

/** A single-consumer async queue used by `events()`. */
class EventQueue implements AsyncIterable<Event> {
  private readonly items: Event[] = [];
  private readonly waiters: Array<(result: IteratorResult<Event>) => void> = [];
  private done = false;

  push(evt: Event): void {
    if (this.done) return;
    const waiter = this.waiters.shift();
    if (waiter) waiter({ value: evt, done: false });
    else this.items.push(evt);
  }

  close(): void {
    if (this.done) return;
    this.done = true;
    for (const waiter of this.waiters.splice(0)) {
      waiter({ value: undefined as unknown as Event, done: true });
    }
  }

  [Symbol.asyncIterator](): AsyncIterator<Event> {
    return {
      next: (): Promise<IteratorResult<Event>> => {
        if (this.items.length > 0) {
          return Promise.resolve({ value: this.items.shift()!, done: false });
        }
        if (this.done) {
          return Promise.resolve({
            value: undefined as unknown as Event,
            done: true,
          });
        }
        return new Promise((resolve) => this.waiters.push(resolve));
      },
      // A consumer leaving early does not close the queue: another consumer
      // may read later. The session's close() is what ends it for everyone.
      return: (): Promise<IteratorResult<Event>> =>
        Promise.resolve({
          value: undefined as unknown as Event,
          done: true,
        }),
    };
  }
}
