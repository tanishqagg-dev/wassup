import Foundation
import Darwin

/// Value types cross process and actor boundaries. No untyped dictionaries escape this layer.
enum JSONValue: Codable, Sendable, Equatable {
    case object([String: JSONValue]), array([JSONValue]), string(String), number(Double), bool(Bool), null

    init(from decoder: any Decoder) throws {
        let c = try decoder.singleValueContainer()
        if c.decodeNil() { self = .null }
        else if let v = try? c.decode(Bool.self) { self = .bool(v) }
        else if let v = try? c.decode(Double.self) { self = .number(v) }
        else if let v = try? c.decode(String.self) { self = .string(v) }
        else if let v = try? c.decode([JSONValue].self) { self = .array(v) }
        else { self = .object(try c.decode([String: JSONValue].self)) }
    }
    func encode(to encoder: any Encoder) throws {
        var c = encoder.singleValueContainer()
        switch self {
        case .object(let v): try c.encode(v)
        case .array(let v): try c.encode(v)
        case .string(let v): try c.encode(v)
        case .number(let v): try c.encode(v)
        case .bool(let v): try c.encode(v)
        case .null: try c.encodeNil()
        }
    }
    subscript(_ key: String) -> JSONValue { if case .object(let v) = self { v[key] ?? .null } else { .null } }
    var string: String { if case .string(let v) = self { v } else { "" } }
    var bool: Bool { if case .bool(let v) = self { v } else { false } }
    var int: Int { if case .number(let v) = self, v.isFinite, v >= Double(Int.min), v < Double(Int.max) { Int(v) } else { 0 } }
    var array: [JSONValue] { if case .array(let v) = self { v } else { [] } }
    var isNull: Bool { self == .null }
}

struct CLIError: LocalizedError, Sendable, Equatable {
    var code: String
    var message: String
    var hint: String = ""
    var errorDescription: String? { message + (hint.isEmpty ? "" : "\n" + hint) }
    static func from(_ value: JSONValue) -> CLIError {
        let error = value["error"]
        return CLIError(code: error["code"].string,
                        message: error["message"].string.isEmpty ? "wassup couldn't finish that." : error["message"].string,
                        hint: error["hint"].string)
    }
}

struct ProcessOutput: Sendable {
    let stdout: Data
    let stderr: Data
    let status: Int32
}

/// The only unchecked Sendable type. Lifecycle fields are locked; pipe reads have one owner each.
/// Blocking pipe work runs on dedicated dispatch workers, never the main actor or Swift executor.
final class ProcessExecution: @unchecked Sendable {
    private let lock = NSLock()
    private var process: Process?
    private var cancelled = false
    private var timedOut = false
    private var finished = false
    private var stderrData = Data()

    func cancel(timeout: Bool = false) {
        lock.lock()
        guard !finished else { lock.unlock(); return }
        cancelled = true
        timedOut = timedOut || timeout
        let running = process
        if let running, running.isRunning { running.terminate() }
        lock.unlock()
        // Go normally handles SIGTERM. Bound cancellation if a child ignores it.
        DispatchQueue.global().asyncAfter(deadline: .now() + 1) { [self] in
            lock.lock()
            defer { lock.unlock() }
            if !finished, let process, process.isRunning { kill(process.processIdentifier, SIGKILL) }
        }
    }

    func start(url: URL, arguments: [String], environment: [String: String], timeout: TimeInterval?,
               streaming: Bool = false, line: (@Sendable (Data) -> Void)? = nil,
               completion: @escaping @Sendable (Result<ProcessOutput, any Error>) -> Void) {
        DispatchQueue.global(qos: .userInitiated).async { [self] in
            let child = Process()
            let out = Pipe(), err = Pipe()
            child.executableURL = url
            child.arguments = arguments
            child.environment = environment
            child.standardInput = FileHandle.nullDevice
            child.standardOutput = out
            child.standardError = err
            lock.lock()
            if cancelled {
                finished = true
                lock.unlock()
                completion(.failure(CancellationError()))
                return
            }
            do {
                process = child
                try child.run()
                lock.unlock()
            } catch {
                finished = true
                lock.unlock()
                completion(.failure(error))
                return
            }
            let deadline = DispatchWorkItem { [self] in cancel(timeout: true) }
            if let timeout { DispatchQueue.global().asyncAfter(deadline: .now() + timeout, execute: deadline) }
            let stderrDone = DispatchGroup()
            stderrDone.enter()
            DispatchQueue.global().async { [self] in
                // Keep draining stderr even after the diagnostic buffer is full.
                var data = Data()
                while true {
                    let chunk = err.fileHandleForReading.availableData
                    if chunk.isEmpty { break }
                    if data.count < 262_144 { data.append(chunk.prefix(262_144 - data.count)) }
                }
                lock.lock(); stderrData = data; lock.unlock()
                stderrDone.leave()
            }
            var output = Data(), pending = Data()
            var tooLarge = false
            while true {
                let chunk = out.fileHandleForReading.availableData
                if chunk.isEmpty { break }
                if !streaming, output.count < 8_388_608 { output.append(chunk) }
                pending.append(chunk)
                while let newline = pending.firstIndex(of: 10) {
                    let data = Data(pending[..<newline])
                    pending.removeSubrange(...newline)
                    if !data.isEmpty { line?(data) }
                }
                if pending.count > 8_388_608 || (!streaming && output.count > 8_388_608) {
                    tooLarge = true
                    cancel()
                    break
                }
            }
            if !pending.isEmpty { line?(pending) }
            child.waitUntilExit()
            stderrDone.wait()
            deadline.cancel()
            lock.lock()
            finished = true
            let wasCancelled = cancelled, wasTimeout = timedOut, errors = stderrData
            lock.unlock()
            if tooLarge { completion(.failure(CLIError(code: "output_limit", message: "wassup returned too much data."))) }
            else if wasTimeout { completion(.failure(CLIError(code: "timeout", message: "That took longer than expected.", hint: "Try again in a moment."))) }
            else if wasCancelled { completion(.failure(CancellationError())) }
            else { completion(.success(ProcessOutput(stdout: output, stderr: errors, status: child.terminationStatus))) }
        }
    }

    static func run(url: URL, arguments: [String], environment: [String: String], timeout: TimeInterval = 30) async throws -> ProcessOutput {
        let execution = ProcessExecution()
        return try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { continuation in
                execution.start(url: url, arguments: arguments, environment: environment, timeout: timeout) {
                    continuation.resume(with: $0)
                }
            }
        } onCancel: { execution.cancel() }
    }
}

struct CLI: Sendable {
    let binary: URL
    let environment: [String: String]

    func call(_ arguments: [String], timeout: TimeInterval = 30) async throws -> JSONValue {
        let output = try await ProcessExecution.run(url: binary, arguments: arguments + ["--json"], environment: environment, timeout: timeout)
        return try Self.decode(output)
    }

    static func decode(_ output: ProcessOutput) throws -> JSONValue {
        let candidates = [output.stdout, output.stderr].flatMap { $0.split(separator: 10).reversed() }
        for candidate in candidates {
            guard let value = try? JSONDecoder().decode(JSONValue.self, from: Data(candidate)), !value["ok"].isNull else { continue }
            if !value["ok"].bool { throw CLIError.from(value) }
            if output.status == 0 { return value["data"] }
        }
        throw CLIError(code: "exit_\(output.status)", message: output.status == 0 ? "wassup returned an unexpected response." : "wassup couldn't finish that.",
                       hint: "Try again. If this keeps happening, reopen wassup.")
    }

    func stream(_ arguments: [String]) -> AsyncThrowingStream<JSONValue, any Error> {
        let execution = ProcessExecution()
        return AsyncThrowingStream(bufferingPolicy: .bufferingNewest(256)) { continuation in
            execution.start(url: binary, arguments: arguments + ["--json"], environment: environment, timeout: nil, streaming: true, line: { data in
                do {
                    let value = try JSONDecoder().decode(JSONValue.self, from: data)
                    if !value["ok"].isNull && !value["ok"].bool { continuation.finish(throwing: CLIError.from(value)); execution.cancel() }
                    else { continuation.yield(value) }
                } catch { continuation.finish(throwing: error); execution.cancel() }
            }, completion: { result in
                switch result {
                case .failure(let error): continuation.finish(throwing: error)
                case .success(let output):
                    if output.status == 0 { continuation.finish() }
                    else {
                        do { _ = try Self.decode(output); continuation.finish() }
                        catch { continuation.finish(throwing: error) }
                    }
                }
            })
            continuation.onTermination = { _ in execution.cancel() }
        }
    }
}

enum RuntimeEnvironment {
    static func resolve() async -> (home: URL, values: [String: String]) {
        var environment = ProcessInfo.processInfo.environment
        let rawHome = environment["WASSUP_HOME"].flatMap { $0.isEmpty ? nil : $0 }
        let home = rawHome.map { URL(fileURLWithPath: ($0 as NSString).expandingTildeInPath).standardizedFileURL }
            ?? FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent(".wassup")
        // Normalize an explicit override so the CLI, installer and Terminal all use the same folder.
        if rawHome != nil { environment["WASSUP_HOME"] = home.path }
        var loginPath = ""
        if let output = try? await ProcessExecution.run(url: URL(fileURLWithPath: "/bin/zsh"), arguments: ["-lc", "printf %s \"$PATH\""], environment: environment, timeout: 3), output.status == 0 {
            loginPath = String(decoding: output.stdout, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
        }
        let fallback = "/opt/homebrew/bin:/usr/local/bin:\(FileManager.default.homeDirectoryForCurrentUser.path)/.local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
        var seen = Set<String>()
        var paths = [home.appendingPathComponent("bin").path]
        paths.append(contentsOf: loginPath.components(separatedBy: ":"))
        paths.append(contentsOf: fallback.components(separatedBy: ":"))
        let uniquePaths = paths.filter { !$0.isEmpty && seen.insert($0).inserted }
        environment["PATH"] = uniquePaths.joined(separator: ":")
        environment["NO_COLOR"] = "1"
        return (home, environment)
    }
}
