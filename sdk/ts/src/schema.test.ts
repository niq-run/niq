import { describe, expect, it } from "vitest";
import { s } from "./schema.js";

describe("schema builders", () => {
  it("builds an object schema with required by default", () => {
    const schema = s.object({
      target: s.string("chat_id or open_id"),
      count: s.integer(),
    });
    expect(schema).toEqual({
      type: "object",
      properties: {
        target: { type: "string", description: "chat_id or open_id" },
        count: { type: "integer" },
      },
      required: ["target", "count"],
    });
  });

  it("leaves optional properties out of required and keeps them plain", () => {
    const schema = s.object({
      text: s.string(),
      file: s.optional(s.string("local path or URL")),
    });
    expect(schema.required).toEqual(["text"]);
    // The optional marker must not leak into the wire form.
    expect(schema.properties).toEqual({
      text: { type: "string" },
      file: { type: "string", description: "local path or URL" },
    });
  });

  it("supports arrays, enums and free-form values", () => {
    expect(
      s.object({
        tags: s.array(s.string()),
        mode: s.enumOf(["fast", "deep"], "search mode"),
        extra: s.any(),
      }),
    ).toEqual({
      type: "object",
      properties: {
        tags: { type: "array", items: { type: "string" } },
        mode: { type: "string", enum: ["fast", "deep"], description: "search mode" },
        extra: {},
      },
      required: ["tags", "mode", "extra"],
    });
  });

  it("builds a response schema the same way", () => {
    expect(
      s.object({ messageId: s.string(), ok: s.boolean() }, "send result"),
    ).toEqual({
      type: "object",
      properties: { messageId: { type: "string" }, ok: { type: "boolean" } },
      required: ["messageId", "ok"],
      description: "send result",
    });
  });
});
