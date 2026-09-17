import AppKit

// JSON-lines presentation for Pi's native authentication callbacks. Secrets only
// traverse anonymous pipes to Pi; neither Chora's database nor logs receive them.
final class Authentication: NSObject {
    private let process = Process()
    private let input = Pipe()
    private let output = Pipe()
    private var window: NSPanel!
    private var label: NSTextField!
    private var promptID: Int?
    private var promptAlert: NSAlert?
    private var buffer = Data()
    private var finished = false
    private let done: () -> Void

    init(done: @escaping () -> Void) { self.done = done }

    func start(runtime: URL, script: URL, environment: [String: String]) throws {
        window = NSPanel(contentRect: NSRect(x: 0, y: 0, width: 500, height: 150), styleMask: [.titled], backing: .buffered, defer: false)
        window.title = "Pi model authentication"
        label = NSTextField(wrappingLabelWithString: "Loading authentication methods from Pi…")
        label.frame = NSRect(x: 20, y: 55, width: 460, height: 75)
        label.isSelectable = true
        window.contentView?.addSubview(label)
        let cancel = NSButton(title: "Cancel", target: self, action: #selector(cancelLogin))
        cancel.frame = NSRect(x: 380, y: 15, width: 100, height: 30)
        window.contentView?.addSubview(cancel)
        window.center(); window.makeKeyAndOrderFront(nil); NSApp.activate(ignoringOtherApps: true)
        process.executableURL = runtime.appendingPathComponent("bin/node")
        process.arguments = [script.path, runtime.path]
        process.environment = environment
        process.standardInput = input
        process.standardOutput = output
        process.standardError = FileHandle.nullDevice
        output.fileHandleForReading.readabilityHandler = { [weak self] handle in
            let bytes = handle.availableData
            DispatchQueue.main.async { self?.receive(bytes) }
        }
        process.terminationHandler = { [weak self] _ in
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.1) {
                guard let self, !self.finished else { return }
                self.finish("Authentication did not complete. Pi manages any saved credentials. Check Runtime readiness and retry from the Chora menu if needed.")
            }
        }
        do { try process.run() } catch { finish(nil); throw error }
    }

    private func receive(_ bytes: Data) {
        guard !finished else { return }
        buffer.append(bytes)
        guard buffer.count <= 1_048_576 else { cancelLogin(); return }
        while let newline = buffer.firstIndex(of: 10) {
            let line = buffer[..<newline]
            buffer.removeSubrange(...newline)
            guard let event = try? JSONSerialization.jsonObject(with: line) as? [String: Any] else { cancelLogin(); return }
            handle(event)
        }
    }

    private func handle(_ event: [String: Any]) {
        switch event["event"] as? String {
        case "prompt": showPrompt(event)
        case "cancel_prompt":
            if promptID == event["id"] as? Int { NSApp.abortModal(); promptAlert?.window.orderOut(nil); promptID = nil }
        case "notice":
            switch event["type"] as? String {
            case "auth_url":
                label.stringValue = event["instructions"] as? String ?? "Complete authentication in your browser."
                openAuthURL(event["url"] as? String)
            case "device_code":
                label.stringValue = "Enter this code in your browser: " + (event["userCode"] as? String ?? "")
                openAuthURL(event["verificationUri"] as? String)
            default: label.stringValue = event["message"] as? String ?? "Waiting for Pi…"
            }
        case "complete": finish("Authentication saved by Pi. Return to the workbench and refresh Runtime readiness before starting a task.")
        case "cancelled": finish(nil)
        case "failed": finish("Pi authentication failed. Pi manages any saved credentials. Check Runtime readiness and retry from the Chora menu if needed.")
        default: cancelLogin()
        }
    }

    private func openAuthURL(_ text: String?) {
        guard let text, let url = URL(string: text), url.scheme == "https", url.host != nil else { return }
        NSWorkspace.shared.open(url)
    }

    private func showPrompt(_ event: [String: Any]) {
        guard let id = event["id"] as? Int, promptID == nil else { cancelLogin(); return }
        let alert = NSAlert()
        alert.messageText = event["message"] as? String ?? "Pi authentication"
        alert.addButton(withTitle: "Continue"); alert.addButton(withTitle: "Cancel")
        let type = event["type"] as? String
        let choices = event["options"] as? [[String: Any]] ?? []
        let select = NSPopUpButton(frame: NSRect(x: 0, y: 0, width: 400, height: 28))
        let field: NSTextField = type == "secret" || type == "manual_code" ? NSSecureTextField() : NSTextField()
        field.frame = NSRect(x: 0, y: 0, width: 400, height: 28)
        field.placeholderString = event["placeholder"] as? String
        if type == "select" {
            guard !choices.isEmpty else { cancelLogin(); return }
            AuthenticationChoices.populate(select, choices: choices)
            alert.accessoryView = select
        } else { alert.accessoryView = field }
        promptID = id; promptAlert = alert
        NSApp.activate(ignoringOtherApps: true)
        let result = alert.runModal()
        let stillPending = promptID == id
        promptID = nil; promptAlert = nil
        alert.window.orderOut(nil)
        guard stillPending, !finished else { return }
        guard result == .alertFirstButtonReturn else { cancelLogin(); return }
        let value = type == "select" ? AuthenticationChoices.value(select) : field.stringValue
        send(["id": id, "value": value])
        field.stringValue = ""
    }

    private func send(_ value: [String: Any]) {
        guard process.isRunning, let bytes = try? JSONSerialization.data(withJSONObject: value) else { return }
        do { try input.fileHandleForWriting.write(contentsOf: bytes + Data([10])) }
        catch { cancelLogin() }
    }

    @objc private func cancelLogin() {
        if process.isRunning { process.terminate() }
        finish(nil)
    }

    private func finish(_ message: String?) {
        guard !finished else { return }
        finished = true
        if promptID != nil { NSApp.abortModal(); promptAlert?.window.orderOut(nil); promptID = nil }
        output.fileHandleForReading.readabilityHandler = nil
        try? input.fileHandleForWriting.close()
        window?.orderOut(nil)
        if let message {
            let alert = NSAlert(); alert.messageText = "Pi authentication"; alert.informativeText = message
            alert.runModal()
        }
        done()
    }
}
