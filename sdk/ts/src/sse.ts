import type { Event } from "./types/index.js";

/**
 * Parse a Node ReadableStream of SSE bytes into Event objects, one per
 * `data:` block. The Go httptrans server writes a single `data: {json}\n\n`
 * per event (single line, compact JSON), but this parser is tolerant of
 * multi-line data blocks and ignores comment lines for robustness.
 *
 * Yields nothing until the first complete event; on parse failure of a block
 * it skips that block (mirrors the Go worker's `continue` on bad JSON).
 */
export async function* parseEventStream(
  stream: ReadableStream<Uint8Array>,
): AsyncGenerator<Event> {
  const reader = stream.getReader();
  const decoder = new TextDecoder();
  let buffer = "";

  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });

      // SSE frames are separated by a blank line. Split conservatively so a
      // partial line at the tail is retained until the next chunk arrives.
      let sep: number;
      while ((sep = buffer.indexOf("\n\n")) !== -1) {
        const frame = buffer.slice(0, sep);
        buffer = buffer.slice(sep + 2);
        const dataLines = frame
          .split("\n")
          .filter((l) => l.startsWith("data:"))
          .map((l) => l.slice(5).trimStart());
        if (dataLines.length === 0) continue;
        const payload = dataLines.join("\n");
        try {
          yield JSON.parse(payload) as Event;
        } catch {
          // skip malformed event, matching Go `continue`
        }
      }
    }
  } finally {
    reader.releaseLock();
  }
}