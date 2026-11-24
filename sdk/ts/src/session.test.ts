import { describe, expect, it } from "vitest";
import { createEvent } from "./createEvent.js";
import { ReplyError } from "./reply.js";
import { InterfaceSession, matchesType } from "./session.js";
import { EventType, type Event, type WorkerSideChannel } from "./types/index.js";

/** A fake side channel: records sends and pushes inbound events on demand. */
class fakeChannel implements WorkerSideChannel {
  readonly id = "session-test";
  readonly sent: Event[] = [];
  readonly targets: string[][] = [];
  private readonly incoming: Event[] = [];
  private waiter?: () => void;
  private closed = false;

  async connect(): Promise<void> {}
  async send(evt: Event, ...targets: string[]): Promise<void> {
    this.sent.push(evt);
    this.targets.push(targets);
  }
  async broadcast(evt: Event): Promise<void> {
    this.sent.push(evt);
  }
  async *events(): AsyncIterable<Event> {
    while (!this.closed) {
      while (this.incoming.length > 0) {
        yield this.incoming.shift()!;
      }
      await new Promise<void>((resolve) => {
        this.waiter = resolve;
      });
    }
  }
  async close(): Promise<void> {
    this.closed = true;
    this.waiter?.();
    this.waiter = undefined;
  }

  push(evt: Event): void {
    this.incoming.push(evt);
    this.waiter?.();
    this.waiter = undefined;
  }
}

const wait = (ms = 10) => new Promise((r) => setTimeout(r, ms));

async function open(): Promise<{ session: InterfaceSession; channel: fakeChannel }> {
  const channel = new fakeChannel();
  const session = new InterfaceSession({ channel });
  await session.connect();
  return { session, channel };
}

describe("InterfaceSession", () => {
  it("connects, sends directed events and broadcasts", async () => {
    const { session, channel } = await open();
    const evt = createEvent("worker.input", { text: "hi" });

    await session.send(evt, "peer");
    expect(channel.sent[0]).toBe(evt);
    expect(channel.targets[0]).toEqual(["peer"]);
    await expect(session.send(createEvent("x", {}))).rejects.toThrow(
      /at least one target/,
    );

    const b = createEvent("worker.ready", {});
    await session.broadcast(b);
    expect(channel.sent[1]).toBe(b);

    await session.close();
  });

  it("pairs a reply with its request and surfaces a failed reply", async () => {
    const { session, channel } = await open();

    const ok = session.request("peer", createEvent("spawn", { type: "reason" }));
    await wait();
    const req = channel.sent[channel.sent.length - 1];
    expect(req.trace_id).toBeTruthy();
    expect(req.request_id).toBeTruthy();
    channel.push({
      ...createEvent(EventType.RequestCompleted, { ok: true }),
      request_id: req.request_id,
    });
    await expect(ok).resolves.toMatchObject({
      type: EventType.RequestCompleted,
    });

    const bad = session.request("peer", createEvent("spawn", {}));
    await wait();
    const req2 = channel.sent[channel.sent.length - 1];
    channel.push({
      ...createEvent(EventType.RequestFailed, { error: "nope" }),
      request_id: req2.request_id,
    });
    await expect(bad).rejects.toBeInstanceOf(ReplyError);
    await expect(bad).rejects.toThrow("nope");

    await session.close();
  });

  it("times out an unanswered request", async () => {
    const { session } = await open();
    await expect(
      session.request("peer", createEvent("spawn", {}), { timeoutMs: 20 }),
    ).rejects.toThrow(/timed out after 20ms waiting for peer/);
    await session.close();
  });

  it("buffers inbound events for a single consumer", async () => {
    const { session, channel } = await open();
    const iterator = session.events()[Symbol.asyncIterator]();

    channel.push(createEvent("a", {}));
    channel.push(createEvent("b", {}));
    await expect(iterator.next()).resolves.toMatchObject({
      value: { type: "a" },
      done: false,
    });
    await expect(iterator.next()).resolves.toMatchObject({
      value: { type: "b" },
      done: false,
    });

    await session.close();
    await expect(iterator.next()).resolves.toMatchObject({ done: true });
  });

  it("fails in-flight requests on close", async () => {
    const { session } = await open();
    const inflight = session.request("peer", createEvent("spawn", {}));
    await wait();
    await session.close();
    await expect(inflight).rejects.toThrow(/closed/);
  });

  it("fails in-flight requests when the channel ends", async () => {
    const { session, channel } = await open();
    const inflight = session.request("peer", createEvent("spawn", {}));
    await wait();
    await channel.close();
    await expect(inflight).rejects.toThrow(/bus closed/);
    await session.close();
  });

  it("times out a handshake that never resolves", async () => {
    class hangingChannel extends fakeChannel {
      override connect(): Promise<void> {
        return new Promise(() => {});
      }
    }
    const session = new InterfaceSession({
      channel: new hangingChannel(),
      connectTimeoutMs: 20,
    });
    await expect(session.connect()).rejects.toThrow(
      /connect timed out after 20ms/,
    );
    await session.close();
  });
});

describe("matchesType", () => {
  it("mirrors the bus's exact / * / prefix.* semantics", () => {
    expect(matchesType("*", "anything")).toBe(true);
    expect(matchesType("worker.input", "worker.input")).toBe(true);
    expect(matchesType("worker.input", "worker.input.x")).toBe(false);
    expect(matchesType("reason.*", "reason.response")).toBe(true);
    expect(matchesType("reason.*", "reason")).toBe(true);
    expect(matchesType("reason.*", "reasons.other")).toBe(false);
  });
});
