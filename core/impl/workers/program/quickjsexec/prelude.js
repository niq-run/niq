// The trusted bootstrap evaluated before every script, inside the same VM.
//
// It builds the `sdk` facade (the same contract the node launcher used to
// provide), keeps the run's result on the global so the host can read it back,
// and installs the few globals a script may reasonably expect. The host
// functions it calls (__niq_log / __niq_send / __niq_finish / __niq_abort) are
// registered by session.go; everything here is plain JavaScript.
//
// The script itself is appended after this file as the body of an async arrow
// function, so top-level `await` and `return` both work. Go counts this file's
// lines and subtracts them from stack line numbers, so an error names the
// script's own line — keep the newline count stable.
"use strict";

// ── console ──
//
// QuickJS has no console (it is an engine, not a runtime). Scripts that reach
// for it get a working one instead of a ReferenceError, and the lines land in
// the run's log like sdk.log does.
const __fmt = (v) => {
  if (typeof v === "string") return v;
  if (v instanceof Error) return (v.name || "Error") + ": " + v.message;
  try {
    const s = JSON.stringify(v);
    return s === undefined ? String(v) : s;
  } catch {
    return String(v);
  }
};
const __logLine = (...a) => __niq_log(a.map(__fmt).join(" "));
globalThis.console = Object.freeze({
  log: __logLine,
  info: __logLine,
  warn: __logLine,
  error: __logLine,
  debug: __logLine,
});

// ── the result ──
//
// `sdk.result(v)` wins over a top-level `return`, and the last call wins: the
// same rule the node launcher implemented, kept here so a script behaves the
// same on either backend.
const __out = { set: false, value: "" };

// ── the SDK ──
const __pending = new Map();
let __seq = 0;

const sdk = Object.freeze({
  args: __niq_args,
  log: (text) => __niq_log(__fmt(text)),
  result: (payload) => {
    __out.set = true;
    __out.value = payload === undefined ? null : payload;
  },
  // One call = one id + one `__niq_send`; the host relays it to the bus and
  // comes back through __settle / __reject, which resolve this promise on the
  // VM's own goroutine. Calls are independent, so Promise.all really fans out.
  call: (target, event, args) => new Promise((resolve, reject) => {
    const id = "c" + (++__seq);
    __pending.set(id, { resolve, reject });
    __niq_send(id, "call", String(target), String(event), JSON.stringify(args === undefined ? {} : args));
  }),
  message: (target, text) => new Promise((resolve, reject) => {
    const id = "m" + (++__seq);
    __pending.set(id, { resolve, reject });
    __niq_send(id, "message", String(target), String(text), "{}");
  }),
});

// ── host → script hooks ──
globalThis.__settle = (id, value) => {
  const p = __pending.get(id);
  if (!p) return; // the run already ended; a late reply is dropped, not fatal
  __pending.delete(id);
  p.resolve(value);
};
globalThis.__reject = (id, message) => {
  const p = __pending.get(id);
  if (!p) return;
  __pending.delete(id);
  p.reject(new Error(String(message)));
};

// __niq_take serializes the result for the host: always JSON text, so the Go
// side gets the value's shape back (object/array/number/string) with no guess.
globalThis.__niq_take = () => {
  if (!__out.set || __out.value === undefined) return "";
  const text = JSON.stringify(__out.value);
  return text === undefined ? String(__out.value) : text;
};

const __describe = (e) => {
  if (e && typeof e === "object" && typeof e.message === "string") {
    return (e.name || "Error") + ": " + e.message + (e.stack ? "\n" + e.stack : "");
  }
  return String(e);
};

// The two terminal continuations of the script's async body.
const __finish = (v) => {
  if (!__out.set) {
    __out.set = true;
    __out.value = v === undefined ? "" : v;
  }
  __niq_finish();
};
const __abort = (e) => __niq_abort(__describe(e));
