import { describe, expect, it } from "vitest";
import { BUS_ENV_VARS, readBusEnv } from "./env.js";

describe("readBusEnv", () => {
  it("reads all three variables", () => {
    const env = {
      [BUS_ENV_VARS.busURL]: "http://localhost:8080/",
      [BUS_ENV_VARS.workerID]: "hello@me",
      [BUS_ENV_VARS.credential]: "secret",
    };
    expect(readBusEnv(env)).toEqual({
      baseURL: "http://localhost:8080",
      workerID: "hello@me",
      credential: "secret",
    });
  });

  it("throws naming every missing variable", () => {
    const env = { [BUS_ENV_VARS.workerID]: "hello@me" };
    expect(() => readBusEnv(env)).toThrow(BUS_ENV_VARS.busURL);
    expect(() => readBusEnv(env)).toThrow(BUS_ENV_VARS.credential);
    expect(() => readBusEnv(env)).toThrow(
      /missing environment variable/,
    );
  });

  it("treats whitespace-only values as missing", () => {
    const env = {
      [BUS_ENV_VARS.busURL]: "   ",
      [BUS_ENV_VARS.workerID]: "hello@me",
      [BUS_ENV_VARS.credential]: "secret",
    };
    expect(() => readBusEnv(env)).toThrow(BUS_ENV_VARS.busURL);
  });
});
