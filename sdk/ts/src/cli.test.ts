import { describe, expect, it } from "vitest";
import { BUS_CLI_FLAGS, busConnFromArgs, parseCliArgs } from "./cli.js";
import { BUS_ENV_VARS } from "./env.js";

describe("parseCliArgs", () => {
  it("parses --key value and bare --flag", () => {
    const { help, opts } = parseCliArgs([
      "node",
      "start.js",
      "--bus-url",
      "http://localhost:8080",
      "--worker-id",
      "w@me",
      "--verbose",
    ]);
    expect(help).toBe(false);
    expect(opts).toEqual({
      "bus-url": "http://localhost:8080",
      "worker-id": "w@me",
      verbose: true,
    });
  });

  it("flags --help / -h", () => {
    expect(parseCliArgs(["node", "start.js", "--help"]).help).toBe(true);
    expect(parseCliArgs(["node", "start.js", "-h"]).help).toBe(true);
  });

  it("strips --help/-h from opts", () => {
    const { opts } = parseCliArgs(["node", "start.js", "--bus-url", "x", "--help"]);
    expect(opts).toEqual({ "bus-url": "x" });
  });

  it("throws on a stray positional argument", () => {
    expect(() => parseCliArgs(["node", "start.js", "nope"])).toThrow(/unexpected argument/);
  });
});

describe("busConnFromArgs", () => {
  const env = {
    [BUS_ENV_VARS.busURL]: "http://localhost:8080/",
    [BUS_ENV_VARS.workerID]: "w@me",
    [BUS_ENV_VARS.credential]: "secret",
  } as Record<string, string | undefined>;

  it("reads from env when no flags given", () => {
    const conn = busConnFromArgs({}, env);
    expect(conn.baseURL).toBe("http://localhost:8080");
    expect(conn.workerID).toBe("w@me");
  });

  it("layers explicit flags over env", () => {
    const conn = busConnFromArgs({ "bus-url": "http://alt:9999", "worker-id": "cli@me" }, env);
    expect(conn.baseURL).toBe("http://alt:9999");
    expect(conn.workerID).toBe("cli@me");
    expect(conn.credential).toBe("secret");
  });

  it("exposes BUS_CLI_FLAGS mapping", () => {
    expect(BUS_CLI_FLAGS).toEqual({
      "bus-url": "baseURL",
      "worker-id": "workerID",
      credential: "credential",
    });
  });
});