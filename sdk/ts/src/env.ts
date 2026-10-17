/**
 * Environment-variable bootstrap for workers launched by the niq project
 * supervisor. When the bus launches an external worker, it injects the
 * connection parameters as env vars (see `internal/project/external.go`
 * `buildEnv`) so the process can connect on its own — no config file needed.
 */

/** The env var names injected by the niq supervisor. */
export const BUS_ENV_VARS = {
  busURL: "NIQ_BUS_URL",
  workerID: "NIQ_WORKER_ID",
  credential: "NIQ_WORKER_CREDENTIAL",
} as const;

/** Connection parameters read from the supervisor-injected environment. */
export interface BusEnv {
  baseURL: string;
  workerID: string;
  credential: string;
}

/**
 * Read the bus connection parameters from the environment. Throws with a
 * clear message naming every missing variable — they are all required.
 *
 * `env` is injectable for tests; defaults to `process.env`.
 */
export function readBusEnv(
  env: Record<string, string | undefined> = process.env,
): BusEnv {
  const baseURL = env[BUS_ENV_VARS.busURL]?.trim() || "";
  const workerID = env[BUS_ENV_VARS.workerID]?.trim() || "";
  const credential = env[BUS_ENV_VARS.credential]?.trim() || "";

  const missing = (Object.keys(BUS_ENV_VARS) as Array<keyof typeof BUS_ENV_VARS>)
    .filter((k) => ({ busURL: baseURL, workerID, credential })[k] === "")
    .map((k) => BUS_ENV_VARS[k]);
  if (missing.length > 0) {
    throw new Error(
      `niq: missing environment variable(s): ${missing.join(", ")}. ` +
        `These are injected automatically when the worker is launched by the ` +
        `niq project supervisor, or set them manually to connect to a bus.`,
    );
  }
  return { baseURL: baseURL.replace(/\/+$/, ""), workerID, credential };
}
