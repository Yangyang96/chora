import AppKit
import Foundation
import UniformTypeIdentifiers
import CryptoKit

// The native shell owns only the child it launched. All persistent-data operations
// go through the CLI's compatibility checks and exclusive data ownership lock.
final class ChoraApplication: NSObject, NSApplicationDelegate {
    private let files = FileManager.default
    private let resources = Bundle.main.resourceURL!
    private let support: URL = {
        if let custom = ProcessInfo.processInfo.environment["CHORA_DESKTOP_SUPPORT_DIR"], custom.hasPrefix("/"), custom != "/" {
            return URL(fileURLWithPath: custom, isDirectory: true)
        }
        return FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0].appendingPathComponent("Chora", isDirectory: true)
    }()
    private var data: URL {
        if let path = try? String(contentsOf: support.appendingPathComponent("data-location.txt"), encoding: .utf8), path.hasPrefix("/") { return URL(fileURLWithPath: path, isDirectory: true) }
        return support.appendingPathComponent("data", isDirectory: true)
    }
    private var binary: URL { resources.appendingPathComponent("bin/chora") }
    private var child: Process?
    private var statusItem: NSStatusItem!
    private var url: URL?
    private var token = UUID().uuidString
    private var runtime: URL?
    private var busy = false
    private var stopping = false
    private var logHandle: FileHandle?
    private var authentication: Authentication?

    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.accessory)
        if let other = NSRunningApplication.runningApplications(withBundleIdentifier: Bundle.main.bundleIdentifier ?? "org.chora.desktop").first(where: { $0.processIdentifier != ProcessInfo.processInfo.processIdentifier }) {
            other.activate(options: [])
            NSApp.terminate(nil)
            return
        }
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        statusItem.button?.title = "Chora"
        let menu = NSMenu()
        for (title, action) in [("Open Workbench", #selector(openWorkbench)), ("Set Up Model Authentication…", #selector(authenticate)), ("Back Up Data…", #selector(backup)), ("Restore Backup…", #selector(restore)), ("Check for Updates…", #selector(checkUpdates)), ("Install Downloaded Update…", #selector(update)), ("Open Logs", #selector(openLogs)), ("Uninstall Information", #selector(uninstall)), ("Quit Chora…", #selector(quit))] {
            let item = NSMenuItem(title: title, action: action, keyEquivalent: "")
            item.target = self
            menu.addItem(item)
        }
        statusItem.menu = menu
        prepareRuntime()
    }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool { openWorkbench(); return false }
    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        if busy || authentication != nil { quit(); return .terminateCancel }
        if child?.isRunning == true { quit(); return .terminateCancel }
        return .terminateNow
    }

    private func prepareRuntime() {
        guard !busy else { return }
        busy = true; statusItem.button?.title = "Chora…"
        DispatchQueue.global(qos: .userInitiated).async {
            do {
                try self.files.createDirectory(at: self.support, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
                let installed = try self.installRuntime()
                DispatchQueue.main.async {
                    self.runtime = installed; self.busy = false; self.statusItem.button?.title = "Chora"
                    do {
                        if !self.files.fileExists(atPath: self.data.path) { try self.firstRun() }
                        self.start()
                    } catch { self.showError(error) }
                }
            } catch {
                DispatchQueue.main.async { self.busy = false; self.statusItem.button?.title = "Chora"; self.showError(error) }
            }
        }
    }

    private func installRuntime() throws -> URL {
        let id = try String(contentsOf: resources.appendingPathComponent("runtime-id.txt"), encoding: .utf8).trimmingCharacters(in: .whitespacesAndNewlines)
        guard id.count == 64, id.allSatisfy({ $0.isHexDigit }) else { throw failure("The bundled runtime identity is invalid. Reinstall Chora.") }
        let manifestBytes = try Data(contentsOf: resources.appendingPathComponent("runtime-manifest.json"))
        guard digest(manifestBytes) == id, let manifest = try JSONSerialization.jsonObject(with: manifestBytes) as? [String: String] else { throw failure("The runtime manifest does not match its identity.") }
        let parent = support.appendingPathComponent("runtimes", isDirectory: true)
        let destination = parent.appendingPathComponent(id, isDirectory: true)
        if !files.fileExists(atPath: destination.path) {
            try files.createDirectory(at: parent, withIntermediateDirectories: true)
            let pending = parent.appendingPathComponent(".pending-" + UUID().uuidString)
            defer { try? files.removeItem(at: pending) }
            try files.copyItem(at: resources.appendingPathComponent("runtime"), to: pending)
            try verifyRuntime(pending, manifest: manifest)
            try files.moveItem(at: pending, to: destination)
        } else {
            try verifyRuntime(destination, manifest: manifest)
        }
        guard files.isExecutableFile(atPath: destination.appendingPathComponent("bin/node").path), files.isExecutableFile(atPath: destination.appendingPathComponent("bin/pi").path) else { throw failure("The managed Node.js/Pi runtime is incomplete. Open Logs and reinstall Chora.") }
        return destination
    }

    private func digest(_ data: Data) -> String { SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined() }
    private func verifyRuntime(_ root: URL, manifest: [String: String]) throws {
        try RuntimeIntegrity.verify(root, manifest: manifest)
    }

    private func firstRun() throws {
        let alert = NSAlert()
        alert.messageText = "Welcome to Chora"
        alert.informativeText = "Start with new data, or adopt data from an existing source installation. Migration backs up the source and uses its existing location, preserving worktree references. Stop the source installation first."
        alert.addButton(withTitle: "Start Fresh")
        alert.addButton(withTitle: "Migrate Existing Data…")
        alert.addButton(withTitle: "Cancel")
        NSApp.activate(ignoringOtherApps: true)
        switch alert.runModal() {
        case .alertFirstButtonReturn: try files.createDirectory(at: data, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        case .alertSecondButtonReturn:
            guard let source = chooseDirectory("Choose the existing Chora data directory") else { throw failure("Migration cancelled. Relaunch Chora to set up data.") }
            let snapshot = support.appendingPathComponent("before-migration-" + UUID().uuidString)
            try cli(["desktop", "adopt", "--source-data", source.path, "--output", snapshot.path])
            try source.path.write(to: support.appendingPathComponent("data-location.txt"), atomically: true, encoding: .utf8)
        default: throw failure("Setup cancelled. Relaunch Chora when ready.")
        }
    }

    private func environment() -> [String: String] {
        var env = ProcessInfo.processInfo.environment
        env["PATH"] = (runtime?.appendingPathComponent("bin").path ?? "") + ":/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin:/usr/local/bin"
        return env
    }

    private func start() {
        guard child?.isRunning != true, !busy else { return }
        token = UUID().uuidString
        let ready = support.appendingPathComponent("ready-" + UUID().uuidString + ".json")
        let log = support.appendingPathComponent("desktop.log")
        do {
            if !files.fileExists(atPath: log.path) { files.createFile(atPath: log.path, contents: nil, attributes: [.posixPermissions: 0o600]) }
            let handle = try FileHandle(forWritingTo: log)
            try handle.seekToEnd()
            logHandle = handle
            let process = Process()
            process.executableURL = binary
            process.arguments = ["workbench", "--source", resources.appendingPathComponent("workbench").path, "--data", data.path, "--isolated-helper", resources.appendingPathComponent("workbench/isolated-helper").path, "--port", "0", "--desktop-token", token, "--desktop-ready", ready.path]
            process.environment = environment()
            process.standardOutput = handle
            process.standardError = handle
            process.terminationHandler = { [weak self] _ in DispatchQueue.main.async {
                guard let self else { return }
                self.url = nil
                try? self.files.removeItem(at: ready)
                if !self.stopping { self.showError(self.failure("Chora stopped unexpectedly. Open Logs for details, then choose Open Workbench to retry.")) }
            } }
            try process.run()
            child = process
            waitForReady(ready, attempts: 150)
        } catch { showError(error) }
    }

    private func waitForReady(_ ready: URL, attempts: Int) {
        guard child?.isRunning == true else { return }
        if let bytes = try? Data(contentsOf: ready), let object = try? JSONSerialization.jsonObject(with: bytes) as? [String: Any], let address = object["url"] as? String, let candidate = URL(string: address), candidate.host == "127.0.0.1", object["pid"] as? Int == Int(child!.processIdentifier) {
            self.url = candidate
            status { [weak self] result in
                guard let self else { return }
                if result != nil { try? self.files.removeItem(at: ready); NSWorkspace.shared.open(candidate) }
                else { self.showError(self.failure("Chora started but its authenticated health check failed. Open Logs for details.")) }
            }
            return
        }
        guard attempts > 0 else { showError(failure("Chora did not become ready within 30 seconds. Open Logs for details.")); return }
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.2) { [weak self] in self?.waitForReady(ready, attempts: attempts - 1) }
    }

    private func status(_ completion: @escaping ([String: Any]?) -> Void) {
        guard let url else { completion(nil); return }
        var request = URLRequest(url: url.appendingPathComponent("desktop/status"))
        request.timeoutInterval = 5
        request.setValue("Bearer " + token, forHTTPHeaderField: "Authorization")
        URLSession.shared.dataTask(with: request) { bytes, response, _ in
            let object = (response as? HTTPURLResponse)?.statusCode == 200 ? bytes.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Any] } : nil
            DispatchQueue.main.async { completion(object) }
        }.resume()
    }

    @objc private func openWorkbench() {
        if let url { NSWorkspace.shared.open(url); return }
        if runtime == nil { prepareRuntime(); return }
        do {
            if !files.fileExists(atPath: data.path) { try firstRun() }
            start()
        } catch { showError(error) }
    }
    @objc private func openLogs() { NSWorkspace.shared.open(support.appendingPathComponent("desktop.log")) }
    @objc private func uninstall() { inform("Uninstall Chora", "Quit Chora and move Chora.app from Applications to the Trash. History, configuration, managed runtimes and Task worktrees are retained for reinstallation. Your original repositories and external tools are never removed.") }
    @objc private func authenticate() {
        guard let runtime, authentication == nil, !busy else { return }
        authentication = Authentication { [weak self] in self?.authentication = nil }
        do { try authentication?.start(runtime: runtime, script: resources.appendingPathComponent("auth.mjs"), environment: environment()) }
        catch { authentication = nil; showError(error) }
    }

    @objc private func quit() {
        guard !busy, authentication == nil else { inform("Operation in progress", "Finish or cancel authentication and wait for the current operation to finish."); return }
        guard child?.isRunning == true else { NSApp.terminate(nil); return }
        let alert = NSAlert()
        alert.messageText = "Quit Chora?"
        alert.informativeText = "Running tasks and previews will stop. Chora waits for a safe shutdown and retains task state. Closing the browser alone keeps Chora running."
        alert.addButton(withTitle: "Stop and Quit"); alert.addButton(withTitle: "Cancel")
        guard alert.runModal() == .alertFirstButtonReturn else { return }
        busy = true
        stop { [weak self] success in self?.busy = false; if success { NSApp.terminate(nil) } }
    }

    private func stop(_ completion: @escaping (Bool) -> Void) {
        guard let process = child, process.isRunning else { completion(true); return }
        stopping = true
        process.terminate()
        waitForStop(process, attempts: 450, completion: completion)
    }
    private func waitForStop(_ process: Process, attempts: Int, completion: @escaping (Bool) -> Void) {
        if !process.isRunning {
            child = nil; url = nil; stopping = false; try? logHandle?.close(); logHandle = nil
            let safe = process.terminationStatus == 0
            if !safe { showError(failure("Chora exited with incomplete cleanup. Open Logs and restart the workbench to reconcile retained task state before retrying.")) }
            completion(safe); return
        }
        if attempts == 0 { stopping = false; showError(failure("Chora is still shutting down. No process was forcibly killed. Wait, inspect Logs, then retry.")); completion(false); return }
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.2) { [weak self] in self?.waitForStop(process, attempts: attempts - 1, completion: completion) }
    }

    private func maintenance(_ operation: @escaping () throws -> Void, restart: Bool = true) {
        guard !busy, authentication == nil else { return }
        busy = true
        status { [weak self] state in
            guard let self else { return }
            guard self.child?.isRunning != true || state?["idle"] as? Bool == true else {
                self.busy = false; self.inform("Chora must be idle", "Stop all running tasks and previews before changing or backing up data. If status cannot be verified, open Logs and retry."); return
            }
            self.stop { success in
                guard success else { self.busy = false; return }
                DispatchQueue.global(qos: .userInitiated).async {
                    var error: Error?
                    do { try operation() } catch let caught { error = caught }
                    DispatchQueue.main.async {
                        self.busy = false
                        if let error { self.showError(error) }
                        if restart || error != nil { self.start() }
                    }
                }
            }
        }
    }

    @objc private func backup() {
        guard let directory = chooseDirectory("Choose a folder for the new backup") else { return }
        let target = directory.appendingPathComponent("chora-backup-" + UUID().uuidString)
        maintenance { try self.cli(["desktop", "backup", "--data", self.data.path, "--output", target.path]) }
    }
    @objc private func restore() {
        guard let source = chooseDirectory("Choose a Chora backup") else { return }
        let alert = NSAlert(); alert.messageText = "Restore this backup?"
        alert.informativeText = "Chora will stop, preserve the current data in a safety backup, then restore the selected snapshot. Repository working files are not rolled back."
        alert.addButton(withTitle: "Restore"); alert.addButton(withTitle: "Cancel")
        guard alert.runModal() == .alertFirstButtonReturn else { return }
        maintenance {
            let saved = self.support.appendingPathComponent("before-restore-" + UUID().uuidString)
            try self.cli(["desktop", "backup", "--data", self.data.path, "--output", saved.path])
            try self.cli(["desktop", "restore", "--data", self.data.path, "--backup", source.path])
        }
    }
    private func newerRelease(_ candidate: String, than installed: String) -> Bool {
        func parse(_ value: String) -> ([Int], [String])? {
            let pieces = value.split(separator: "+", maxSplits: 1)[0].split(separator: "-", maxSplits: 1)
            let core = pieces[0].split(separator: ".").compactMap { Int($0) }
            guard core.count == 3 else { return nil }
            return (core, pieces.count == 2 ? pieces[1].split(separator: ".").map(String.init) : [])
        }
        guard let lhs = parse(candidate), let rhs = parse(installed) else { return false }
        for (a, b) in zip(lhs.0, rhs.0) { if a != b { return a > b } }
        if lhs.1.isEmpty || rhs.1.isEmpty { return lhs.1.isEmpty && !rhs.1.isEmpty }
        for (a, b) in zip(lhs.1, rhs.1) {
            if a == b { continue }
            if let x = Int(a), let y = Int(b) { return x > y }
            if Int(a) != nil { return false }
            if Int(b) != nil { return true }
            return a > b
        }
        return lhs.1.count > rhs.1.count
    }

    @objc private func checkUpdates() {
        guard let endpoint = URL(string: "https://api.github.com/repos/Yangyang96/chora/releases?per_page=30") else { return }
        var request = URLRequest(url: endpoint)
        request.timeoutInterval = 15
        request.setValue("application/vnd.github+json", forHTTPHeaderField: "Accept")
        URLSession.shared.dataTask(with: request) { [weak self] bytes, response, _ in
            let releases = bytes.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [[String: Any]] }
            let object = releases?.first(where: { release in
                guard release["draft"] as? Bool != true, let assets = release["assets"] as? [[String: Any]] else { return false }
                return assets.contains { asset in
                    guard let name = asset["name"] as? String else { return false }
                    return name.hasPrefix("chora-") && name.hasSuffix("-macos-arm64.dmg")
                }
            })
            DispatchQueue.main.async {
                guard let self else { return }
                guard (response as? HTTPURLResponse)?.statusCode == 200, let release = object?["tag_name"] as? String else {
                    self.inform("No release information available", "A published release could not be found. Check your connection or try again later."); return
                }
                let installed = Bundle.main.object(forInfoDictionaryKey: "ChoraReleaseVersion") as? String ?? "unknown"
                let version = release.hasPrefix("v") ? String(release.dropFirst()) : release
                guard self.newerRelease(version, than: installed) else {
                    self.inform("Chora is up to date", "Installed version: " + installed); return
                }
                let alert = NSAlert(); alert.messageText = "Chora " + release + " is available"
                alert.informativeText = "Installed version: " + installed + ". Download the Apple Silicon macOS application, then choose Install Downloaded Update from the Chora menu. Installation verifies its signature and notarization, waits for idle work, and backs up data."
                alert.addButton(withTitle: "Open Downloads"); alert.addButton(withTitle: "Later")
                if alert.runModal() == .alertFirstButtonReturn {
                    NSWorkspace.shared.open(URL(string: "https://github.com/Yangyang96/chora/releases")!)
                }
            }
        }.resume()
    }
    @objc private func update() {
        let panel = NSOpenPanel(); panel.title = "Choose a downloaded, signed Chora.app update"; panel.canChooseDirectories = false; panel.allowedContentTypes = [.applicationBundle]
        guard panel.runModal() == .OK, let candidate = panel.url else { return }
        let current = Bundle.main.bundleURL
        maintenance({
            try self.cli(["desktop", "verify-update", "--app", candidate.path, "--current-app", current.path])
            let snapshot = self.support.appendingPathComponent("before-update-" + UUID().uuidString)
            try self.cli(["desktop", "backup", "--data", self.data.path, "--output", snapshot.path])
            let parent = current.deletingLastPathComponent()
            let pending = parent.appendingPathComponent(".Chora-pending-" + UUID().uuidString + ".app")
            let previous = parent.appendingPathComponent("Chora-previous-" + UUID().uuidString + ".app")
            defer { try? self.files.removeItem(at: pending) }
            try self.files.copyItem(at: candidate, to: pending)
            try self.cli(["desktop", "verify-update", "--app", pending.path, "--current-app", current.path])
            try self.files.moveItem(at: current, to: previous)
            do { try self.files.moveItem(at: pending, to: current) }
            catch { try? self.files.moveItem(at: previous, to: current); throw error }
            DispatchQueue.main.async {
                self.inform("Update installed", "Chora will now quit. Open Chora again from Applications. The previous application and data backup are retained for recovery. If startup fails, move the previous application back into place and use Restore Backup.\n\nData backup: " + snapshot.path)
                NSApp.terminate(nil)
            }
        }, restart: false)
    }

    private func cli(_ arguments: [String]) throws {
        // Files, rather than pipes, avoid a child blocking on a full stdout/stderr buffer.
        let output = support.appendingPathComponent("operation-" + UUID().uuidString + ".log")
        files.createFile(atPath: output.path, contents: nil, attributes: [.posixPermissions: 0o600])
        let handle = try FileHandle(forWritingTo: output)
        defer { try? handle.close() }
        let process = Process(); process.executableURL = binary; process.arguments = arguments; process.environment = environment()
        process.standardOutput = handle; process.standardError = handle
        try process.run(); process.waitUntilExit()
        guard process.terminationStatus == 0 else { throw failure("The operation failed without proceeding. Inspect the operation log:\n" + output.path + "\n\n" + ((try? String(contentsOf: output, encoding: .utf8)) ?? "").suffix(3000)) }
    }
    private func chooseDirectory(_ title: String) -> URL? {
        let panel = NSOpenPanel(); panel.title = title; panel.canChooseFiles = false; panel.canChooseDirectories = true; panel.canCreateDirectories = true
        NSApp.activate(ignoringOtherApps: true)
        return panel.runModal() == .OK ? panel.url : nil
    }
    private func failure(_ text: String) -> NSError { NSError(domain: "Chora", code: 1, userInfo: [NSLocalizedDescriptionKey: text]) }
    private func showError(_ error: Error) { inform("Chora needs attention", error.localizedDescription) }
    private func inform(_ title: String, _ text: String) { let alert = NSAlert(); alert.messageText = title; alert.informativeText = text; NSApp.activate(ignoringOtherApps: true); alert.runModal() }
}

let application = NSApplication.shared
let delegate = ChoraApplication()
application.delegate = delegate
application.run()
