/**
 * Rendering helpers for `worker.help` — the TS mirror of Go's
 * `baseworker/help.go`.
 *
 * `worker.help` is the per-event counterpart of `worker.readme`, like a CLI's
 * `-h`: `event_name` names one of the worker's events, and `params` may select
 * a more specific guide. The reusable registration lives on `BaseWorker`
 * (`registerHelp`); this file only renders the answers.
 */

import type { Extension } from "./types/index.js";

/**
 * Render a custom guide for one event, selected by the request's `params`
 * (e.g. `{ worker: "reason" }` for spawn). Return `null`/`undefined` when these
 * params do not match: the generic guide built from the event's declared
 * contract is returned instead.
 *
 * `params` is `{}` when the request carried none, so lookups are always safe.
 */
export type HelpFunc = (
  params: Record<string, unknown>,
) => string | null | undefined;

/**
 * Render the worker's capability list — the "no event_name" half of
 * `worker.help`. Sorted by event type so the index is stable.
 */
export function helpIndex(
  id: string,
  exts: Extension[],
  includeInternal: boolean,
): string {
  const out = [`# ${id} — help`, ""];
  out.push(
    "Call any event below by sending it to this worker with a request id; " +
      "the reply arrives as `request.completed`.",
    "",
  );
  for (const ext of visibleExtensions(exts, includeInternal)) {
    const desc = summary(ext.description ?? "");
    out.push(`- \`${ext.event}\` — ${desc === "" ? "(no description)" : desc}`);
  }
  out.push(
    "",
    "Ask for one event with `{\"event_name\": \"<event>\"}`. " +
      "Some events also accept `params` to pick a more specific guide.",
  );
  return out.join("\n");
}

/**
 * Render the generic guide for one event from its declared contract. It is
 * what a worker gets without writing any help text, and the fallback for params
 * a custom `HelpFunc` does not recognize.
 */
export function eventHelp(ext: Extension, paramsGiven: boolean): string {
  const out = [`# ${ext.event}`, ""];
  const desc = (ext.description ?? "").trim();
  if (desc !== "") out.push(desc, "");
  if (ext.parameters) {
    out.push("## Arguments", "", "```json", prettyJson(ext.parameters), "```", "");
  }
  if (ext.response) {
    out.push("## Reply", "", "```json", prettyJson(ext.response), "```", "");
  }
  out.push(
    "## How to call",
    "",
    "Send this event to the worker as a request: the payload IS the " +
      "arguments object above, and the reply echoes the same request id as " +
      "`request.completed`.",
  );
  if (paramsGiven) {
    out.push(
      "",
      "No event-specific guide matched `params`; this is the declared contract.",
    );
  }
  return out.join("\n");
}

/** Extensions the caller may see, sorted by event type. */
function visibleExtensions(
  exts: Extension[],
  includeInternal: boolean,
): Extension[] {
  return exts
    .filter((ext) => includeInternal || !ext.internal)
    .sort((a, b) => (a.event < b.event ? -1 : a.event > b.event ? 1 : 0));
}

/** Event names the caller may see, sorted. */
export function visibleEventNames(
  exts: Extension[],
  includeInternal: boolean,
): string[] {
  return visibleExtensions(exts, includeInternal).map((ext) => ext.event);
}

/** Keep a one-line description one line, so the index stays scannable. */
function summary(desc: string): string {
  const trimmed = desc.trim();
  const i = trimmed.indexOf("\n");
  return i >= 0 ? trimmed.slice(0, i) : trimmed;
}

function prettyJson(v: unknown): string {
  try {
    return JSON.stringify(v, null, 2) ?? String(v);
  } catch {
    return String(v);
  }
}
