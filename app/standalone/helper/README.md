# helper — the address and port primitives nothing else owns

`helper` is the stdlib-only package the single-machine form sits on. It answers
a narrow set of questions — what port does this address name, can it be dialled
or bound right now, which process is listening on it, is a given pid still alive
— and exposes the shaping helpers that keep a fallback bind on the host that was
asked for. Everything in it is a primitive several packages need and none of
them owns.

It must not know a project, a worker, a provider or auth, and it must not join a
path — paths belong to `store/root.go`. It also imports nothing from `app`, which
is what lets it sit *underneath* `store` (which uses it for address parsing)
without inverting the layering. Callers call `helper.*`; re-implementing a dial
probe or a port lookup elsewhere is a layering bug, not a shortcut.

## Where it sits

```
cli   gateway   launcher   project   store
  └───────┴─────────┴─────────┴────────┘
                  │
                  ▼
                helper  ──►  stdlib only (net, os/exec, syscall)
```

## Listening versus connecting

`Holders` is deliberately narrow: it lists the processes **LISTENING** on a port
(`lsof -sTCP:LISTEN`). A client that merely connected to that port is not a
holder. The distinction matters because `Signal` and `Kill` act on whatever
`Holders` returns, and interrupting a client — the gateway proxying a project,
say — would be a bug. Neither `Signal` nor `Kill` ever targets the calling
process, and pid 1 is dropped from the listing and never reported alive.

## Preserving exposure

Two functions exist so that recovering from a taken port cannot silently widen or
narrow who can reach a server. `EphemeralAddr` maps an address to port 0 on the
*host that was asked for*: a loopback bind stays loopback (a data port must not
become reachable from the LAN just because its port was busy), while a wildcard
bind stays wildcard. `LoopbackBind` is its opposite direction, forcing an address
onto `127.0.0.1` while keeping the port.

## Files

| File | Responsibility |
|---|---|
| `doc.go` | The package doc, and the two declarations everything else returns: `ErrUnsupported` and `Holder`. |
| `net.go` | Pure address work: `Port`, `DialAddr`, `IsServing`/`IsServingPort`, `ProbeListen`, `IsLoopbackBind`, `LoopbackBind`, `EphemeralAddr`, `ValidPort`. |
| `process_unix.go` | The lsof-backed half: `Holders` and its `-F` parser, plus `Signal`, `Kill` (SIGTERM then SIGKILL after a grace) and the signal-0 `Alive`. |
| `process_other.go` | The `!unix` half: `Holders` and `Kill` return `ErrUnsupported`, `Signal` is a no-op, `Alive` uses `os.FindProcess`. |
| `net_test.go` | Table tests for every address mapping, a real-listener `IsServing` check, and the pre-flight `ProbeListen` on a busy address. |
| `process_unix_test.go` | `lsof` output parsing without lsof, discovery of a real listener, and the guarantee that `Signal` never targets its own process. |

## Invariants

- `ErrUnsupported` is not a dead end: discovery may be absent on a platform
  without `lsof`, or on a unix host where `lsof` is not installed, and the caller
  can still offer "start on another port" (`EphemeralAddr`, `ProbeListen`).
- `ProbeListen` is a pre-flight, not a bind: it binds and closes immediately, so
  a success means the address was free at that instant and nothing more.
- `Alive` treats `EPERM` as alive — a process owned by someone else exists — and
  never reports pid 1.
- `DialAddr` maps unspecified hosts (`:port`, `0.0.0.0:port`, `[::]:port`) to
  loopback and returns empty for an address with no port, which `IsServing`
  reads as "not serving".
