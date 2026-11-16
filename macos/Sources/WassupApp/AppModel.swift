import AppKit
import Observation
import SwiftUI

struct AccountStatus: Sendable {
    var loggedIn = false
    var connected = false
    var name = ""
    var phone = ""
    var state = ""
    init() {}
    init(_ value: JSONValue) {
        loggedIn = value["logged_in"].bool
        connected = value["connected"].bool
        name = value["push_name"].string
        phone = value["phone"].string
        state = value["state"].string
    }
    var displayName: String { name.isEmpty ? (phone.isEmpty ? "Your WhatsApp" : phone) : name }
}

struct Draft: Identifiable, Sendable {
    let id: Int
    var summary: String
    var preview: String
    var reason: String
    var source: String
    var expires: Date?
    init(id: Int, summary: String, preview: String, reason: String = "", source: String = "", expires: Date? = nil) {
        self.id = id; self.summary = summary; self.preview = preview; self.reason = reason; self.source = source; self.expires = expires
    }
    init(_ value: JSONValue) {
        id = value["id"].int
        summary = value["summary"].string
        preview = value["preview"].string
        reason = value["reason"].string
        source = value["source"].string
        expires = Self.date(value["expires"].string)
    }
    static func date(_ string: String) -> Date? {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return formatter.date(from: string) ?? ISO8601DateFormatter().date(from: string)
    }
    func expiry(at now: Date) -> String {
        guard let expires else { return "Waiting for your approval" }
        let seconds = Int(expires.timeIntervalSince(now))
        if seconds <= 0 { return "Expired" }
        if seconds < 60 { return "Expires in \(seconds)s" }
        return "Expires in \(Int(ceil(Double(seconds) / 60))) min"
    }
}

enum SetupStep: Int, CaseIterable {
    case welcome, link, running, connect, safety, done
    var shortTitle: String { ["Welcome", "Link", "Keep running", "AI apps", "Approvals", "Ready"][rawValue] }
}

enum ApprovalMode: String, CaseIterable, Identifiable {
    case always, new = "new_chats_and_groups", never = "none"
    var id: String { rawValue }
    var argument: String { switch self { case .always: "always"; case .new: "new"; case .never: "never" } }
    var title: String { switch self { case .always: "Ask me first"; case .new: "Ask for new chats and groups"; case .never: "Don't ask" } }
    var detail: String { switch self { case .always: "You approve every message before it sends."; case .new: "Familiar one-to-one chats can send without asking."; case .never: "Your AI can send messages without your approval." } }
    var symbol: String { switch self { case .always: "hand.raised"; case .new: "person.2"; case .never: "bolt" } }
}

struct AIApp: Identifiable {
    let id: String
    let name: String
    let path: String?
    let symbol: String
    var found = false
    var connected = false
    var connecting = false
    static let defaults: [AIApp] = [
        AIApp(id: "claude-desktop", name: "Claude Desktop", path: "/Applications/Claude.app", symbol: "sparkle"),
        AIApp(id: "claude-code", name: "Claude Code", path: nil, symbol: "terminal"),
        AIApp(id: "codex", name: "Codex", path: "/Applications/Codex.app", symbol: "curlybraces"),
        AIApp(id: "cursor", name: "Cursor", path: "/Applications/Cursor.app", symbol: "cursorarrow")
    ]
}

@Observable @MainActor
final class AppModel {
    static let shared = AppModel()
    var status = AccountStatus()
    var drafts: [Draft] = []
    var step: SetupStep = .welcome
    var error: CLIError?
    var refreshError: CLIError?
    var busy = false
    var ready = false
    var startAtLogin = true
    var serviceInstalled = false
    var apps = AIApp.defaults
    var approval: ApprovalMode = .always
    var approvalLoaded = false
    var remoteEnabled = false
    var phoneMode = false
    var phoneNumber = ""
    var qrCode = ""
    var qrExpires = Date()
    var qrLifetime = 60.0
    var pairCode = ""
    var loginRunning = false
    var resolving: Set<Int> = []
    var resolved: Set<Int> = []
    var notificationNote = ""
    var snapshotMode = false
    var openSetup: (() -> Void)?
    var closeSetup: (() -> Void)?
    @ObservationIgnored var cli: CLI?
    @ObservationIgnored private var loginTask: Task<Void, Never>?
    @ObservationIgnored private var setupTask: Task<Void, Never>?
    @ObservationIgnored private var eventTask: Task<Void, Never>?
    @ObservationIgnored private var pollTask: Task<Void, Never>?
    @ObservationIgnored private var loginGeneration = UUID()
    @ObservationIgnored private var refreshing = false
    @ObservationIgnored private var notifications: Notifications?
    @ObservationIgnored private var pendingNotificationActions: [(id: Int, approve: Bool, binaryPath: String?)] = []
    @ObservationIgnored private var preferences: UserDefaults = .standard

    func bootstrap() async {
        guard !ready, !busy else { return }
        busy = true
        defer { busy = false }
        do {
            let runtime = await RuntimeEnvironment.resolve()
            if ProcessInfo.processInfo.environment["WASSUP_HOME"] != nil {
                // Test and custom homes do not share first-run state with the default account.
                preferences = UserDefaults(suiteName: "dev.wassup.app.\(runtime.home.path.utf8.map { String(format: "%02x", $0) }.joined())") ?? .standard
            }
            let helper = Bundle.main.bundleURL.appendingPathComponent("Contents/Helpers/wassup")
            guard let version = Bundle.main.object(forInfoDictionaryKey: "WassupCLIVersion") as? String else {
                throw CLIError(code: "version", message: "The app's version information is missing.", hint: "Reinstall the complete wassup app.")
            }
            cli = try await BinaryInstaller.install(helper: helper, home: runtime.home, environment: runtime.values, version: version)
            ready = true
            let pending = pendingNotificationActions
            pendingNotificationActions = []
            for action in pending { handleNotificationAction(action.id, approve: action.approve, binaryPath: action.binaryPath) }
            await refresh()
            if !status.loggedIn || !preferences.bool(forKey: "setupFinished") { openSetup?() }
            startMonitoring()
        } catch {
            self.error = Self.friendly(error)
            openSetup?()
        }
    }

    func prepareNotifications() {
        if notifications == nil { notifications = Notifications(model: self) }
    }

    func handleNotificationAction(_ id: Int, approve: Bool, binaryPath: String?) {
        guard let cli else {
            pendingNotificationActions.append((id, approve, binaryPath))
            return
        }
        guard binaryPath == nil || binaryPath == cli.binary.path else {
            refreshError = CLIError(code: "notification_home", message: "This approval belongs to another wassup setup.", hint: "Open that setup to review it.")
            return
        }
        Task { await resolve(id, approve: approve) }
    }

    func refresh() async {
        guard let cli, !refreshing else { return }
        refreshing = true
        defer { refreshing = false }
        do {
            async let account = cli.call(["status"])
            async let waiting = cli.call(["drafts"])
            let (accountValue, draftValues) = try await (account, waiting)
            status = AccountStatus(accountValue)
            let incoming = draftValues.array.filter { $0["status"].string == "pending" }.map(Draft.init).filter { $0.id > 0 }
            let incomingIDs = Set(incoming.map(\.id))
            // A resolution event can arrive before the approve call returns. Keep that card
            // until its result has been shown, instead of racing the success animation.
            let finishing = drafts.filter { resolving.contains($0.id) && !incomingIDs.contains($0.id) }
            drafts = incoming + finishing
            refreshError = nil
        } catch is CancellationError { }
        catch { refreshError = Self.friendly(error) }
    }

    private func startMonitoring() {
        guard let cli else { return }
        pollTask = Task { [weak self] in
            while !Task.isCancelled {
                do { try await Task.sleep(for: .seconds(15)) } catch { return }
                await self?.refresh()
            }
        }
        eventTask = Task { [weak self] in
            var backoff = 1.0
            while !Task.isCancelled {
                let started = Date()
                do {
                    for try await event in cli.stream(["events", "--type", "draft.created,draft.resolved,connection"]) {
                        guard let self, !Task.isCancelled else { return }
                        if event["type"].string == "draft.created" { self.notifications?.post(event["data"]) }
                        await self.refresh()
                    }
                } catch is CancellationError { return }
                catch { self?.refreshError = Self.friendly(error) }
                if Date().timeIntervalSince(started) > 30 { backoff = 1 }
                do { try await Task.sleep(for: .seconds(backoff)) } catch { return }
                backoff = min(backoff * 2, 30)
            }
        }
    }

    func advance() {
        guard !busy, ready || snapshotMode else { return }
        error = nil
        switch step {
        case .welcome: step = .link; beginLogin()
        case .link:
            guard status.loggedIn else { return }
            cancelLogin(); step = .running
            setupTask = Task { await readService() }
        case .running:
            setupTask = Task {
                busy = true
                defer { busy = false }
                do {
                    guard let cli else { return }
                    if startAtLogin {
                        let existing = try await cli.call(["daemon", "status"])
                        if !Self.isServiceInstalled(existing) {
                            do { _ = try await cli.call(["daemon", "install"], timeout: 45) }
                            catch {
                                // Some CLI versions report an already-installed service as an error.
                                let status = try await cli.call(["daemon", "status"])
                                guard Self.isServiceInstalled(status) else { throw error }
                            }
                        }
                        let result = try await cli.call(["daemon", "status"])
                        serviceInstalled = Self.isServiceInstalled(result)
                        if !result["running"].bool { _ = try await cli.call(["daemon", "start"], timeout: 30) }
                    }
                    try Task.checkCancellation()
                    step = .connect
                    await detectApps()
                } catch is CancellationError { }
                catch { self.error = Self.friendly(error) }
            }
        case .connect:
            step = .safety
            setupTask = Task { await readApproval() }
        case .safety:
            setupTask = Task {
                busy = true
                defer { busy = false }
                do {
                    guard let cli, approvalLoaded else { return }
                    _ = try await cli.call(["approval", approval.argument])
                    try Task.checkCancellation()
                    preferences.set(true, forKey: "setupFinished")
                    step = .done
                    notificationNote = await notifications?.requestPermission() ?? ""
                } catch is CancellationError { }
                catch { self.error = Self.friendly(error) }
            }
        case .done: closeSetup?()
        }
    }

    func back() {
        guard !busy, step.rawValue > 0 else { return }
        cancelLogin()
        setupTask?.cancel()
        error = nil
        step = SetupStep(rawValue: step.rawValue - 1) ?? .welcome
        if step == .link { beginLogin() }
    }

    func setupClosed() {
        cancelLogin()
        setupTask?.cancel()
    }

    func beginLogin() {
        cancelLogin()
        guard let cli, !snapshotMode else { return }
        let generation = loginGeneration
        qrCode = ""; pairCode = ""; error = nil
        loginTask = Task {
            loginRunning = true
            defer { if generation == loginGeneration { loginRunning = false } }
            do {
                let existing = try await cli.call(["status"])
                guard generation == loginGeneration, !Task.isCancelled else { return }
                status = AccountStatus(existing)
                if status.loggedIn { return }
                if phoneMode && phoneNumber.isEmpty { return }
                var args = ["login"]
                if phoneMode {
                    let phone = phoneNumber.filter { !$0.isWhitespace && $0 != "-" && $0 != "(" && $0 != ")" }
                    guard phone.first == "+", phone.dropFirst().allSatisfy(\.isNumber), (8...15).contains(phone.dropFirst().count) else {
                        throw CLIError(code: "phone", message: "Add your phone number with its country code.", hint: "For example, +919876543210.")
                    }
                    args += ["--phone", phone]
                }
                var linked = false
                for try await value in cli.stream(args) {
                    guard generation == loginGeneration, !Task.isCancelled else { return }
                    switch value["type"].string {
                    case "qr":
                        qrLifetime = Double(max(1, value["expires_in"].int))
                        qrExpires = Date().addingTimeInterval(qrLifetime)
                        qrCode = value["code"].string
                    case "pair_code": pairCode = value["code"].string
                    default:
                        if value["ok"].bool && value["data"]["linked"].bool {
                            let data = value["data"]
                            status.loggedIn = true; status.connected = data["connected"].bool
                            status.name = data["push_name"].string; status.phone = data["phone"].string
                            linked = true
                        }
                    }
                }
                if !linked { throw CLIError(code: "login_ended", message: "The link request ended.", hint: "Try again to get a fresh code.") }
            } catch is CancellationError { }
            catch {
                guard generation == loginGeneration else { return }
                if let cliError = error as? CLIError, cliError.code == "already_logged_in" {
                    if let value = try? await cli.call(["status"]), generation == loginGeneration { status = AccountStatus(value) }
                }
                if !status.loggedIn { self.error = Self.friendly(error) }
            }
        }
    }

    func cancelLogin() {
        loginGeneration = UUID()
        loginTask?.cancel(); loginTask = nil; loginRunning = false
    }

    func switchLoginMode() {
        cancelLogin(); phoneMode.toggle(); qrCode = ""; pairCode = ""; error = nil
        if !phoneMode { beginLogin() }
    }

    private func readService() async {
        guard let cli else { return }
        if let value = try? await cli.call(["daemon", "status"]) { serviceInstalled = Self.isServiceInstalled(value) }
    }
    static func isServiceInstalled(_ value: JSONValue) -> Bool {
        value["service_installed"].bool || value["installed"].bool || !value["installed"].string.isEmpty
    }

    func detectApps() async {
        guard let cli else { return }
        busy = true
        defer { busy = false }
        do {
            let values = try await cli.call(["connect"])
            try Task.checkCancellation()
            for row in values.array {
                if let i = apps.firstIndex(where: { $0.id == row["target"].string }) {
                    apps[i].found = row["found"].bool || apps[i].path.map { FileManager.default.fileExists(atPath: $0) } == true
                    if !row["connected"].isNull { apps[i].connected = row["connected"].bool }
                }
                if row["target"].string == "claude-web" { remoteEnabled = row["remote_enabled"].bool }
            }
        } catch is CancellationError { }
        catch { self.error = Self.friendly(error) }
    }

    func connect(_ id: String) {
        guard let i = apps.firstIndex(where: { $0.id == id }), !apps[i].connecting, let cli else { return }
        apps[i].connecting = true; error = nil
        Task {
            defer { apps[i].connecting = false }
            do { _ = try await cli.call(["connect", id]); apps[i].connected = true }
            catch { self.error = Self.friendly(error) }
        }
    }

    func readApproval() async {
        guard let cli else { return }
        busy = true; approvalLoaded = false
        defer { busy = false }
        do {
            let value = try await cli.call(["approval"])
            try Task.checkCancellation()
            guard let mode = ApprovalMode(rawValue: value["approval"].string) else {
                throw CLIError(code: "approval", message: "Couldn't read your approval setting.", hint: "Try loading it again before continuing.")
            }
            approval = mode; approvalLoaded = true
        } catch is CancellationError { }
        catch { self.error = Self.friendly(error) }
    }

    func resolve(_ id: Int, approve: Bool) async {
        guard let cli, !resolving.contains(id), !resolved.contains(id) else { return }
        resolving.insert(id)
        defer { resolving.remove(id) }
        do {
            var args = ["drafts", approve ? "approve" : "reject", String(id)]
            if approve { args += ["--yes"] }
            _ = try await cli.call(args, timeout: 90)
            resolved.insert(id)
            notifications?.remove(id)
            try? await Task.sleep(for: .milliseconds(650))
            drafts.removeAll { $0.id == id }
            resolved.remove(id)
            await refresh()
        } catch { refreshError = Self.friendly(error) }
    }

    func openRemoteSetup() {
        guard let cli else { return }
        let env = cli.environment["WASSUP_HOME"].map { "WASSUP_HOME=\(Self.shellQuote($0)) " } ?? ""
        let command = "export PATH=\(Self.shellQuote(cli.environment["PATH"] ?? "/usr/bin:/bin")); " + env + Self.shellQuote(cli.binary.path) + " remote setup"
        runAppleScript("tell application \"Terminal\"\nactivate\ndo script \(Self.appleScriptString(command))\nend tell")
    }

    func installCommandLineTool() {
        guard let cli else { return }
        let command = "mkdir -p /usr/local/bin && ln -sfn \(Self.shellQuote(cli.binary.path)) /usr/local/bin/wassup"
        runAppleScript("do shell script \(Self.appleScriptString(command)) with administrator privileges")
    }

    private func runAppleScript(_ source: String) {
        Task {
            let failure = await Task.detached(priority: .userInitiated) { () -> String? in
                var details: NSDictionary?
                guard let script = NSAppleScript(source: source) else { return "Couldn't prepare the macOS action." }
                script.executeAndReturnError(&details)
                guard let details else { return Optional<String>.none }
                if (details[NSAppleScript.errorNumber] as? Int) == -128 { return Optional<String>.none }
                return details[NSAppleScript.errorMessage] as? String ?? "macOS couldn't complete that action."
            }.value
            if let failure {
                let error = CLIError(code: "automation", message: "macOS couldn't complete that action.", hint: failure)
                refreshError = error; self.error = error
            }
        }
    }
    static func shellQuote(_ string: String) -> String { "'" + string.replacingOccurrences(of: "'", with: "'\\''") + "'" }
    static func appleScriptString(_ string: String) -> String { "\"" + string.replacingOccurrences(of: "\\", with: "\\\\").replacingOccurrences(of: "\"", with: "\\\"").replacingOccurrences(of: "\n", with: "\\n").replacingOccurrences(of: "\r", with: "\\r") + "\"" }
    static func friendly(_ error: any Error) -> CLIError {
        (error as? CLIError) ?? CLIError(code: "app", message: error.localizedDescription, hint: "Try again in a moment.")
    }
    func shutdown() { cancelLogin(); setupTask?.cancel(); eventTask?.cancel(); pollTask?.cancel() }
}
