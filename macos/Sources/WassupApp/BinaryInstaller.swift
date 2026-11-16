import Foundation
import Darwin

enum BinaryInstaller {
    static func install(helper: URL, home: URL, environment: [String: String], version: String) async throws -> CLI {
        let fm = FileManager.default
        guard fm.isExecutableFile(atPath: helper.path) else {
            throw CLIError(code: "missing_helper", message: "The command line tool is missing.", hint: "Reinstall wassup from the complete app bundle.")
        }
        let destination = home.appendingPathComponent("bin/wassup")
        let stable = CLI(binary: destination, environment: environment)
        // build.sh records the CLI version in Info.plist, so even version queries never
        // execute the helper from the bundle. Every invocation uses the stable path.
        guard !version.isEmpty else { throw CLIError(code: "version", message: "Couldn't check the bundled tool's version.") }
        let exists = fm.isExecutableFile(atPath: destination.path)
        let previousVersion = exists ? (try? await stable.call(["version"])["version"].string) : nil
        if previousVersion == version { return stable }
        let wasRunning: Bool
        if exists { wasRunning = (try? await stable.call(["daemon", "status"])["running"].bool) ?? false }
        else { wasRunning = false }
        try fm.createDirectory(at: destination.deletingLastPathComponent(), withIntermediateDirectories: true)
        let temp = destination.deletingLastPathComponent().appendingPathComponent(".wassup-\(UUID().uuidString)")
        defer { try? fm.removeItem(at: temp) }
        try fm.copyItem(at: helper, to: temp)
        try fm.setAttributes([.posixPermissions: 0o755], ofItemAtPath: temp.path)
        // POSIX rename replaces atomically on the same filesystem, including a previous symlink.
        guard rename(temp.path, destination.path) == 0 else { throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO) }
        let installedVersion = try await stable.call(["version"])["version"].string
        guard installedVersion == version else { throw CLIError(code: "version", message: "The installed tool's version doesn't match the app.", hint: "Reinstall the complete wassup app.") }
        if wasRunning { _ = try await stable.call(["daemon", "restart"], timeout: 45) }
        return stable
    }
}
