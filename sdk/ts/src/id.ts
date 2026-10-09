import { randomBytes } from "node:crypto";

/**
 * Generate a time-ordered UUIDv7 string.
 *
 * Mirrors `core/event.newID` (github.com/google/uuid v7): globally unique and
 * sortable by generation time, suitable as a durable key and for chronological
 * audit/replay. Falls back to an unguessable random v4 when crypto is
 * unavailable (should not happen in Node).
 */
export function newId(now = Date.now()): string {
  // UUIDv7: 48-bit unix-ms timestamp + version nibble, then random bits.
  const bytes = randomBytes(16);
  const timeMillis = BigInt(now);
  bytes[0] = Number((timeMillis >> 40n) & 0xffn);
  bytes[1] = Number((timeMillis >> 32n) & 0xffn);
  bytes[2] = Number((timeMillis >> 24n) & 0xffn);
  bytes[3] = Number((timeMillis >> 16n) & 0xffn);
  bytes[4] = Number((timeMillis >> 8n) & 0xffn);
  bytes[5] = Number(timeMillis & 0xffn);

  // Set version (7) and variant (RFC 4122 10xx).
  bytes[6] = (bytes[6] & 0x0f) | 0x70;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;

  const hex = [...bytes].map((b) => b.toString(16).padStart(2, "0")).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}