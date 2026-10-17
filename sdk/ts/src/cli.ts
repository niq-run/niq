/**
 * Small CLI utilities for writing a worker's `start` entrypoint — the launch
 * glue that turns a worker into a runnable app (e.g. the `bin` target compiled
 * from a `src/start.ts`).
 *
 * Every out-of-process worker builds on these so the connection bootstrap and
 * arg parsing don't get re-implemented per worker.
 */
import { readBusEnv, type BusEnv } from "./env.js";

/** Result of parsing a worker's command-line arguments. */
export interface ParsedCliArgs {
  /** True when `--help` / `-h` was passed. The caller prints its usage and exits. */
  help: boolean;
  /** Parsed `--key value` pairs; a flag given without a value maps to `true`. */
  opts: Record<string, string | true>;
}

/**
 * Parse `--key value` / `--key` arguments (strips the leading `node script`).
 * Does NOT exit or print — the caller decides how to react to `--help`.
 * Throws on an unexpected positional (non-flag) argument so the app's error
 * wrapper can report it and exit non-zero.
 */
export function parseCliArgs(argv: string[]): ParsedCliArgs {
  const args = argv.slice(2);
  const help = args.includes("--help") || args.includes("-h");
  const opts: Record<string, string | true> = {};
  for (let i = 0; i < args.length; i++) {
    const a = args[i];
    if (a === "--help" || a === "-h") continue;
    if (!a.startsWith("--")) {
      throw new Error(`unexpected argument: ${a}`);
    }
    const key = a.slice(2);
    const next = args[i + 1];
    if (next !== undefined && !next.startsWith("--")) {
      opts[key] = next;
      i++;
    } else {
      opts[key] = true;
    }
  }
  return { help, opts };
}

/** `--flag` names recognized for the bus connection, mapped to option keys. */
export const BUS_CLI_FLAGS = {
  "bus-url": "baseURL",
  "worker-id": "workerID",
  credential: "credential",
} as const;

/**
 * Build bus connection parameters by layering explicit CLI flags on top of the
 * supervisor-injected environment (`NIQ_BUS_URL` / `NIQ_WORKER_ID` /
 * `NIQ_WORKER_CREDENTIAL`, see {@link readBusEnv}). Explicit flags win.
 *
 * `env` is injectable for tests; defaults to `process.env`.
 */
export function busConnFromArgs(
  opts: Record<string, string | true>,
  env: Record<string, string | undefined> = process.env,
): BusEnv {
  const conn = readBusEnv(env);
  for (const [flag, key] of Object.entries(BUS_CLI_FLAGS)) {
    if (opts[flag] !== undefined) conn[key] = String(opts[flag]);
  }
  return conn;
}

/**
 * Run a worker app body, setting `process.exitCode = 1` and logging on
 * failure. Shared by every worker's `start` so failure handling is uniform.
 */
export async function runWorkerApp(run: () => Promise<void>): Promise<void> {
  try {
    await run();
  } catch (err) {
    console.error("[niq] worker exited:", err);
    process.exitCode = 1;
  }
}