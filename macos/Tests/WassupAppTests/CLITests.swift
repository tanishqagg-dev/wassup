import Testing
import Foundation
@testable import WassupApp

struct CLITests {
    @Test func decodesSuccessAndStructuredError() throws {
        let success = ProcessOutput(stdout: Data("{\"ok\":true,\"data\":{\"logged_in\":true,\"push_name\":\"Alex\"}}\n".utf8), stderr: Data(), status: 0)
        let data = try CLI.decode(success)
        #expect(data["logged_in"].bool)
        #expect(data["push_name"].string == "Alex")
        let failure = ProcessOutput(stdout: Data(), stderr: Data("{\"ok\":false,\"error\":{\"code\":\"timeout\",\"message\":\"Code expired\",\"hint\":\"Try again\"}}\n".utf8), status: 7)
        #expect(throws: CLIError(code: "timeout", message: "Code expired", hint: "Try again")) { try CLI.decode(failure) }
    }

    @Test func rejectsSuccessEnvelopeOnFailedExit() {
        let output = ProcessOutput(stdout: Data("{\"ok\":true,\"data\":{}}\n".utf8), stderr: Data(), status: 9)
        #expect(throws: CLIError.self) { try CLI.decode(output) }
    }

    @Test func streamingHandlesSplitUTF8AndFinalLineWithoutNewline() async throws {
        let script = "printf '{\"type\":\"qr\",\"code\":\"'; printf 'hello\"}\\n'; printf '{\"ok\":true,\"data\":{\"linked\":true}}'"
        let cli = CLI(binary: URL(fileURLWithPath: "/bin/sh"), environment: [:])
        var lines: [JSONValue] = []
        for try await value in cli.stream(["-c", script]) { lines.append(value) }
        #expect(lines.count == 2)
        #expect(lines[0]["code"].string == "hello")
        #expect(lines[1]["data"]["linked"].bool)
    }

    @Test func streamPropagatesEnvelopeError() async {
        let cli = CLI(binary: URL(fileURLWithPath: "/bin/sh"), environment: [:])
        do {
            for try await _ in cli.stream(["-c", "printf '%s\\n' '{\"ok\":false,\"error\":{\"code\":\"timeout\",\"message\":\"Expired\"}}'; exit 7"]) { }
            Issue.record("Expected the structured stream error")
        } catch let error as CLIError { #expect(error.code == "timeout"); #expect(error.message == "Expired") }
        catch { Issue.record("Unexpected error: \(error)") }
    }

    @Test func drainsLargeStderrWithoutDeadlocking() async throws {
        let output = try await ProcessExecution.run(url: URL(fileURLWithPath: "/bin/sh"), arguments: ["-c", "dd if=/dev/zero bs=1024 count=1024 >&2 2>/dev/null; printf done"], environment: ["PATH": "/usr/bin:/bin"], timeout: 5)
        #expect(output.status == 0)
        #expect(String(decoding: output.stdout, as: UTF8.self) == "done")
        #expect(output.stderr.count == 262_144)
    }

    @Test func cancellationTerminatesChild() async throws {
        let start = Date()
        let task = Task { try await ProcessExecution.run(url: URL(fileURLWithPath: "/bin/sleep"), arguments: ["30"], environment: [:]) }
        try await Task.sleep(for: .milliseconds(100))
        task.cancel()
        do { _ = try await task.value; Issue.record("Expected cancellation") }
        catch { #expect(error is CancellationError) }
        #expect(Date().timeIntervalSince(start) < 3)
    }

    @Test func timeoutIsBoundedAndFriendly() async {
        do {
            _ = try await ProcessExecution.run(url: URL(fileURLWithPath: "/bin/sleep"), arguments: ["30"], environment: [:], timeout: 0.1)
            Issue.record("Expected timeout")
        } catch let error as CLIError { #expect(error.code == "timeout") }
        catch { Issue.record("Unexpected error: \(error)") }
    }

    @Test @MainActor func serviceShapesAndQuoting() throws {
        for json in ["{\"service_installed\":true}", "{\"installed\":true}", "{\"installed\":\"/a/service.plist\"}"] {
            #expect(AppModel.isServiceInstalled(try JSONDecoder().decode(JSONValue.self, from: Data(json.utf8))))
        }
        #expect(!AppModel.isServiceInstalled(.object(["running": .bool(true)])))
        #expect(AppModel.shellQuote("a'b $HOME") == "'a'\\''b $HOME'")
        #expect(AppModel.appleScriptString("a\"b\\c") == "\"a\\\"b\\\\c\"")
    }

    @Test func datesAcceptFractionalSeconds() {
        #expect(Draft.date("2026-11-16T11:30:00.123456789Z") != nil)
        #expect(Draft.date("2026-11-16T11:30:00Z") != nil)
    }
}
