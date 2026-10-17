// PTC launcher — the trusted bootstrap executed inside the sandbox. It speaks
// a line-delimited JSON protocol on stdin/stdout with the program worker,
// compiles the untrusted script in memory, and injects a minimal SDK.
//
// Protocol (one JSON object per line):
//   worker -> script: {type:"run", src, args}
//                     {type:"call_result", id, text | error}
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
  const factory = new Function(
    "sdk",
    '"use strict";return (async () => {' + msg.src + "\n})();",
  );
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
    else p.resolve(msg.text ?? "");
    return;
  }

  if (msg.type === "run") {
    run(msg).catch((e) => write({ type: "error", message: String((e && e.stack) || e) }));
  }
});
