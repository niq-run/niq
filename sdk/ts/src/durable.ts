import { mkdir, readFile, rename, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";

/**
 * A worker's own durable state, behind one tiny contract: load the last saved
 * text, save a new one. Workers do not deal with files, locking or atomicity
 * themselves — and a host that wants a different backend can supply its own
 * implementation with {@link BaseWorker.setDurableStore}.
 *
 * Why text and not JSON: the store is a byte sink. Serialization policy belongs
 * to the worker (`loadState` / `saveState` on the base worker speak JSON), and a
 * store that only moves strings can be backed by a file, a database row or a
 * remote KV without changing its shape.
 */
export interface DurableStore {
  /** Return the last saved text, or undefined when nothing was ever saved. */
  load(): Promise<string | undefined>;
  /** Replace the saved text. Implementations must be atomic (no torn reads),
   * and concurrent saves must land in call order so the newest snapshot wins. */
  save(data: string): Promise<void>;
}

/**
 * The default durable store: one atomically-replaced file under a directory the
 * caller passes in.
 *
 * The directory is the caller's choice because only the caller knows where a
 * worker's state belongs — a worker launched by a host gets a per-worker
 * directory, a standalone tool gets its own config directory. The SDK does not
 * invent a location.
 *
 * Writes go to a temporary sibling and are renamed into place, so a crash
 * mid-write leaves the previous state intact rather than a truncated file.
 * Saves are also serialized: a worker that saves on every handled event must
 * not lose the newer snapshot to a slower older write, so each save is chained
 * after the previous one and lands in call order.
 */
export class FileDurableStore implements DurableStore {
  readonly dir: string;
  readonly fileName: string;

  /** Tail of the write chain; keeps saves applied in call order. */
  private queue: Promise<void> = Promise.resolve();

  constructor(dir: string, fileName = "state.json") {
    this.dir = dir;
    this.fileName = fileName;
  }

  /** Absolute-or-relative path of the state file this store owns. */
  get path(): string {
    return join(this.dir, this.fileName);
  }

  async load(): Promise<string | undefined> {
    try {
      return await readFile(this.path, "utf8");
    } catch (err) {
      if ((err as NodeJS.ErrnoException).code === "ENOENT") return undefined;
      throw err;
    }
  }

  save(data: string): Promise<void> {
    // Chain onto the previous write so the last call wins, not the last to
    // finish. A failed write must not poison the chain for later saves.
    const write = this.queue.then(() => this.writeNow(data));
    this.queue = write.catch(() => {});
    return write;
  }

  private async writeNow(data: string): Promise<void> {
    await mkdir(dirname(this.path), { recursive: true });
    // A per-write suffix: two concurrent saves must not share a temp file.
    const tmp = `${this.path}.tmp-${process.pid}-${Math.random().toString(36).slice(2)}`;
    await writeFile(tmp, data, "utf8");
    await rename(tmp, this.path);
  }
}

/**
 * A durable store that keeps the value in memory only.
 *
 * Useful for tests, and for a worker that wants the state API to exist while
 * running somewhere with no writable disk: it behaves like the file store for
 * one process lifetime and loses everything on exit.
 */
export class MemoryDurableStore implements DurableStore {
  private value?: string;

  load(): Promise<string | undefined> {
    return Promise.resolve(this.value);
  }

  save(data: string): Promise<void> {
    this.value = data;
    return Promise.resolve();
  }
}
