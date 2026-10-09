import { describe, expect, it } from "vitest";
import { HTTPWorkerClient } from "./client.js";

function isEvents(url: string): boolean {
  return new URL(url).pathname === "/events";
}

function sseStream(payloads: string[]): ReadableStream<Uint8Array> {
  const body = payloads.map((p) => `data: ${p}\n\n`).join("");
  return new ReadableStream({
    start(controller) {
      controller.enqueue(new TextEncoder().encode(body));
      controller.close();
    },
  });
}

describe("publish wire shape", () => {
  it("sends a directed event with targets and stamps worker_id", async () => {
    let body: any;
    const fetchImpl = (async (url: string, init?: RequestInit) => {
      if (isEvents(String(url))) {
        return new Response(sseStream(["{}"]), { status: 200 });
      }
      body = JSON.parse(init!.body as string);
      return new Response(JSON.stringify({ status: "ok" }), { status: 200 });
    }) as typeof fetch;

    const client = new HTTPWorkerClient({
      baseURL: "http://localhost:8080",
      workerID: "w1",
      credential: "secret",
      fetchImpl,
    });
    await client.connect();
    await client.send({ id: "e1", type: "tool.request", status: "created", worker_id: "attacker", payload: {}, timestamp: 1 }, "target");

    expect(body).toEqual({
      worker_id: "w1",
      credential: "secret",
      type: "send",
      events: [
        {
          id: "e1",
          type: "tool.request",
          status: "created",
          worker_id: "w1", // overwritten at the edge
          payload: {},
          timestamp: 1,
        },
      ],
      targets: ["target"],
    });
  });

  it("rejects send with no targets", async () => {
    const fetchImpl = (async () =>
      new Response(sseStream(["{}"]), { status: 200 })) as typeof fetch;
    const client = new HTTPWorkerClient({
      baseURL: "http://localhost:8080",
      workerID: "w1",
      credential: "secret",
      fetchImpl,
    });
    await client.connect();
    await expect(
      client.send({ id: "e", type: "x", status: "created", worker_id: "", payload: {}, timestamp: 1 }),
    ).rejects.toThrow(/at least one target/);
  });
});

describe("reconnect on session loss", () => {
  it("re-establishes the SSE session and retries a publish that hit 'worker not connected'", async () => {
    let eventsHit = 0;
    let publishHit = 0;
    const fetchImpl = (async (url: string) => {
      if (isEvents(String(url))) {
        eventsHit++;
        return new Response(sseStream(["{}"]), { status: 200 });
      }
      publishHit++;
      if (publishHit === 1) {
        // First publish: the bus has no session for us.
        return new Response("worker not connected", { status: 400 });
      }
      return new Response(JSON.stringify({ status: "ok" }), { status: 200 });
    }) as typeof fetch;

    const client = new HTTPWorkerClient({
      baseURL: "http://localhost:8080",
      workerID: "w1",
      credential: "secret",
      fetchImpl,
    });
    await client.connect();
    expect(eventsHit).toBe(1);

    await client.send(
      { id: "e1", type: "x", status: "created", worker_id: "w1", payload: {}, timestamp: 1 },
      "target",
    );

    expect(eventsHit).toBe(2); // reconnect opened /events again
    expect(publishHit).toBe(2); // original 400 + successful retry
  });

  it("still throws when the retry also fails with a non-session error", async () => {
    const fetchImpl = (async (url: string) => {
      if (isEvents(String(url))) {
        return new Response(sseStream(["{}"]), { status: 200 });
      }
      return new Response("boom", { status: 500 });
    }) as typeof fetch;

    const client = new HTTPWorkerClient({
      baseURL: "http://localhost:8080",
      workerID: "w1",
      credential: "secret",
      fetchImpl,
    });
    await client.connect();
    await expect(
      client.send(
        { id: "e1", type: "x", status: "created", worker_id: "w1", payload: {}, timestamp: 1 },
        "target",
      ),
    ).rejects.toThrow(/500/);
  });
});

describe("SSE parsing", () => {
  it("parses JSON events from the event stream", async () => {
    const fetchImpl = (async () =>
      new Response(
        sseStream([
          JSON.stringify({ id: "1", type: "tool.completed" }),
          JSON.stringify({ id: "2", type: "tool.completed" }),
        ]),
        { status: 200 },
      )) as typeof fetch;

    const client = new HTTPWorkerClient({
      baseURL: "http://b:8080",
      workerID: "w",
      credential: "c",
      fetchImpl,
    });
    await client.connect();
    const got: string[] = [];
    for await (const evt of client.events()) {
      got.push(evt.id);
    }
    expect(got).toEqual(["1", "2"]);
  });
});