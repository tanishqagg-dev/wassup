import SwiftUI
import AppKit

@main
struct WassupApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    var body: some Scene {
        MenuBarExtra {
            MenuBarView(model: AppModel.shared)
        } label: {
            MenuBarLabel(count: AppModel.shared.drafts.count)
        }.menuBarExtraStyle(.window)
    }
}

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate, NSWindowDelegate {
    private var setupWindow: NSWindow?
    private var startup: Task<Void, Never>?
    func applicationDidFinishLaunching(_ notification: Notification) {
        let arguments = ProcessInfo.processInfo.arguments
        if let index = arguments.firstIndex(of: "--snapshot") {
            guard arguments.indices.contains(index + 1) else {
                fputs("--snapshot requires an output directory\n", stderr)
                exit(2)
            }
            do { try SnapshotRenderer.render(to: URL(fileURLWithPath: arguments[index + 1])); exit(0) }
            catch { fputs("Snapshot failed: \(error.localizedDescription)\n", stderr); exit(1) }
        }
        NSApp.setActivationPolicy(.accessory)
        let model = AppModel.shared
        model.openSetup = { [weak self] in self?.showSetup() }
        model.closeSetup = { [weak self] in self?.setupWindow?.close() }
        model.prepareNotifications()
        startup = Task { await model.bootstrap() }
    }
    private func showSetup() {
        if AppModel.shared.step == .done { AppModel.shared.step = .welcome }
        NSApp.setActivationPolicy(.regular)
        if setupWindow == nil {
            let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 680, height: 580), styleMask: [.titled, .closable, .miniaturizable, .fullSizeContentView], backing: .buffered, defer: false)
            window.title = "wassup"
            window.titleVisibility = .hidden
            window.titlebarAppearsTransparent = true
            window.isMovableByWindowBackground = true
            window.isReleasedWhenClosed = false
            window.backgroundColor = NSColor(Palette.background)
            window.contentView = NSHostingView(rootView: SetupView(model: AppModel.shared).ignoresSafeArea())
            window.delegate = self
            window.center()
            setupWindow = window
        }
        setupWindow?.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
        if AppModel.shared.step == .link && !AppModel.shared.status.loggedIn { AppModel.shared.beginLogin() }
    }
    func windowWillClose(_ notification: Notification) {
        AppModel.shared.setupClosed()
        NSApp.setActivationPolicy(.accessory)
    }
    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { false }
    func applicationWillTerminate(_ notification: Notification) { startup?.cancel(); AppModel.shared.shutdown() }
    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool { showSetup(); return true }
}
