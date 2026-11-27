# `Features/Events` — the raw event inspector

`EventsView` shows the bus as a dense table, one line per event:

```
时间 · 类型 · 来源 · 目标 · 摘要
```

- **Search** filters on type / worker / target / summary text.
- **Click a type tag** to filter to it; click again (or the x chip) to clear.
- The right pane is an inspector: id/time/worker/target/request/trace plus the
  pretty-printed payload, with a **调用链** button that switches to trace scope.

## Trace scope

`AppModel.eventsFilterTrace` is set by:
- a talk row's 调用链 link,
- the inspector's 调用链 button.

While set, a banner at the top says the list is trace-scoped and offers 清除.

The type→colour rule encodes *outcome* (reason = purple, failed = red,
completed = green, request/tool = amber, else grey), matching the WebUI.
