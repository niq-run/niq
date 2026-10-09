import { describe, expect, it } from "vitest";
import { createEvent } from "./createEvent.js";

describe("createEvent", () => {
  it("fills deterministic defaults", () => {
    const evt = createEvent("tool.request", { tool: "x" }, 1_700_000_000_000);
    expect(evt.type).toBe("tool.request");
    expect(evt.status).toBe("created");
    expect(evt.payload).toEqual({ tool: "x" });
    expect(evt.worker_id).toBe(""); // stamped by the client at publish time
    expect(evt.timestamp).toBe(1_700_000_000); // unix seconds
    expect(evt.specversion).toBe("niq/1.0");
    expect(evt.id).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/,
    ); // UUIDv7
  });

  it("generates unique, time-ordered ids", () => {
    const a = createEvent("a");
    const b = createEvent("b", {}, Date.now() + 1);
    expect(a.id).not.toBe(b.id);
  });
});