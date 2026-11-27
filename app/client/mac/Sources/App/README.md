# `Sources/App` — the executable entry (`@main`)

`NiqApp.swift` declares the SwiftUI `App` and its three scenes:

| Scene | What |
|---|---|
| `WindowGroup` | the main window (the WebUI reproduction), hosts `ContentView` |
| `MenuBarExtra` | the menu bar app's functionality — gateway up/down, per-project open/start/stop/restart, quit (drives the same `AppModel`) |
| `Settings` | app settings (gateway address, located `niq` binary) |

`MenuBarView` is the menu bar content. Everything here only wires scenes to the
shared `AppModel`; no business logic.
