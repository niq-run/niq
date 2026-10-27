# Using niq · cheatsheet

> One page. Full guide: `program__load("using-niq")`.

## Three concepts

| Concept | What it is | In one line |
|---|---|---|
| **Worker** | A unit that can do things | subscribe → process → publish; the only extension point |
| **Program** | Source code of a capability | `instruction` (always on) / `playbook` (on demand); prompt or script |
| **Event** | The communication language | no session, no loop; the loop emerges from the topology |

## Three questions before acting

| Question | Tool |
|---|---|
| Who is here? | `directory__list_workers` / `directory__search_workers` |
| What can it do? | `directory__get_worker_info` / `<worker>__worker_readme` |
| How do I talk to it? | tool call / `request` / `message` |

## Three ways to communicate

| Way | When |
|---|---|
| Tool call | the capability is already in your `tools` |
| `request` | you know the worker + event, but it is not in your tools |
| `message` | async note, or hand over work it should take at its own pace |

Peer tools are `<worker>__<event>`, dots become underscores: `workspace__bash`,
`program__load`, `timer__elapse`, `history__history_list`. Your own tools are
unprefixed: `message`, `request`, `context_compress`, `context_rotate`,
`program_query`, `program_update`, `worker_readme_update`.

## Worker types

| Type | Intelligent? | Purpose |
|---|---|---|
| `reason`, `coding` | ✅ | reasoning, decisions, collaboration |
| `workspace` | ❌ | file I/O, command execution |
| `timer` | ❌ | scheduling, reminders, tool timeouts |
| `program` | ❌ | program store: search / load / run / edit |
| `host` | ❌ | worker lifecycle: spawn / suspend / resume |
| `directory` | ❌ | the roster: list / search / capability info |
| `history` | ❌ | past events from the persisted bus |
| `mcp` | ❌ | external tool bridge |
| `niw` | ❌ | bridge to another niq instance |
| `hiw` | ❌ | the human at the WebUI |

**Natural language to a non-reason worker = silence.**

## Why multiple reason workers

- A reason worker's context is finite: a longer transcript means dilution, cost
  and mistakes.
- People are the same: nobody holds every concern at once — they divide and
  collaborate.
- **One focus area, one reason worker**; collaboration stitches the pieces back
  into a whole.
- Split on demand, not for the sake of splitting.

## Quick decision

```
Need reasoning                          → yourself
Need files / commands                   → workspace
Need scheduling / reminders             → timer
Need programs (find / load / run)       → program
Need to create / stop workers           → host
Need to know who is here                → directory
Need to recall what already happened    → history
Need external tools                     → mcp
Need another thinking agent             → host__spawn a reason worker
Need another niq instance               → niw
Need the human                          → hiw / ask the user
```

## Seven principles

1. Understand the goal before acting
2. Discover before acting — never guess
3. Targeted over broadcast
4. Reuse before creating
5. Leave a trace for important decisions
6. Ask when unsure
7. Stay restrained

## First run

1. Read the instructions and the project context
2. `directory__list_workers` + read the readmes of the workers your goal touches
3. Confirm goal and boundary with the user in one sentence
4. Start from the smallest verifiable step
5. Turn rules into programs; put data in the workspace
