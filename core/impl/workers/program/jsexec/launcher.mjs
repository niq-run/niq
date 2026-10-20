// PTC launcher — the trusted bootstrap executed inside the sandbox. It speaks
// a line-delimited JSON protocol on stdin/stdout with the program worker,
// compiles the untrusted script in memory, and injects a minimal SDK.
//
// SDK contract (kept in sync with the program.run tool description):
//   sdk.call(target, event, args) — a normal request to another worker. Resolves
//     with the target's result TEXT. A tool-level failure is a RESULT, not an
//     exception (e.g. bash exit 3 -> "Exit code: 3"); only a capability-level
//     failure (request.failed / rejected, unknown worker) rejects, so wrap calls
//     in try/catch only if you care about those. Calls run concurrently:
//     Promise.all([...]) fans out, one request id per call.
//   sdk.result(payload) — sets the run's result. A string is returned verbatim,
//     anything else is JSON-encoded. Last call wins.
//   return <payload> — same rendering, used ONLY when sdk.result was never
//     called (a top-level return is legal: the source is a function body).
//   neither -> the run's result is the empty string.
//   sdk.log(text) — echoed to the caller after the result under
//     "--- script log ---" (tail-capped) and mirrored to the worker log.
//   sdk.args — the program.run args object (never undefined; defaults to {}).
//
// Protocol (one JSON object per line):
//   worker -> script: {type:"run", src, args}
//                     {type:"call_result", id, value | error}
//   script -> worker: {type:"call", id, target, event, arguments}
//                     {type:"log", text}
//                     {type:"result", payload}
//                     {type:"error", message}
//
// stdout carries ONLY these protocol lines; diagnostics go to stderr.
import { createInterface } from "node:readline";

const write = (obj) => process.stdout.write(JSON.stringify(obj) + "\n");

const pending = new Map();
let seq = 0;
let resultText = "";
let resultSet = false;

// ── error location ──
//
// `new Function` wraps the source, so a runtime stack reports constructed lines
// (`<anonymous>:5:7`) that mean nothing to the script's author. The header size
// is measured at startup with a one-line probe, and syntax errors (whose stack
// carries no location at all) are located by re-compiling growing prefixes.

const HEADER_LINES = (() => {
  try {
    new Function("throw new Error('probe')")();
  } catch (e) {
    const m = /<anonymous>:(\d+):\d+/.exec(String((e && e.stack) || ""));
    if (m) return Number(m[1]) - 1; // lines above the body's first line
  }
  return 2; // node's `function anonymous(<args>\n) {\n` header
})();

function compile(src) {
  return new Function(
    "sdk",
    '"use strict";return (async () => {' + src + "\n})();",
  );
}

// syntaxErrorLine finds the first source line whose prefix fails to compile.
// Best effort by nature (a construct spanning lines can look broken earlier),
// but it still points at the right neighborhood far better than no location.
function syntaxErrorLine(src) {
  const lines = src.split("\n");
  for (let i = 1; i <= lines.length; i++) {
    try {
      compile(lines.slice(0, i).join("\n"));
    } catch {
      return i;
    }
  }
  return 0;
}

// describeError renders an error as name/message plus the user-script location,
// never the launcher's own stack, so the caller sees "script line 3" instead of
// `niq-ptc-launcher-*.mjs`.
function describeError(e, src) {
  const name = (e && e.name) || "Error";
  const message = (e && e.message) || String(e);
  const lines = src.split("\n");
  let line = 0;
  let column = 0;

  if (name === "SyntaxError") {
    line = syntaxErrorLine(src);
  } else {
    const m = /<anonymous>:(\d+):(\d+)/.exec(String((e && e.stack) || ""));
    if (m) {
      line = Number(m[1]) - HEADER_LINES;
      column = Number(m[2]);
    }
  }

  let out = name + ": " + message;
  if (line > 0 && line <= lines.length) {
    const text = lines[line - 1].trim();
    out += "\n    at script line " + line + (column > 0 ? ":" + column : "") +
      (text ? ": " + text : "");
  }
  return out;
}

function makeSdk(args) {
  return {
    args,
    log: (text) => write({ type: "log", text: String(text) }),
    result: (payload) => {
      resultSet = true;
      resultText = typeof payload === "string" ? payload : JSON.stringify(payload ?? null);
    },
    // Each call becomes a `call` protocol line; the reply comes back as
    // `call_result` and resolves this promise.
    call: (target, event, callArgs = {}) =>
      new Promise((resolve, reject) => {
        const id = "c" + ++seq;
        pending.set(id, { resolve, reject });
        write({ type: "call", id, target, event, arguments: callArgs });
      }),
  };
}

async function run(msg) {
  const sdk = makeSdk(msg.args || {});
  // The untrusted script is compiled in memory as an async function body — no
  // module import, so it cannot reach fs/net (and Bun --sandbox blocks those
  // anyway). `return X` or sdk.result(X) yields the final result.
  const factory = compile(msg.src);
  const ret = await factory(sdk);
  if (!resultSet) {
    resultText = ret === undefined ? "" : typeof ret === "string" ? ret : JSON.stringify(ret);
    resultSet = true;
  }
  write({ type: "result", payload: resultText });
}

const rl = createInterface({ input: process.stdin });
rl.on("line", (line) => {
  const text = line.trim();
  if (!text) return;
  let msg;
  try {
    msg = JSON.parse(text);
  } catch {
    return;
  }

  if (msg.type === "call_result") {
    const p = pending.get(msg.id);
    if (!p) return;
    pending.delete(msg.id);
    if (msg.error) p.reject(new Error(String(msg.error)));
    // The worker sends the reply VALUE: a string for prose, an object/array for a
    // structured reply. Resolving with it directly means a script gets the shape
    // the capability declared, with no JSON.parse guesswork.
    else p.resolve(msg.value);
    return;
  }

  if (msg.type === "run") {
    run(msg).catch((e) => {
      // The full stack stays server-side (stderr -> worker log); the caller gets
      // the located one-liner.
      process.stderr.write("[ptc] script error: " + String((e && e.stack) || e) + "\n");
      write({ type: "error", message: describeError(e, msg.src) });
    });
  }
});
