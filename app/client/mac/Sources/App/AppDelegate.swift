import AppKit
import Foundation

/// App lifecycle. The important job: keep the process out of App Nap so the
/// long-lived SSE connection and its callbacks keep running even when the
/// window is not the frontmost one. A live client must not be suspended by the
/// system for being idle.
final class AppDelegate: NSObject, NSApplicationDelegate {
    private var activity: NSObjectProtocol?

    func applicationDidFinishLaunching(_ notification: Notification) {
        activity = ProcessInfo.processInfo.beginActivity(
            options: [.userInitiated, .idleSystemSleepDisabled, .latencyCritical],
            reason: "niq keeps the live event stream connected"
        )
        NSApp.activate(ignoringOtherApps: true)
    }

    func applicationWillTerminate(_ notification: Notification) {
        if let activity { ProcessInfo.processInfo.endActivity(activity) }
        activity = nil
    }
}
