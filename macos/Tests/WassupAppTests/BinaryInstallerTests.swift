import Foundation
import Testing
@testable import WassupApp

struct BinaryInstallerTests {
    /// Fixtures live under macos/.build. They never discover or invoke the real wassup CLI.
    private func fixture() throws -> URL {
        let macos = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        let directory = macos.appendingPathComponent(".build/verification/\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        return directory
    }
    private func helper(at url: URL, version: String, running: Bool = false) throws {
        let script = """
        #!/bin/sh
        printf '%s\\n' "$0" >> "$WASSUP_HOME/invocations"
        case "$1 $2" in
          'version --json') printf '%s\\n' '{"ok":true,"data":{"version":"\(version)"}}' ;;
          'daemon status') printf '%s\\n' '{"ok":true,"data":{"running":\(running)}}' ;;
          'daemon restart') printf restart > "$WASSUP_HOME/restarted"; printf '%s\\n' '{"ok":true,"data":{"running":true}}' ;;
          *) printf '%s\\n' '{"ok":false,"error":{"code":"fixture","message":"Unexpected command"}}'; exit 2 ;;
        esac
        """
        try script.write(to: url, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: url.path)
    }
    @Test func installsAndPreservesSameVersion() async throws {
        let directory = try fixture()
        defer { try? FileManager.default.removeItem(at: directory) }
        let bundled = directory.appendingPathComponent("bundled")
        try helper(at: bundled, version: "1.0.0")
        let cli = try await BinaryInstaller.install(helper: bundled, home: directory, environment: ["WASSUP_HOME": directory.path], version: "1.0.0")
        #expect(cli.binary.path == directory.appendingPathComponent("bin/wassup").path)
        let before = try FileManager.default.attributesOfItem(atPath: cli.binary.path)[.modificationDate] as? Date
        _ = try await BinaryInstaller.install(helper: bundled, home: directory, environment: cli.environment, version: "1.0.0")
        let after = try FileManager.default.attributesOfItem(atPath: cli.binary.path)[.modificationDate] as? Date
        #expect(before == after)
        let invocations = try String(contentsOf: directory.appendingPathComponent("invocations"), encoding: .utf8).split(separator: "\n")
        #expect(!invocations.isEmpty)
        #expect(invocations.allSatisfy { $0 == cli.binary.path })
        #expect(FileManager.default.isExecutableFile(atPath: cli.binary.path))
        #expect(!FileManager.default.fileExists(atPath: directory.appendingPathComponent("restarted").path))
    }
    @Test func replacesChangedVersionAndRestartsRunningFixture() async throws {
        let directory = try fixture()
        defer { try? FileManager.default.removeItem(at: directory) }
        let bundled = directory.appendingPathComponent("bundled")
        try helper(at: bundled, version: "1.0.0", running: true)
        let environment = ["WASSUP_HOME": directory.path]
        _ = try await BinaryInstaller.install(helper: bundled, home: directory, environment: environment, version: "1.0.0")
        try helper(at: bundled, version: "2.0.0", running: true)
        let cli = try await BinaryInstaller.install(helper: bundled, home: directory, environment: environment, version: "2.0.0")
        let version = try await cli.call(["version"])
        #expect(version["version"].string == "2.0.0")
        #expect(FileManager.default.fileExists(atPath: directory.appendingPathComponent("restarted").path))
        let leftovers = try FileManager.default.contentsOfDirectory(atPath: directory.appendingPathComponent("bin").path)
        #expect(leftovers == ["wassup"])
    }
}
