import Foundation
import CryptoKit

enum RuntimeIntegrity {
    private static func digest(_ data: Data) -> String { SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined() }
    private static func failure(_ text: String) -> NSError { NSError(domain: "Chora", code: 1, userInfo: [NSLocalizedDescriptionKey: text]) }
    static func verify(_ root: URL, manifest: [String: String]) throws {
        let files = FileManager.default
        let canonical = root.resolvingSymlinksInPath().standardizedFileURL.path + "/"
        guard let entries = files.enumerator(atPath: root.path) else { throw failure("Cannot inspect the managed runtime.") }
        var seen = Set<String>()
        // DirectoryEnumerator supplies paths relative to the requested root. URL
        // enumeration may expand a filesystem alias such as /tmp to /private/tmp.
        for case let relative as String in entries {
            let entry = root.appendingPathComponent(relative)
            let properties = try entry.resourceValues(forKeys: [.isRegularFileKey, .isSymbolicLinkKey])
            if properties.isSymbolicLink == true {
                guard entry.resolvingSymlinksInPath().standardizedFileURL.path.hasPrefix(canonical), files.fileExists(atPath: entry.path) else { throw failure("The managed runtime contains an unsafe or broken symlink.") }
            } else if properties.isRegularFile == true {
                guard let expected = manifest[relative], digest(try Data(contentsOf: entry, options: .mappedIfSafe)) == expected else { throw failure("Managed runtime integrity check failed: " + relative + ". Reinstall Chora after removing the damaged runtime directory.") }
                seen.insert(relative)
            }
        }
        guard seen == Set(manifest.keys) else { throw failure("The managed runtime is missing files. Reinstall Chora.") }
    }

}
