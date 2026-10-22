import { describe, expect, it } from "vitest";
import { replyText, replyValue } from "./reply.js";

describe("reply payload conventions", () => {
  it("an object reply is the value; a wrapped reply yields its result", () => {
    expect(replyValue({ mounts: ["/a"], primary: "/a" })).toEqual({
      mounts: ["/a"],
      primary: "/a",
    });
    expect(replyValue({ result: "Exit code: 0" })).toBe("Exit code: 0");
    expect(replyValue({ result: [{ worker_id: "w" }] })).toEqual([{ worker_id: "w" }]);
  });

  it("renders text verbatim and structured values as JSON", () => {
    expect(replyText({ result: "Exit code: 0" })).toBe("Exit code: 0");
    expect(replyText({ ok: true, n: 2 })).toBe('{"ok":true,"n":2}');
    expect(replyText({ result: ["a"] })).toBe('["a"]');
  });

  it("treats an empty payload as nothing to say", () => {
    expect(replyText({})).toBe("");
    expect(replyText({ result: null })).toBe("");
  });
});
