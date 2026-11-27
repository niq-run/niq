import { describe, expect, it, afterEach } from "vitest";
import { mkdtemp } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  BaseWorker,
  ConfigUnavailableError,
  StateUnavailableError,
  type ConfigPatch,
} from "./baseworker.js";
import { MemoryDurableStore, type DurableStore } from "./durable.js";
import { STATE_ENV_VAR } from "./env.js";
import type { Event, WorkerSideChannel } from "./types/index.js";

class FakeChannel implements WorkerSideChannel {
  readonly id = "fake";
  async connect(): Promise<void> {}
  async send(): Promise<void> {}
  async broadcast(): Promise<void> {}
  // eslint-disable-next-line require-yield
  async *events(): AsyncIterable<Event> {}
  async close(): Promise<void> {}
}

function worker(opts: { durable?: DurableStore; durableDir?: string } = {}): BaseWorker {
  return new BaseWorker({ id: "peer.0", channel: new FakeChannel(), ...opts });
}

describe("patchConfig", () => {
  it("reports unavailable when no provider was injected", async () => {
    await expect(worker().patchConfig({ remote_url: "http://x" })).rejects.toBeInstanceOf(
      ConfigUnavailableError,
    );
  });

  it("forwards the patch to the injected provider", async () => {
    const w = worker();
    const seen: ConfigPatch[] = [];
    w.setConfigPatcher((patch) => {
      seen.push(patch);
    });

    await w.patchConfig({ remote_url: "http://x", note: null });

    expect(seen).toEqual([{ remote_url: "http://x", note: null }]);
  });

  it("awaits an async provider and propagates its error", async () => {
    const w = worker();
    let released: () => void = () => {};
    const gate = new Promise<void>((resolve) => {
      released = resolve;
    });
    w.setConfigPatcher(async () => {
      await gate;
      throw new Error("boom");
    });

    const pending = w.patchConfig({ a: 1 });
    released();
    await expect(pending).rejects.toThrow("boom");
  });

  it("can replace a previously installed provider", async () => {
    const w = worker();
    const calls: string[] = [];
    w.setConfigPatcher(() => {
      calls.push("first");
    });
    w.setConfigPatcher(() => {
      calls.push("second");
    });

    await w.patchConfig({});

    expect(calls).toEqual(["second"]);
  });
});

describe("durable state", () => {
  it("has no store by default: load is undefined, save reports unavailable", async () => {
    const w = worker();
    expect(w.hasDurableStore()).toBe(false);
    expect(await w.loadState()).toBeUndefined();
    await expect(w.saveState({ a: 1 })).rejects.toBeInstanceOf(StateUnavailableError);
  });

  it("uses the default file store when a directory is passed in", async () => {
    const dir = await mkdtemp(join(tmpdir(), "niq-worker-"));
    const w = worker({ durableDir: dir });

    expect(w.hasDurableStore()).toBe(true);
    await w.saveState({ count: 3, name: "ada" });
    expect(await w.loadState()).toEqual({ count: 3, name: "ada" });

    // A fresh worker over the same directory sees the persisted value.
    const again = worker({ durableDir: dir });
    expect(await again.loadState()).toEqual({ count: 3, name: "ada" });
  });

  it("lets the worker override the default store", async () => {
    const dir = await mkdtemp(join(tmpdir(), "niq-worker-"));
    const w = worker({ durableDir: dir });
    const memory = new MemoryDurableStore();
    w.setDurableStore(memory);

    await w.saveState({ via: "override" });

    expect(w.hasDurableStore()).toBe(true);
    expect(await memory.load()).toBe('{"via":"override"}');
    // The file store was never written to.
    const freshFileStore = worker({ durableDir: dir });
    expect(await freshFileStore.loadState()).toBeUndefined();
  });

  it("an explicit store wins over the directory", async () => {
    const memory = new MemoryDurableStore();
    const w = worker({ durable: memory, durableDir: "/nonexistent/should-not-be-used" });

    await w.saveState({ a: 1 });
    expect(await memory.load()).toBe('{"a":1}');
  });

  it("clearing the store turns persistence off again", async () => {
    const w = worker({ durable: new MemoryDurableStore() });
    w.setDurableStore(null);
    expect(w.hasDurableStore()).toBe(false);
    await expect(w.saveState({})).rejects.toBeInstanceOf(StateUnavailableError);
  });

  it("reports a corrupt snapshot instead of pretending there is no state", async () => {
    const store = new MemoryDurableStore();
    await store.save("{not json");
    const w = worker({ durable: store });

    await expect(w.loadState()).rejects.toThrow(/not valid JSON/);
  });

  it("treats an empty file as no state", async () => {
    const store = new MemoryDurableStore();
    await store.save("");
    expect(await worker({ durable: store }).loadState()).toBeUndefined();
  });

  it("refuses to persist a value JSON cannot represent", async () => {
    const w = worker({ durable: new MemoryDurableStore() });
    await expect(w.saveState(undefined)).rejects.toThrow(/JSON-serializable/);
  });

  describe("state directory injected through the environment", () => {
    // The SDK reads the directory once, at construction, so each case controls
    // the ambient value explicitly and restores it afterwards.
    const saved = process.env[STATE_ENV_VAR];
    afterEach(() => {
      if (saved === undefined) delete process.env[STATE_ENV_VAR];
      else process.env[STATE_ENV_VAR] = saved;
    });

    it("persists under NIQ_STATE_DIR when it is set", async () => {
      const dir = await mkdtemp(join(tmpdir(), "niq-env-state-"));
      process.env[STATE_ENV_VAR] = dir;

      const w = worker();
      expect(w.hasDurableStore()).toBe(true);
      await w.saveState({ from: "env" });
      expect(await worker().loadState()).toEqual({ from: "env" });
    });

    it("persists nothing when the variable is absent", async () => {
      delete process.env[STATE_ENV_VAR];
      expect(worker().hasDurableStore()).toBe(false);
    });

    it("persists nothing when the variable is blank", async () => {
      process.env[STATE_ENV_VAR] = "   ";
      expect(worker().hasDurableStore()).toBe(false);
    });

    it("lets durableDir override the environment", async () => {
      const envDir = await mkdtemp(join(tmpdir(), "niq-env-state-"));
      const optDir = await mkdtemp(join(tmpdir(), "niq-opt-state-"));
      process.env[STATE_ENV_VAR] = envDir;

      const w = worker({ durableDir: optDir });
      await w.saveState({ where: "opt" });

      expect(await worker({ durableDir: optDir }).loadState()).toEqual({ where: "opt" });
      // The env directory was untouched.
      expect(await worker({ durableDir: envDir }).loadState()).toBeUndefined();
    });

    it("lets an explicit store override the environment", async () => {
      process.env[STATE_ENV_VAR] = "/nonexistent/env-dir";
      const memory = new MemoryDurableStore();
      const w = worker({ durable: memory });

      await w.saveState({ via: "explicit" });
      expect(await memory.load()).toBe('{"via":"explicit"}');
    });
  });
});
