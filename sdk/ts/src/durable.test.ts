import { mkdtemp, readFile, readdir, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { FileDurableStore, MemoryDurableStore } from "./durable.js";

async function tempDir(): Promise<string> {
  return mkdtemp(join(tmpdir(), "niq-durable-"));
}

describe("FileDurableStore", () => {
  it("returns undefined before anything is saved", async () => {
    const store = new FileDurableStore(await tempDir());
    expect(await store.load()).toBeUndefined();
  });

  it("round-trips the saved text", async () => {
    const dir = await tempDir();
    const store = new FileDurableStore(dir);
    await store.save('{"count":1}');
    expect(await store.load()).toBe('{"count":1}');
    expect(await readFile(join(dir, "state.json"), "utf8")).toBe('{"count":1}');
  });

  it("replaces the previous value instead of appending", async () => {
    const store = new FileDurableStore(await tempDir());
    await store.save("first");
    await store.save("second");
    expect(await store.load()).toBe("second");
  });

  it("honours a custom file name", async () => {
    const dir = await tempDir();
    const store = new FileDurableStore(dir, "worker-state.json");
    await store.save("x");
    expect(await readFile(join(dir, "worker-state.json"), "utf8")).toBe("x");
  });

  it("creates a missing nested directory on save", async () => {
    const dir = join(await tempDir(), "nested", "deeper");
    const store = new FileDurableStore(dir);
    await store.save("x");
    expect(await store.load()).toBe("x");
  });

  it("leaves no temporary files behind", async () => {
    const dir = await tempDir();
    const store = new FileDurableStore(dir);
    await store.save("x");
    expect(await readdir(dir)).toEqual(["state.json"]);
  });

  it("applies concurrent saves in call order", async () => {
    const store = new FileDurableStore(await tempDir());
    // A worker saving on every event fires these without awaiting: the newest
    // snapshot must win, not whichever write happens to finish last.
    await Promise.all([store.save("1"), store.save("2"), store.save("3")]);
    expect(await store.load()).toBe("3");
  });

  it("reports a write failure to the caller", async () => {
    const parent = await tempDir();
    const asFile = join(parent, "not-a-directory");
    await writeFile(asFile, "x");
    // The store's directory is a regular file, so creating the temp sibling
    // fails — and the caller must see it rather than a silent no-op.
    const store = new FileDurableStore(asFile);
    await expect(store.save("boom")).rejects.toBeTruthy();
  });
});

describe("MemoryDurableStore", () => {
  it("returns undefined before anything is saved", async () => {
    expect(await new MemoryDurableStore().load()).toBeUndefined();
  });

  it("round-trips the saved text", async () => {
    const store = new MemoryDurableStore();
    await store.save("in-memory");
    expect(await store.load()).toBe("in-memory");
  });
});
