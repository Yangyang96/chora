import Foundation
import CryptoKit

@main
struct RuntimeIntegrityTests {
    static func main() throws {
        let files = FileManager.default
        let base = files.temporaryDirectory.appendingPathComponent("chora-integrity-" + UUID().uuidString)
        try files.createDirectory(at: base, withIntermediateDirectories: true)
        defer { try? files.removeItem(at: base) }
        // Match the launcher's resolved support URL followed by derived child URLs.
        let root = base.resolvingSymlinksInPath().appendingPathComponent(".pending-9A4D2DE")
        try files.createDirectory(at: root, withIntermediateDirectories: true)
        let license = root.appendingPathComponent("LICENSE")
        let bytes = Data("runtime license fixture".utf8)
        let hash = SHA256.hash(data: bytes).map { String(format: "%02x", $0) }.joined()
        let manifest = ["LICENSE": hash]
        try bytes.write(to: license)
        try RuntimeIntegrity.verify(root, manifest: manifest)
        try RuntimeIntegrity.verify(URL(fileURLWithPath: root.path, isDirectory: true), manifest: manifest)
        let link = root.appendingPathComponent("license-link")
        try files.createSymbolicLink(atPath: link.path, withDestinationPath: "LICENSE")
        try RuntimeIntegrity.verify(root, manifest: manifest)
        try files.removeItem(at: link)
        try files.createSymbolicLink(atPath: link.path, withDestinationPath: "../outside")
        try bytes.write(to: base.appendingPathComponent("outside"))
        try rejected("escaping symlink") { try RuntimeIntegrity.verify(root, manifest: manifest) }
        try files.removeItem(at: link)
        try Data("changed".utf8).write(to: license)
        try rejected("modified file") { try RuntimeIntegrity.verify(root, manifest: manifest) }
        try files.removeItem(at: license)
        try rejected("missing file") { try RuntimeIntegrity.verify(root, manifest: manifest) }
        try bytes.write(to: license)
        try bytes.write(to: root.appendingPathComponent("unexpected"))
        try rejected("unexpected file") { try RuntimeIntegrity.verify(root, manifest: manifest) }
        print("Runtime integrity: alias paths and intact files pass; modified, missing, extra files and escaping symlinks fail")
    }
    static func rejected(_ name: String, _ operation: () throws -> Void) throws {
        do { try operation() } catch { return }
        throw NSError(domain: "RuntimeIntegrityTests", code: 1, userInfo: [NSLocalizedDescriptionKey: "Accepted " + name])
    }
}
