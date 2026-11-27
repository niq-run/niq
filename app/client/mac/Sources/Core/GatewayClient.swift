import Foundation

private func gcLog(_ s: String) {
    guard ProcessInfo.processInfo.environment["NIQ_DEBUG"] == "1" else { return }
    FileHandle.standardError.write(Data(("[gc] " + s + "\n").utf8))
}

struct GatewayError: LocalizedError {
    let message: String
    var errorDescription: String? { message }
}

/// The HTTP half of a client: project catalog + one project's data port.
/// Same wire as the WebUI/TUI — a client only ever reaches a project through
/// the gateway.
struct GatewayClient {
    var base: URL

    private func url(_ path: String) -> URL {
        URL(string: path, relativeTo: base) ?? base
    }

    private func projectPath(_ id: String, _ suffix: String) -> String {
        "/p/\(id)/api\(suffix)"
    }

    @discardableResult
    private func data(for req: URLRequest) async throws -> Data {
        let (data, resp) = try await URLSession.shared.data(for: req)
        guard let http = resp as? HTTPURLResponse, http.statusCode < 300 else {
            let msg = String(data: data, encoding: .utf8) ?? "HTTP error"
            throw GatewayError(message: msg.trimmingCharacters(in: .whitespacesAndNewlines))
        }
        return data
    }

    private func post(_ path: String) async throws {
        var req = URLRequest(url: url(path))
        req.httpMethod = "POST"
        _ = try await data(for: req)
    }

    // MARK: Control plane (gateway)

    func listProjects() async throws -> [ProjectView] {
        let data = try await data(for: URLRequest(url: url("/api/projects")))
        return try JSONDecoder().decode([ProjectView].self, from: data)
    }
    func start(_ id: String) async throws { try await post("/api/projects/\(id)/start") }
    func stop(_ id: String) async throws { try await post("/api/projects/\(id)/stop") }
    func restart(_ id: String) async throws { try await post("/api/projects/\(id)/restart") }

    /// The model-provider config (control plane).
    func providers() async throws -> ProviderConfig {
        let data = try await data(for: URLRequest(url: url("/api/providers")))
        return try JSONDecoder().decode(ProviderConfig.self, from: data)
    }

    // MARK: Data plane (one project)

    func workers(_ project: String) async throws -> [Worker] {
        let data = try await data(for: URLRequest(url: url(projectPath(project, "/workers"))))
        return try JSONDecoder().decode([Worker].self, from: data)
    }

    /// Events older than `anchor`, newest first (as the endpoint returns them).
    func history(_ project: String, before anchor: String, limit: Int = 50) async throws -> [Event] {
        let escaped = anchor.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? anchor
        var root = base.absoluteString
        if root.hasSuffix("/") { root.removeLast() }
        let path = projectPath(project, "/events/before/\(escaped)")
        guard let target = URL(string: root + path + "?limit=\(limit)") else { return [] }
        gcLog("history url \(target.absoluteString)")
        let data = try await data(for: URLRequest(url: target))
        let trimmed = String(decoding: data, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
        if trimmed.isEmpty || trimmed == "null" { return [] }
        return try JSONDecoder().decode([Event].self, from: data)
    }

    func sendInput(_ project: String, body: InputBody) async throws {
        var req = URLRequest(url: url(projectPath(project, "/input")))
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONEncoder().encode(body)
        _ = try await data(for: req)
    }

    /// Ask a worker to stop what it is doing (`POST /p/{id}/api/abort`).
    func abort(_ project: String, target: String) async throws {
        var req = URLRequest(url: url(projectPath(project, "/abort")))
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONSerialization.data(withJSONObject: ["target": target])
        _ = try await data(for: req)
    }

    // MARK: Worker management

    /// `GET /p/{id}/api/workers/{wid}/providers` — a reason worker's selectable
    /// providers and its current choice.
    func workerProviders(_ project: String, _ workerID: String) async throws -> WorkerProviders {
        let path = projectPath(project, "/workers/\(workerID)/providers")
        let data = try await data(for: URLRequest(url: url(path)))
        return try JSONDecoder().decode(WorkerProviders.self, from: data)
    }

    /// `POST /p/{id}/api/workers/{wid}/provider` — switch provider/model.
    func setWorkerProvider(_ project: String, _ workerID: String,
                           provider: String, model: String) async throws {
        var req = URLRequest(url: url(projectPath(project, "/workers/\(workerID)/provider")))
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONSerialization.data(withJSONObject: ["provider": provider, "model": model])
        _ = try await data(for: req)
    }

    /// `POST /p/{id}/api/workers/{wid}/{start|stop|restart|suspend|resume}`.
    func workerAction(_ project: String, _ workerID: String, _ action: String) async throws {
        try await post(projectPath(project, "/workers/\(workerID)/\(action)"))
    }

    /// `POST /p/{id}/api/workers/{wid}/archived {archived}`.
    func setWorkerArchived(_ project: String, _ workerID: String, archived: Bool) async throws {
        var req = URLRequest(url: url(projectPath(project, "/workers/\(workerID)/archived")))
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONSerialization.data(withJSONObject: ["archived": archived])
        _ = try await data(for: req)
    }

    // MARK: Approvals

    /// `GET /p/{id}/api/approvals` — pending first, then decided history.
    func approvals(_ project: String) async throws -> [ApprovalEntry] {
        let data = try await data(for: URLRequest(url: url(projectPath(project, "/approvals"))))
        let trimmed = String(decoding: data, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
        if trimmed.isEmpty || trimmed == "null" { return [] }
        // The endpoint answers `{approvals:[...]}`; tolerate a bare array.
        if trimmed.hasPrefix("[") {
            return try JSONDecoder().decode([ApprovalEntry].self, from: data)
        }
        return try JSONDecoder().decode(ApprovalList.self, from: data).approvals ?? []
    }

    /// `POST /p/{id}/api/approvals/{id}/decision {approved, note}`.
    func decideApproval(_ project: String, _ approvalID: String, approved: Bool, note: String) async throws {
        var req = URLRequest(url: url(projectPath(project, "/approvals/\(approvalID)/decision")))
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONSerialization.data(withJSONObject: ["approved": approved, "note": note])
        _ = try await data(for: req)
    }

    // MARK: Uploads

    /// `POST /p/{id}/api/upload` — multipart, field name `file`.
    func upload(_ project: String, fileURL: URL) async throws -> UploadedFile {
        let boundary = "niq-\(UUID().uuidString)"
        let name = fileURL.lastPathComponent
        let contents = try Data(contentsOf: fileURL)

        var body = Data()
        func append(_ s: String) { body.append(Data(s.utf8)) }
        append("--\(boundary)\r\n")
        append("Content-Disposition: form-data; name=\"file\"; filename=\"\(name)\"\r\n")
        append("Content-Type: application/octet-stream\r\n\r\n")
        body.append(contents)
        append("\r\n--\(boundary)--\r\n")

        var req = URLRequest(url: url(projectPath(project, "/upload")))
        req.httpMethod = "POST"
        req.setValue("multipart/form-data; boundary=\(boundary)", forHTTPHeaderField: "Content-Type")
        req.httpBody = body
        req.timeoutInterval = 120
        let data = try await data(for: req)
        return try JSONDecoder().decode(UploadedFile.self, from: data)
    }

    /// The live event stream (SSE).
    ///
    /// The frame parser works on raw bytes, not `AsyncBytes.lines`: SSE frames
    /// are separated by a blank line, and relying on the line sequence to emit
    /// that empty line is brittle. We split on `\n`, treat an empty line as the
    /// frame boundary, strip a trailing `\r`, and process `event:` / `data:`
    /// fields. The first frame is the watermark (the newest event id at
    /// subscribe time); `onWatermark` reports it so the caller can page history.
    func stream(project: String, onWatermark: ((String) -> Void)? = nil) -> AsyncThrowingStream<Event, Error> {
        let streamURL = url(projectPath(project, "/stream"))
        return AsyncThrowingStream { continuation in
            let task = Task {
                do {
                    gcLog("start \(streamURL.absoluteString)")
                    var req = URLRequest(url: streamURL)
                    req.cachePolicy = .reloadIgnoringLocalCacheData
                    req.setValue("text/event-stream", forHTTPHeaderField: "Accept")
                    req.setValue("no-cache", forHTTPHeaderField: "Cache-Control")
                    let (bytes, resp) = try await URLSession.shared.bytes(for: req)
                    gcLog("status \((resp as? HTTPURLResponse)?.statusCode ?? -1)")
                    guard let http = resp as? HTTPURLResponse, http.statusCode < 300 else {
                        throw GatewayError(message: "stream HTTP \((resp as? HTTPURLResponse)?.statusCode ?? -1)")
                    }

                    let decoder = JSONDecoder()
                    var line = [UInt8]()
                    var eventName = ""
                    var dataLines: [String] = []
                    var frames = 0

                    func endFrame() {
                        if !dataLines.isEmpty {
                            let payload = dataLines.joined(separator: "\n")
                            dataLines.removeAll(keepingCapacity: true)
                            if eventName == "watermark" {
                                onWatermark?(payload)
                            } else if let event = try? decoder.decode(Event.self, from: Data(payload.utf8)) {
                                continuation.yield(event)
                            }
                            frames += 1
                            if frames <= 3 { gcLog("frame \(frames) event=\(eventName.isEmpty ? "-" : eventName)") }
                        }
                        eventName = ""
                    }

                    for try await byte in bytes {
                        if byte == 0x0A { // \n: end of one line
                            if line.last == 0x0D { line.removeLast() } // drop \r
                            let text = String(decoding: line, as: UTF8.self)
                            line.removeAll(keepingCapacity: true)
                            if text.isEmpty {
                                endFrame()
                            } else if text.hasPrefix(":") {
                                // heartbeat / comment
                            } else if text.hasPrefix("event:") {
                                var v = String(text.dropFirst(6))
                                if v.hasPrefix(" ") { v.removeFirst() }
                                eventName = v
                            } else if text.hasPrefix("data:") {
                                var v = String(text.dropFirst(5))
                                if v.hasPrefix(" ") { v.removeFirst() }
                                dataLines.append(v)
                            }
                        } else {
                            line.append(byte)
                        }
                    }
                    endFrame()
                    continuation.finish()
                } catch {
                    gcLog("error \(error)")
                    continuation.finish(throwing: error)
                }
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }
}

// MARK: - The remaining WebUI surfaces
//
// Everything below mirrors app/client/webui/assets/src/services/api.ts: the
// control plane's templates / providers / project creation, and the project's
// programs, worker management, and the generic bus "ask" (a worker event plus a
// poll of its terminal reply) that powers readme / MCP / context / mounts.

extension GatewayClient {
    // MARK: JSON helpers

    private func jsonRequest(_ path: String, method: String, body: Any? = nil) async throws -> Data {
        var req = URLRequest(url: url(path))
        req.httpMethod = method
        if let body {
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
            req.httpBody = try JSONSerialization.data(withJSONObject: body)
        }
        req.timeoutInterval = 60
        return try await data(for: req)
    }

    private func decode<T: Decodable>(_ type: T.Type, from data: Data) throws -> T {
        try JSONDecoder().decode(T.self, from: data)
    }

    private func jsonValue(_ path: String) async throws -> JSONValue {
        let data = try await data(for: URLRequest(url: url(path)))
        return try JSONDecoder().decode(JSONValue.self, from: data)
    }

    // MARK: Control plane — templates

    func templates() async throws -> [String] {
        let data = try await data(for: URLRequest(url: url("/api/templates")))
        return (try? decode([String].self, from: data)) ?? []
    }

    func template(_ name: String) async throws -> JSONValue {
        try await jsonValue("/api/templates/\(esc(name))")
    }

    func createTemplate(id: String, template: JSONValue) async throws {
        var object = template.object ?? [:]
        object.removeValue(forKey: "id")
        _ = try await jsonRequest("/api/templates", method: "POST",
                                  body: ["id": id, "template": object.mapValues { $0.anyValue }])
    }

    func updateTemplate(name: String, template: JSONValue) async throws {
        _ = try await jsonRequest("/api/templates/\(esc(name))", method: "PUT",
                                  body: template.anyValue)
    }

    func deleteTemplate(name: String) async throws {
        _ = try await jsonRequest("/api/templates/\(esc(name))", method: "DELETE")
    }

    func importTemplate(id: String, fileURL: URL) async throws {
        var form = MultipartBody()
        if !id.isEmpty { form.field("id", id) }
        form.file("file", fileURL)
        var req = URLRequest(url: url("/api/templates/import"))
        req.httpMethod = "POST"
        req.setValue(form.contentType, forHTTPHeaderField: "Content-Type")
        req.httpBody = form.close()
        req.timeoutInterval = 120
        _ = try await data(for: req)
    }

    // MARK: Control plane — providers / projects

    func providersRaw() async throws -> JSONValue {
        try await jsonValue("/api/providers")
    }

    func updateProviders(_ config: JSONValue) async throws {
        _ = try await jsonRequest("/api/providers", method: "PUT", body: config.anyValue)
    }

    func createProject(id: String, template: String) async throws {
        _ = try await jsonRequest("/api/projects", method: "POST", body: ["id": id, "template": template])
    }

    func linkProject(path: String) async throws -> ProjectView {
        let data = try await jsonRequest("/api/projects/link", method: "POST", body: ["path": path])
        return try decode(ProjectView.self, from: data)
    }

    struct DirEntry: Decodable, Hashable, Identifiable {
        var name: String
        var path: String
        var id: String { path }
    }

    func projectDirs(_ path: String?) async throws -> [DirEntry] {
        let suffix = path.map { "?path=\(esc($0))" } ?? ""
        let data = try await data(for: URLRequest(url: url("/api/projects/dirs\(suffix)")))
        return (try? decode([DirEntry].self, from: data)) ?? []
    }

    // MARK: Project — archived / worker management

    func archived(_ project: String) async throws -> [String] {
        let data = try await data(for: URLRequest(url: url(projectPath(project, "/archived"))))
        return (try? decode([String].self, from: data)) ?? []
    }

    func deleteWorker(_ project: String, _ workerID: String) async throws {
        _ = try await jsonRequest(projectPath(project, "/workers/\(esc(workerID))"), method: "DELETE")
    }

    func createWorker(_ project: String, body: [String: Any]) async throws -> JSONValue {
        let data = try await jsonRequest(projectPath(project, "/workers/create"), method: "POST", body: body)
        return try decode(JSONValue.self, from: data)
    }

    func updateWorkerMeta(_ project: String, _ workerID: String,
                          label: String, tags: [String], description: String) async throws {
        _ = try await jsonRequest(projectPath(project, "/workers/\(esc(workerID))/meta"), method: "PUT",
                                  body: ["label": label, "tags": tags, "description": description])
    }

    func updateWorkerParams(_ project: String, _ workerID: String, params: JSONValue) async throws {
        _ = try await jsonRequest(projectPath(project, "/workers/\(esc(workerID))/params"), method: "PUT",
                                  body: ["params": params.anyValue])
    }

    func updateWorkerAllow(_ project: String, _ workerID: String,
                           publish: [PublishPattern]?, subscribe: [EventPattern]?) async throws {
        var body: [String: Any] = [:]
        if let publish { body["publish_allow"] = publish.map { $0.dict } }
        if let subscribe { body["subscribe_allow"] = subscribe.map { $0.dict } }
        _ = try await jsonRequest(projectPath(project, "/workers/\(esc(workerID))/allow"), method: "PUT", body: body)
    }

    // MARK: Project — the bus ask path

    /// Publish an event to a worker as HIW and return the server's request id.
    func sendWorkerEvent(_ project: String, _ workerID: String,
                         type: String, payload: [String: Any]) async throws -> String {
        let data = try await jsonRequest(projectPath(project, "/workers/\(esc(workerID))/event"),
                                         method: "POST", body: ["type": type, "payload": payload])
        if let object = try? JSONDecoder().decode(JSONValue.self, from: data),
           let requestID = object.object?["request_id"]?.string {
            return requestID
        }
        return ""
    }

    func eventsByRequest(_ project: String, _ requestID: String) async throws -> [Event] {
        let data = try await data(for: URLRequest(url: url(projectPath(project, "/events/by-request/\(esc(requestID))"))))
        let trimmed = String(decoding: data, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
        if trimmed.isEmpty || trimmed == "null" { return [] }
        return (try? decode([Event].self, from: data)) ?? []
    }

    /// Drive a worker the way the WebUI does: publish an event it watches, then
    /// poll the by-request index for its terminal answer.
    func ask(_ project: String, _ workerID: String, type: String,
             payload: [String: Any] = [:], timeout: TimeInterval = 20) async throws -> AskReply {
        let requestID = try await sendWorkerEvent(project, workerID, type: type, payload: payload)
        guard !requestID.isEmpty else { throw GatewayError(message: "no request id for \(type)") }
        let deadline = Date().addingTimeInterval(timeout)
        var first = true
        while Date() < deadline {
            if !first {
                try await Task.sleep(nanoseconds: 250_000_000)
            }
            first = false
            if Task.isCancelled { throw CancellationError() }
            let events = (try? await eventsByRequest(project, requestID)) ?? []
            guard let reply = events.first(where: { $0.isToolResult }) else { continue }
            if reply.type == "request.completed" {
                if let result = reply.payload?["result"] {
                    return AskReply(ok: true, result: result, error: nil)
                }
                return AskReply(ok: true, result: reply.payload.map { JSONValue.object($0) }, error: nil)
            }
            let error = reply.payload?["error"]?.string
                ?? reply.payload?["reason"]?.string
                ?? "\(type) rejected"
            return AskReply(ok: false, result: nil, error: error)
        }
        throw GatewayError(message: "\(type) timed out")
    }

    func workerContext(_ project: String, _ workerID: String) async throws -> WorkerContextStatus {
        let data = try await data(for: URLRequest(url: url(projectPath(project, "/workers/\(esc(workerID))/context"))))
        return try decode(WorkerContextStatus.self, from: data)
    }

    func workerMounts(_ project: String, _ workerID: String) async throws -> MountList {
        let data = try await data(for: URLRequest(url: url(projectPath(project, "/workers/\(esc(workerID))/mounts"))))
        return try decode(MountList.self, from: data)
    }

    func mutateMount(_ project: String, _ workerID: String, add: Bool, path: String) async throws -> JSONValue {
        let data = try await jsonRequest(projectPath(project, "/workers/\(esc(workerID))/mounts/\(add ? "add" : "remove")"),
                                         method: "POST", body: ["path": path])
        return try decode(JSONValue.self, from: data)
    }

    // MARK: Project — programs

    func programs(_ project: String) async throws -> [ProgramInfo] {
        let data = try await data(for: URLRequest(url: url(projectPath(project, "/programs"))))
        return (try? decode([ProgramInfo].self, from: data)) ?? []
    }

    func program(_ project: String, _ name: String) async throws -> ProgramDetail {
        let data = try await data(for: URLRequest(url: url(projectPath(project, "/programs/\(esc(name))"))))
        return try decode(ProgramDetail.self, from: data)
    }

    func programContent(_ project: String, _ name: String, path: String) async throws -> ProgramContent {
        let data = try await data(for: URLRequest(url: url(projectPath(project, "/programs/\(esc(name))/content?path=\(esc(path))"))))
        return try decode(ProgramContent.self, from: data)
    }

    func updateProgram(_ project: String, _ name: String,
                       contentType: String?, description: String?, tags: [String]?, body: String) async throws {
        var payload: [String: Any] = ["body": body]
        if let contentType { payload["content_type"] = contentType }
        if let description { payload["description"] = description }
        if let tags { payload["tags"] = tags }
        _ = try await jsonRequest(projectPath(project, "/programs/\(esc(name))"), method: "PUT", body: payload)
    }

    func deleteProgram(_ project: String, _ name: String) async throws {
        _ = try await jsonRequest(projectPath(project, "/programs/\(esc(name))"), method: "DELETE")
    }

    private func esc(_ value: String) -> String {
        value.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? value
    }
}

// MARK: - Encoding helpers

extension JSONValue {
    /// Bridge to `JSONSerialization` for request bodies.
    var anyValue: Any {
        switch self {
        case .null: return NSNull()
        case .bool(let b): return b
        case .number(let n): return n
        case .string(let s): return s
        case .array(let a): return a.map { $0.anyValue }
        case .object(let o): return o.mapValues { $0.anyValue }
        }
    }
}

extension PublishPattern {
    var dict: [String: Any] {
        var out: [String: Any] = ["type": type]
        if let target { out["target"] = target }
        return out
    }
}

extension EventPattern {
    var dict: [String: Any] {
        var out: [String: Any] = ["type": type]
        if let source { out["source"] = source }
        return out
    }
}

/// A tiny multipart/form-data builder (template import).
struct MultipartBody {
    private let boundary = "niq-\(UUID().uuidString)"
    private(set) var data = Data()
    private var finished = false

    var contentType: String { "multipart/form-data; boundary=\(boundary)" }

    private mutating func write(_ s: String) { data.append(Data(s.utf8)) }

    mutating func field(_ name: String, _ value: String) {
        write("--\(boundary)\r\n")
        write("Content-Disposition: form-data; name=\"\(name)\"\r\n\r\n")
        write(value)
        write("\r\n")
    }

    mutating func file(_ name: String, _ url: URL) {
        guard let contents = try? Data(contentsOf: url) else { return }
        write("--\(boundary)\r\n")
        write("Content-Disposition: form-data; name=\"\(name)\"; filename=\"\(url.lastPathComponent)\"\r\n")
        write("Content-Type: application/octet-stream\r\n\r\n")
        data.append(contents)
        write("\r\n")
    }

    /// Closes the body. Called implicitly by `data` consumers is not possible,
    /// so callers read `closed`.
    mutating func close() -> Data {
        if !finished {
            finished = true
            write("--\(boundary)--\r\n")
        }
        return data
    }
}
