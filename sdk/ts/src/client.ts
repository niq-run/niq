import type { Event, WorkerMessage } from "./types/index.js";
import type { WorkerSideChannel } from "./types/index.js";
import { parseEventStream } from "./sse.js";

/** Minimal wire request body sent to `POST /publish`. */
interface PublishWire {
  worker_id: string;
  credential: string;
  type: "send" | "broadcast";
  events: Event[];
  targets?: string[];
  trace_id?: string;
}

export interface HTTPWorkerClientOptions {
  /** Base URL of the niq bus, e.g. `http://localhost:8080`. Trailing slash optional. */
  baseURL: string;
  /** Worker identity registered on the bus (control plane created it). */
  workerID: string;
  /** Credential for this worker, verified at connect/publish time. */
  credential: string;
  /** Injectable fetch (e.g. for tests / proxies). Defaults to global fetch. */
  fetchImpl?: typeof fetch;
  /** Optional logger for reconnect / network diagnostics (defaults to none). */
  logger?: Pick<Console, "debug" | "warn">;
}

/**
 * A WorkerSideChannel over the niq HTTP transport.
 *
 * The protocol — mirroring the Go `httptrans` package:
 * - Connect: open `GET /events?worker_id=&credential=` as an SSE stream.
 * - Send/Broadcast: `POST /publish` with a {@link WorkerMessage} body.
 *
 * Every request carries its own `worker_id` + `credential`; there is no session
 * token. The SSE stream acts as the connection anchor: the bus requires it to
 * be open before it accepts publishes, and only one live connection per worker
 * is kept. Closing the SSE stream marks the worker offline (identity is
 * retained and can reconnect).
 *
 * Identity registration is NOT part of this SDK — it is a control-plane
 * operation (registryapi) performed before the worker connects.
 */
export class HTTPWorkerClient implements WorkerSideChannel {
  readonly id: string;

  private readonly baseURL: string;
  private readonly workerID: string;
  private readonly credential: string;
  private readonly fetchImpl: typeof fetch;
  private readonly logger?: Pick<Console, "debug" | "warn">;

  private es?: ReadableStream<Uint8Array>;
  private controller?: AbortController;
  private connected = false;
  private closed = false;
  private reconnectPromise?: Promise<void>;

  constructor(opts: HTTPWorkerClientOptions) {
    this.id = opts.workerID;
    this.baseURL = opts.baseURL.replace(/\/+$/, "");
    this.workerID = opts.workerID;
    this.credential = opts.credential;
    this.fetchImpl = opts.fetchImpl ?? fetch;
    this.logger = opts.logger;
  }

  async connect(): Promise<void> {
    if (this.connected) return;
    if (this.closed) throw new Error("niq: client is closed");
    const { es, controller } = await this.openSSE();
    this.es = es;
    this.controller = controller;
    this.connected = true;
  }

  private async openSSE(): Promise<{
    es: ReadableStream<Uint8Array>;
    controller: AbortController;
  }> {
    const url = `${this.baseURL}/events?worker_id=${encodeURIComponent(
      this.workerID,
    )}&credential=${encodeURIComponent(this.credential)}`;

    const controller = new AbortController();
    let resp: Response;
    try {
      resp = await this.fetchImpl(url, { signal: controller.signal });
    } catch (err) {
      throw new Error(`niq: SSE connect failed: ${(err as Error).message}`);
    }
    if (!resp.ok || !resp.body) {
      throw new Error(`niq: SSE connect failed with status ${resp.status}`);
    }
    return { es: resp.body, controller };
  }

  /**
   * Re-establish the SSE stream (the client's session anchor on the bus).
   * Used when the bus no longer has a session for us (publish 400
   * "worker not connected"), e.g. after the bus restarted or our connection
   * was silently torn down. Concurrent callers share one in-flight attempt.
   */
  private reconnectSSE(): Promise<void> {
    if (!this.reconnectPromise) {
      this.reconnectPromise = this.doReconnect()
        .catch((err) => {
          this.connected = false;
          this.logger?.debug?.(`niq: reconnect failed: ${(err as Error).message}`);
          throw err;
        })
        .finally(() => {
          this.reconnectPromise = undefined;
        });
    }
    return this.reconnectPromise;
  }

  private async doReconnect(): Promise<void> {
    this.logger?.debug?.("niq: session lost; reconnecting SSE");
    // Tear down the current stream so the bus releases the old session.
    this.controller?.abort();
    this.connected = false;
    // Reopen /events (re-registers the session), then resume publishing.
    const { es, controller } = await this.openSSE();
    this.es = es;
    this.controller = controller;
    this.connected = true;
  }

  async send(event: Event, ...targets: string[]): Promise<void> {
    this.assertOpen();
    if (targets.length === 0) {
      throw new Error("niq: send requires at least one target; use broadcast() for undirected delivery");
    }
    await this.publish("send", event, targets);
  }

  async broadcast(event: Event): Promise<void> {
    this.assertOpen();
    await this.publish("broadcast", event, undefined);
  }

  events(): AsyncIterable<Event> {
    if (!this.connected) throw new Error("niq: not connected");
    const stream = this.es;
    if (!stream) throw new Error("niq: no event stream");
    return parseEventStream(stream);
  }

  async close(): Promise<void> {
    if (this.closed) return;
    this.closed = true;
    this.connected = false;
    this.controller?.abort();
  }

  private assertOpen(): void {
    if (!this.connected) throw new Error(`niq: worker ${this.workerID} not connected`);
  }

  private async publish(
    type: "send" | "broadcast",
    event: Event,
    targets: string[] | undefined,
  ): Promise<void> {
    // Stamp authorship. The bus overwrites it anyway (anti-spoofing), but this
    // is done at the edge so a caller cannot accidentally attribute to another.
    const stamped: Event = { ...event, worker_id: this.workerID };

    const body: PublishWire = {
      worker_id: this.workerID,
      credential: this.credential,
      type,
      events: [stamped],
      ...(targets ? { targets } : {}),
    };

    let resp: Response;
    try {
      resp = await this.fetchImpl(`${this.baseURL}/publish`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
    } catch (err) {
      throw new Error(`niq: publish failed: ${(err as Error).message}`);
    }
    if (resp.ok) return;

    const detail = await resp.text().catch(() => "");

    // The bus has no session for us (400 "worker not connected"): the SSE
    // anchor was silently torn down (bus restart, dropped connection).
    // Re-establish it and retry this publish once.
    if (
      resp.status === 400 &&
      detail.includes("worker not connected") &&
      !this.closed
    ) {
      await this.reconnectSSE();
      resp = await this.fetchImpl(`${this.baseURL}/publish`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
    }
    if (!resp.ok) {
      const retryDetail = await resp.text().catch(() => "");
      throw new Error(
        `niq: publish failed (${resp.status}): ${retryDetail}`,
      );
    }
  }
}