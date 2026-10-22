import { describe, expect, it } from "vitest";
import { s, toJsonSchema } from "./schema.js";

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

describe("toJsonSchema", () => {
  it("passes a JSON Schema through untouched", () => {
    const schema = s.object({ a: s.string() });
    expect(toJsonSchema(schema, "parameters")).toBe(schema);
    expect(toJsonSchema({}, "parameters")).toEqual({});
  });

  it("converts a schema library object that exposes toJSONSchema", () => {
    // Stand-in for zod 4 / arktype: the SDK depends on no library, it just uses
    // the converter the object provides. Note the decoy `type` key: a library
    // schema can look like a JSON Schema at the top level while its real shape
    // lives in library-specific keys, so the converter must win.
    const zodLike = {
      type: "object",
      def: { type: "object", shape: { a: { type: "string" } } },
      toJSONSchema() {
        return { type: "object", properties: { a: { type: "string" } } };
      },
    };
    expect(toJsonSchema(zodLike, "parameters")).toEqual({
      type: "object",
      properties: { a: { type: "string" } },
    });
  });

  it("accepts the camelCase converter spelling", () => {
    const arkLike = {
      toJsonSchema() {
        return { type: "array", items: { type: "number" } };
      },
    };
    expect(toJsonSchema(arkLike, "response")).toEqual({
      type: "array",
      items: { type: "number" },
    });
  });

  it("rejects a library schema that has no converter (valibot, zod 3)", () => {
    // These carry their own keys (kind/expects, or _def) plus a top-level type;
    // announcing them as a JSON Schema would put library internals on the wire.
    const valibotLike = {
      kind: "schema",
      type: "object",
      expects: "Object",
      entries: { text: { kind: "schema", type: "string" } },
    };
    expect(() => toJsonSchema(valibotLike, "parameters of t")).toThrow(
      /@valibot\/to-json-schema/,
    );
    const zod3Like = { _def: { typeName: "ZodObject" }, type: "object" };
    expect(() => toJsonSchema(zod3Like, "parameters of t")).toThrow(/expected a JSON Schema/);
  });

  it("accepts spec and vendor extension keys", () => {
    expect(toJsonSchema({ type: "string", "x-ui": "textarea" }, "p")).toEqual({
      type: "string",
      "x-ui": "textarea",
    });
  });

  it("throws a clear error for something that is not a schema", () => {
    expect(() => toJsonSchema({ a: 1 }, "parameters of x")).toThrow(
      /parameters of x: expected a JSON Schema/,
    );
    expect(() => toJsonSchema("nope", "response of y")).toThrow(/response of y/);
  });
});
