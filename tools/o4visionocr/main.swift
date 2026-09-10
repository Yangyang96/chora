import CryptoKit
import Darwin
import Foundation
import ImageIO
import Vision

private let protocolName = "chora.m1-o4-system-managed-ocr.v2"
private let bindingSchema = "chora.m1-o4-system-managed-ocr-engine-binding.v1"
private let outputSchema = "chora.m1-o4-system-managed-ocr-record.v2"
private let maxJSON = 4 * 1024 * 1024
private let maxPNG = 16 * 1024 * 1024

private struct PlatformBinding: Codable {
    let os: String
    let architecture: String
    let macOSProductVersion: String
    let macOSBuildVersion: String
    let darwinSysname: String
    let darwinRelease: String
    let darwinVersion: String
    let darwinMachine: String
}

private struct VisionBinding: Codable {
    let bundleIdentifier: String
    let bundleVersion: String
    let infoPlistSHA256: String
    let supportedRecognitionRevisions: [Int]
    let requestedRecognitionRevision: Int
    let recognitionLevel: String
    let recognitionLanguage: String
    let usesLanguageCorrection: Bool
    let confidenceThreshold: Double

    private enum CodingKeys: String, CodingKey {
        case bundleIdentifier, bundleVersion
        case infoPlistSHA256 = "infoPlistSha256"
        case supportedRecognitionRevisions, requestedRecognitionRevision, recognitionLevel
        case recognitionLanguage, usesLanguageCorrection, confidenceThreshold
    }
}

private struct ExecutableBinding: Codable {
    let pathSHA256: String
    let sha256: String

    private enum CodingKeys: String, CodingKey {
        case pathSHA256 = "pathSha256"
        case sha256
    }
}

private struct SandboxBinding: Codable {
    let executableFile: String
    let executablePathSHA256: String
    let executableSHA256: String
    let profileFile: String
    let profilePathSHA256: String
    let profileSHA256: String
    let networkRule: String

    private enum CodingKeys: String, CodingKey {
        case executableFile
        case executablePathSHA256 = "executablePathSha256"
        case executableSHA256 = "executableSha256"
        case profileFile
        case profilePathSHA256 = "profilePathSha256"
        case profileSHA256 = "profileSha256"
        case networkRule
    }
}

private struct EngineBinding: Codable {
    let schemaVersion: String
    let status: String
    let engine: String
    let modelBytes: String
    let platform: PlatformBinding
    let vision: VisionBinding
    let executable: ExecutableBinding
    let sandbox: SandboxBinding
    let bindingDigest: String
}

private struct OCRRecord: Codable {
    let schemaVersion: String
    let completed: Bool
    let pngSHA256: String
    let engineBindingSHA256: String
    let executableSHA256: String
    let visionBundleIdentifier: String
    let visionBundleVersion: String
    let visionInfoPlistSHA256: String
    let actualRecognitionRevision: Int
    let recognitionLevel: String
    let recognitionLanguage: String
    let usesLanguageCorrection: Bool
    let confidenceThreshold: Double
    let observationCount: Int
    let recognizedText: String
    let recognizedTextSHA256: String
    let minimumConfidence: Double
    let averageConfidence: Double
    let credentialMatches: Int
    let genericSecretMatches: Int
    let privatePathMatches: Int
    let emailMatches: Int
    let urlMatches: Int
    let modelBytes: String
    let networkDisabled: Bool
    let recordDigest: String
}

private enum Failure: Error {
    case invalid
}

private func execute() throws {
        let cli = try parseCLI(Array(CommandLine.arguments.dropFirst()))
        let bindingURL = try exactFileURL(cli["--engine-binding"]!)
        let inputURL = try exactFileURL(cli["--input"]!)
        let outputURL = try exactFileURL(cli["--output"]!)
        let bindingData = try boundedRegularFile(bindingURL, expectedMode: 0o400, maximum: maxJSON)
        let binding = try JSONDecoder().decode(EngineBinding.self, from: bindingData)
        try validateBindingShape(bindingData)
        try validateBinding(binding, rawData: bindingData)

        let png = try boundedRegularFile(inputURL, expectedMode: 0o400, maximum: maxPNG)
        guard png.count >= 20, png.prefix(8) == Data([137, 80, 78, 71, 13, 10, 26, 10]),
              FileManager.default.fileExists(atPath: outputURL.path) == false,
              let source = CGImageSourceCreateWithData(png as CFData, nil),
              let image = CGImageSourceCreateImageAtIndex(source, 0, nil) else { throw Failure.invalid }

        let request = VNRecognizeTextRequest()
        request.recognitionLevel = .accurate
        request.recognitionLanguages = ["en-US"]
        request.usesLanguageCorrection = true
        request.revision = binding.vision.requestedRecognitionRevision
        let handler = VNImageRequestHandler(cgImage: image, options: [:])
        try handler.perform([request])
        let actualRevision = Int(request.revision)
        guard actualRevision == binding.vision.requestedRecognitionRevision else { throw Failure.invalid }
        guard let observations = request.results, observations.isEmpty == false else { throw Failure.invalid }

        let ordered = observations.sorted {
            if abs($0.boundingBox.maxY - $1.boundingBox.maxY) > 0.000001 {
                return $0.boundingBox.maxY > $1.boundingBox.maxY
            }
            return $0.boundingBox.minX < $1.boundingBox.minX
        }
        var texts: [String] = []
        var confidences: [Double] = []
        for observation in ordered {
            guard let candidate = observation.topCandidates(1).first else { continue }
            let text = candidate.string.trimmingCharacters(in: .whitespacesAndNewlines)
            let confidence = Double(candidate.confidence)
            if text.isEmpty == false && confidence >= binding.vision.confidenceThreshold {
                texts.append(text)
                confidences.append(confidence)
            }
        }
        guard texts.isEmpty == false, confidences.count == texts.count else { throw Failure.invalid }
        let recognized = texts.joined(separator: "\n")
        guard recognized.utf8.count <= 64 * 1024 else { throw Failure.invalid }
        let counts = unsafeCounts(recognized)
        guard counts.allSatisfy({ $0 == 0 }) else { throw Failure.invalid }

        let bundle = Bundle(for: VNRecognizeTextRequest.self)
        let infoURL = try visionInfoPlistURL(bundle)
        let infoData = try Data(contentsOf: infoURL, options: [.mappedIfSafe])
        let average = confidences.reduce(0, +) / Double(confidences.count)
        var record: [String: Any] = [
            "schemaVersion": outputSchema,
            "completed": true,
            "pngSha256": sha256(png),
            "engineBindingSha256": sha256(bindingData),
            "executableSha256": binding.executable.sha256,
            "visionBundleIdentifier": bundle.bundleIdentifier!,
            "visionBundleVersion": bundleVersion(bundle),
            "visionInfoPlistSha256": sha256(infoData),
            "actualRecognitionRevision": actualRevision,
            "recognitionLevel": "accurate",
            "recognitionLanguage": "en-US",
            "usesLanguageCorrection": true,
            "confidenceThreshold": binding.vision.confidenceThreshold,
            "observationCount": texts.count,
            "recognizedText": recognized,
            "recognizedTextSha256": sha256(Data(recognized.utf8)),
            "minimumConfidence": confidences.min()!,
            "averageConfidence": average,
            "credentialMatches": counts[0],
            "genericSecretMatches": counts[1],
            "privatePathMatches": counts[2],
            "emailMatches": counts[3],
            "urlMatches": counts[4],
            "modelBytes": "opaque_unavailable",
            "networkDisabled": true,
        ]
        record["recordDigest"] = sha256(try canonicalJSON(record))
        let output = try JSONSerialization.data(withJSONObject: record, options: [.prettyPrinted, .sortedKeys]) + Data([10])
        try writeExclusiveReadonly(output, to: outputURL)
}

private func parseCLI(_ argv: [String]) throws -> [String: String] {
    let flags = ["--protocol", "--engine-binding", "--language", "--input", "--output", "--network"]
    guard argv.count == flags.count * 2 else { throw Failure.invalid }
    var result: [String: String] = [:]
    for index in flags.indices {
        guard argv[index * 2] == flags[index], result[flags[index]] == nil else { throw Failure.invalid }
        let value = argv[index * 2 + 1]
        guard value.isEmpty == false, value.utf8.contains(0) == false else { throw Failure.invalid }
        result[flags[index]] = value
    }
    guard result["--protocol"] == protocolName, result["--language"] == "eng",
          result["--network"] == "disabled" else { throw Failure.invalid }
    return result
}

private func validateBinding(_ binding: EngineBinding, rawData: Data) throws {
    guard binding.schemaVersion == bindingSchema, binding.status == "authorized",
          binding.engine == "system_managed_ocr_engine", binding.modelBytes == "opaque_unavailable",
          binding.platform.os == "darwin", binding.platform.architecture == "arm64",
          binding.vision.recognitionLevel == "accurate", binding.vision.recognitionLanguage == "en-US",
          binding.vision.usesLanguageCorrection,
          binding.vision.confidenceThreshold > 0, binding.vision.confidenceThreshold <= 1,
          binding.vision.supportedRecognitionRevisions.contains(binding.vision.requestedRecognitionRevision),
          binding.sandbox.networkRule == "deny network*" else { throw Failure.invalid }

    let system = try systemIdentity()
    guard binding.platform.macOSProductVersion == system.macOSProductVersion,
          binding.platform.macOSBuildVersion == system.macOSBuildVersion,
          binding.platform.darwinSysname == system.darwinSysname,
          binding.platform.darwinRelease == system.darwinRelease,
          binding.platform.darwinVersion == system.darwinVersion,
          binding.platform.darwinMachine == system.darwinMachine else { throw Failure.invalid }

    let executable = try executableURL()
    let executableData = try Data(contentsOf: executable, options: [.mappedIfSafe])
    guard binding.executable.pathSHA256 == sha256(Data(executable.path.utf8)),
          binding.executable.sha256 == sha256(executableData) else { throw Failure.invalid }

    let bundle = Bundle(for: VNRecognizeTextRequest.self)
    let infoData = try Data(contentsOf: visionInfoPlistURL(bundle), options: [.mappedIfSafe])
    let supported = VNRecognizeTextRequest.supportedRevisions.map { Int($0) }.sorted()
    guard binding.vision.bundleIdentifier == bundle.bundleIdentifier,
          binding.vision.bundleVersion == bundleVersion(bundle),
          binding.vision.infoPlistSHA256 == sha256(infoData),
          binding.vision.supportedRecognitionRevisions == supported else { throw Failure.invalid }

    let sandboxExecutable = try exactFileURL(binding.sandbox.executableFile)
    let sandboxProfile = try exactFileURL(binding.sandbox.profileFile)
    let sandboxBytes = try boundedRegularFile(sandboxExecutable, expectedMode: 0o755,
                                               maximum: 2 * 1024 * 1024, ownerUID: 0)
    let profileBytes = try boundedRegularFile(sandboxProfile, expectedMode: 0o400, maximum: maxJSON)
    let profileLines = String(data: profileBytes, encoding: .utf8)?
        .split(separator: "\n").map { $0.trimmingCharacters(in: .whitespaces) }
    guard binding.sandbox.executablePathSHA256 == sha256(Data(sandboxExecutable.path.utf8)),
          binding.sandbox.executableSHA256 == sha256(sandboxBytes),
          binding.sandbox.profilePathSHA256 == sha256(Data(sandboxProfile.path.utf8)),
          binding.sandbox.profileSHA256 == sha256(profileBytes),
          profileLines?.contains("(deny network*)") == true,
          profileLines?.contains(where: { $0.hasPrefix("(allow network") }) == false else {
        throw Failure.invalid
    }

    let object = try JSONSerialization.jsonObject(with: rawData)
    guard var dictionary = object as? [String: Any] else { throw Failure.invalid }
    dictionary.removeValue(forKey: "bindingDigest")
    guard binding.bindingDigest == sha256(try canonicalJSON(dictionary)) else { throw Failure.invalid }
}

private func validateExactJSONObject(_ data: Data, keys: [String]) throws {
    guard let object = try JSONSerialization.jsonObject(with: data) as? [String: Any],
          object.keys.sorted() == keys.sorted() else { throw Failure.invalid }
}

private func validateBindingShape(_ data: Data) throws {
    guard let root = try JSONSerialization.jsonObject(with: data) as? [String: Any] else { throw Failure.invalid }
    try exactKeys(root, ["schemaVersion", "status", "engine", "modelBytes", "platform", "vision",
                         "executable", "sandbox", "bindingDigest"])
    guard let platform = root["platform"] as? [String: Any],
          let vision = root["vision"] as? [String: Any],
          let executable = root["executable"] as? [String: Any],
          let sandbox = root["sandbox"] as? [String: Any] else { throw Failure.invalid }
    try exactKeys(platform, ["os", "architecture", "macOSProductVersion", "macOSBuildVersion",
                             "darwinSysname", "darwinRelease", "darwinVersion", "darwinMachine"])
    try exactKeys(vision, ["bundleIdentifier", "bundleVersion", "infoPlistSha256",
                           "supportedRecognitionRevisions", "requestedRecognitionRevision",
                           "recognitionLevel", "recognitionLanguage", "usesLanguageCorrection",
                           "confidenceThreshold"])
    try exactKeys(executable, ["pathSha256", "sha256"])
    try exactKeys(sandbox, ["executableFile", "executablePathSha256", "executableSha256",
                            "profileFile", "profilePathSha256", "profileSha256", "networkRule"])
}

private func exactKeys(_ object: [String: Any], _ keys: [String]) throws {
    guard object.keys.sorted() == keys.sorted() else { throw Failure.invalid }
}

private func systemIdentity() throws -> PlatformBinding {
    let plistURL = URL(fileURLWithPath: "/System/Library/CoreServices/SystemVersion.plist")
    let data = try Data(contentsOf: plistURL, options: [.mappedIfSafe])
    guard let plist = try PropertyListSerialization.propertyList(from: data, options: [], format: nil) as? [String: Any],
          let product = plist["ProductVersion"] as? String,
          let build = plist["ProductBuildVersion"] as? String else { throw Failure.invalid }
    var info = utsname()
    guard uname(&info) == 0 else { throw Failure.invalid }
    return PlatformBinding(os: "darwin", architecture: "arm64", macOSProductVersion: product,
                           macOSBuildVersion: build, darwinSysname: tupleString(info.sysname),
                           darwinRelease: tupleString(info.release), darwinVersion: tupleString(info.version),
                           darwinMachine: tupleString(info.machine))
}

private func tupleString<T>(_ tuple: T) -> String {
    withUnsafeBytes(of: tuple) { raw in
        let bytes = raw.prefix { $0 != 0 }
        return String(decoding: bytes, as: UTF8.self)
    }
}

private func executableURL() throws -> URL {
    var size: UInt32 = 0
    _NSGetExecutablePath(nil, &size)
    var buffer = [CChar](repeating: 0, count: Int(size))
    guard _NSGetExecutablePath(&buffer, &size) == 0 else { throw Failure.invalid }
    return try exactFileURL(String(cString: buffer))
}

private func exactFileURL(_ path: String) throws -> URL {
    guard path.hasPrefix("/"), path.utf8.contains(0) == false else { throw Failure.invalid }
    let url = URL(fileURLWithPath: path).standardizedFileURL
    guard url.path == path else { throw Failure.invalid }
    try rejectSymlinkAncestors(url)
    return url
}

private func boundedRegularFile(_ url: URL, expectedMode: mode_t, maximum: Int,
                                ownerUID: uid_t? = getuid()) throws -> Data {
    var before = stat()
    guard lstat(url.path, &before) == 0, (before.st_mode & S_IFMT) == S_IFREG,
          before.st_nlink == 1, before.st_size > 0, before.st_size <= Int64(maximum),
          mode_t(before.st_mode & 0o777) == expectedMode,
          ownerUID == nil || before.st_uid == ownerUID! else { throw Failure.invalid }
    let data = try Data(contentsOf: url, options: [.mappedIfSafe])
    var after = stat()
    guard lstat(url.path, &after) == 0, sameIdentity(before, after),
          data.count == Int(before.st_size) else { throw Failure.invalid }
    return data
}

private func sameIdentity(_ left: stat, _ right: stat) -> Bool {
    left.st_dev == right.st_dev && left.st_ino == right.st_ino && left.st_mode == right.st_mode &&
        left.st_nlink == right.st_nlink && left.st_uid == right.st_uid && left.st_size == right.st_size &&
        left.st_mtimespec.tv_sec == right.st_mtimespec.tv_sec &&
        left.st_mtimespec.tv_nsec == right.st_mtimespec.tv_nsec &&
        left.st_ctimespec.tv_sec == right.st_ctimespec.tv_sec &&
        left.st_ctimespec.tv_nsec == right.st_ctimespec.tv_nsec
}

private func rejectSymlinkAncestors(_ url: URL) throws {
    var current = url.path
    while true {
        var info = stat()
        if lstat(current, &info) == 0 {
            guard (info.st_mode & S_IFMT) != S_IFLNK else { throw Failure.invalid }
        } else if errno != ENOENT {
            throw Failure.invalid
        }
        let parent = URL(fileURLWithPath: current).deletingLastPathComponent().path
        if parent == current { return }
        current = parent
    }
}

private func visionInfoPlistURL(_ bundle: Bundle) throws -> URL {
    if let url = bundle.url(forResource: "Info", withExtension: "plist") { return url }
    let fallback = bundle.bundleURL.appendingPathComponent("Resources/Info.plist")
    guard FileManager.default.fileExists(atPath: fallback.path) else { throw Failure.invalid }
    return fallback
}

private func bundleVersion(_ bundle: Bundle) -> String {
    (bundle.object(forInfoDictionaryKey: "CFBundleVersion") as? String) ?? ""
}

private func unsafeCounts(_ text: String) -> [Int] {
    let patterns = [
        #"(?i)(?:api[_-]?key|access[_-]?token|password|secret)\s*[:=]\s*\S+"#,
        #"(?i)\b(?:[0-9a-f]{40,}|[A-Za-z0-9+/]{48,}={0,2})\b"#,
        #"(?:/Users/"# + #"|/home/|\\Users\\)"#,
        #"\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b"#,
        #"(?i)\bhttps?://(?!127\.0\.0\.1(?::\d+)?(?:/|\b))\S+"#,
    ]
    return patterns.map { pattern in
        (try? NSRegularExpression(pattern: pattern, options: [.caseInsensitive]))?
            .numberOfMatches(in: text, range: NSRange(text.startIndex..., in: text)) ?? Int.max
    }
}

private func canonicalJSON(_ value: Any) throws -> Data {
    if value is NSNull || value is String || value is NSNumber {
        return Data(try JSONSerialization.data(withJSONObject: [value]).dropFirst().dropLast())
    }
    if let array = value as? [Any] {
        var output = Data([91])
        for (index, item) in array.enumerated() {
            if index > 0 { output.append(44) }
            output.append(try canonicalJSON(item))
        }
        output.append(93)
        return output
    }
    guard let object = value as? [String: Any] else { throw Failure.invalid }
    var output = Data([123])
    for (index, key) in object.keys.sorted().enumerated() {
        if index > 0 { output.append(44) }
        output.append(Data(try JSONSerialization.data(withJSONObject: [key]).dropFirst().dropLast()))
        output.append(58)
        output.append(try canonicalJSON(object[key]!))
    }
    output.append(125)
    return output
}

private func sha256(_ data: Data) -> String {
    SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
}

private struct BoundIdentity {
    let value: stat
}

private struct BoundOutputParent {
    let descriptor: Int32
    let path: String
    let finalName: String
    let identity: BoundIdentity
}

private struct PrivateQuarantine {
    let descriptor: Int32
    let name: String
    let identity: BoundIdentity
}

private enum AtomicWriterTestMode: String {
    case none
    case failAfterWrite = "fail-after-write"
    case cleanupSwapAfterCheck = "cleanup-swap-after-check"
    case publishSwapAfterCheck = "publish-swap-after-check"
    case occupyFinalAfterCheck = "occupy-final-after-check"
    case hardlinkAfterCheck = "hardlink-after-check"
    case symlinkAfterCheck = "symlink-after-check"
    case observeDuringWrite = "observe-during-write"
}

private func writeExclusiveReadonly(_ data: Data, to url: URL,
                                    testMode: AtomicWriterTestMode = .none) throws {
    let parent = try openBoundOutputParent(url)
    defer { _ = close(parent.descriptor) }
    try requireAbsentAt(parent.descriptor, parent.finalName)

    let stagingName = try freshPrivateName(prefix: ".chora-o4visionocr-stage-")
    let descriptor = openat(parent.descriptor, stagingName,
                            O_CREAT | O_EXCL | O_WRONLY | O_CLOEXEC | O_NOFOLLOW, 0o600)
    guard descriptor >= 0 else { throw Failure.invalid }
    defer { _ = close(descriptor) }

    var created = stat()
    guard fstat(descriptor, &created) == 0,
          isOwnedRegular(created, parent: parent.identity.value, mode: nil, size: nil) else {
        throw Failure.invalid
    }

    var published = false
    var finishedIdentity: BoundIdentity?
    do {
        guard fchmod(descriptor, 0o600) == 0 else { throw Failure.invalid }
        try writeAll(data, descriptor: descriptor, parent: parent, testMode: testMode)
        guard fsync(descriptor) == 0, fchmod(descriptor, 0o400) == 0,
              fsync(descriptor) == 0 else { throw Failure.invalid }

        var finished = stat()
        guard fstat(descriptor, &finished) == 0,
              sameStableObject(created, finished),
              isOwnedRegular(finished, parent: parent.identity.value,
                             mode: 0o400, size: data.count) else { throw Failure.invalid }
        let expected = BoundIdentity(value: finished)
        finishedIdentity = expected

        try validateBoundParent(parent)
        try requireAbsentAt(parent.descriptor, parent.finalName)
        try applyAfterStagingHook(testMode, parent: parent, stagingName: stagingName)

        let beforePublish = try statAt(parent.descriptor, stagingName)
        guard samePreActionIdentity(expected.value, beforePublish),
              isOwnedRegular(beforePublish, parent: parent.identity.value,
                             mode: 0o400, size: data.count) else { throw Failure.invalid }
        try applyAfterPublishCheckHook(testMode, parent: parent, stagingName: stagingName)

        guard renameatx_np(parent.descriptor, stagingName,
                           parent.descriptor, parent.finalName,
                           UInt32(RENAME_EXCL)) == 0 else { throw Failure.invalid }
        published = true

        let moved = try statAt(parent.descriptor, parent.finalName)
        guard sameExactIdentity(expected.value, moved),
              isOwnedRegular(moved, parent: parent.identity.value,
                             mode: 0o400, size: data.count) else { throw Failure.invalid }
        try requireAbsentAt(parent.descriptor, stagingName)
        guard fsync(parent.descriptor) == 0 else { throw Failure.invalid }
        try validateBoundParent(parent)
        let durable = try statAt(parent.descriptor, parent.finalName)
        guard sameExactIdentity(expected.value, durable) else { throw Failure.invalid }
    } catch {
        if finishedIdentity == nil {
            var current = stat()
            if fstat(descriptor, &current) == 0,
               sameStableObject(created, current),
               isOwnedRegular(current, parent: parent.identity.value, mode: nil, size: nil) {
                finishedIdentity = BoundIdentity(value: current)
            }
        }
        if let expected = finishedIdentity {
            if published {
                try? quarantinePublishedEntry(parent, name: parent.finalName, expected: expected)
            } else {
                try? quarantineAndRemoveExactEntry(parent, name: stagingName,
                                                   expected: expected, testMode: testMode)
            }
        }
        throw Failure.invalid
    }
}

private func openBoundOutputParent(_ url: URL) throws -> BoundOutputParent {
    let parentPath = url.deletingLastPathComponent().path
    let finalName = url.lastPathComponent
    guard finalName.isEmpty == false, finalName != ".", finalName != "..",
          finalName.utf8.count <= 255, finalName.utf8.contains(0) == false,
          finalName.contains("/") == false else { throw Failure.invalid }

    let descriptor: Int32
    let opened: stat
    do {
        (descriptor, opened) = try openCanonicalDirectory(parentPath)
    } catch {
        throw Failure.invalid
    }
    var pathInfo = stat()
    guard lstat(parentPath, &pathInfo) == 0,
          sameBoundObject(opened, pathInfo), isPrivateParent(opened), isPrivateParent(pathInfo) else {
        _ = close(descriptor)
        throw Failure.invalid
    }
    return BoundOutputParent(descriptor: descriptor, path: parentPath, finalName: finalName,
                             identity: BoundIdentity(value: opened))
}

private func openCanonicalDirectory(_ path: String) throws -> (Int32, stat) {
    guard path.hasPrefix("/"), path.utf8.contains(0) == false else { throw Failure.invalid }
    let components = path.split(separator: "/", omittingEmptySubsequences: true).map(String.init)
    let rootDescriptor = open("/", O_RDONLY | O_DIRECTORY | O_CLOEXEC | O_NOFOLLOW)
    guard rootDescriptor >= 0 else { throw Failure.invalid }
    var current = rootDescriptor
    var returning = false
    defer {
        if returning == false { _ = close(current) }
    }
    for component in components {
        guard component != ".", component != "..", component.utf8.count <= 255,
              component.utf8.contains(0) == false else { throw Failure.invalid }
        var before = stat()
        guard fstatat(current, component, &before, AT_SYMLINK_NOFOLLOW) == 0,
              (before.st_mode & S_IFMT) == S_IFDIR else { throw Failure.invalid }
        let next = openat(current, component, O_RDONLY | O_DIRECTORY | O_CLOEXEC | O_NOFOLLOW)
        guard next >= 0 else { throw Failure.invalid }
        var opened = stat()
        guard fstat(next, &opened) == 0, sameBoundObject(before, opened) else {
            _ = close(next)
            throw Failure.invalid
        }
        _ = close(current)
        current = next
    }
    var final = stat()
    guard fstat(current, &final) == 0 else { throw Failure.invalid }
    returning = true
    return (current, final)
}

private func validateBoundParent(_ parent: BoundOutputParent) throws {
    var descriptorInfo = stat()
    var pathInfo = stat()
    guard fstat(parent.descriptor, &descriptorInfo) == 0,
          lstat(parent.path, &pathInfo) == 0,
          sameBoundObject(parent.identity.value, descriptorInfo),
          sameBoundObject(parent.identity.value, pathInfo),
          isPrivateParent(descriptorInfo), isPrivateParent(pathInfo) else { throw Failure.invalid }
}

private func isPrivateParent(_ info: stat) -> Bool {
    (info.st_mode & S_IFMT) == S_IFDIR && mode_t(info.st_mode & 0o7777) == 0o700 &&
        info.st_uid == geteuid()
}

private func isOwnedRegular(_ info: stat, parent: stat, mode: mode_t?, size: Int?) -> Bool {
    guard (info.st_mode & S_IFMT) == S_IFREG, info.st_nlink == 1,
          info.st_uid == geteuid(), info.st_gid == parent.st_gid,
          info.st_dev == parent.st_dev else { return false }
    if let mode, mode_t(info.st_mode & 0o7777) != mode { return false }
    if let size, info.st_size != off_t(size) { return false }
    return true
}

private func sameStableObject(_ left: stat, _ right: stat) -> Bool {
    left.st_dev == right.st_dev && left.st_ino == right.st_ino &&
        (left.st_mode & S_IFMT) == (right.st_mode & S_IFMT) &&
        left.st_uid == right.st_uid && left.st_gid == right.st_gid
}

private func sameBoundObject(_ left: stat, _ right: stat) -> Bool {
    sameStableObject(left, right) && left.st_mode == right.st_mode
}

private func sameExactIdentity(_ left: stat, _ right: stat) -> Bool {
    sameBoundObject(left, right) && left.st_nlink == right.st_nlink &&
        left.st_size == right.st_size
}

private func samePreActionIdentity(_ left: stat, _ right: stat) -> Bool {
    sameExactIdentity(left, right) &&
        left.st_mtimespec.tv_sec == right.st_mtimespec.tv_sec &&
        left.st_mtimespec.tv_nsec == right.st_mtimespec.tv_nsec &&
        left.st_ctimespec.tv_sec == right.st_ctimespec.tv_sec &&
        left.st_ctimespec.tv_nsec == right.st_ctimespec.tv_nsec
}

private func statAt(_ parentDescriptor: Int32, _ name: String) throws -> stat {
    var info = stat()
    guard fstatat(parentDescriptor, name, &info, AT_SYMLINK_NOFOLLOW) == 0 else {
        throw Failure.invalid
    }
    return info
}

private func requireAbsentAt(_ parentDescriptor: Int32, _ name: String) throws {
    var info = stat()
    errno = 0
    guard fstatat(parentDescriptor, name, &info, AT_SYMLINK_NOFOLLOW) != 0,
          errno == ENOENT else { throw Failure.invalid }
}

private func freshPrivateName(prefix: String) throws -> String {
    let name = prefix + UUID().uuidString.lowercased().replacingOccurrences(of: "-", with: "")
    guard name.utf8.count <= 255 else { throw Failure.invalid }
    return name
}

private func writeAll(_ data: Data, descriptor: Int32, parent: BoundOutputParent,
                      testMode: AtomicWriterTestMode) throws {
    var offset = 0
    var observed = false
    while offset < data.count {
        let requested = min(4096, data.count - offset)
        let written = data.withUnsafeBytes { raw in
            Darwin.write(descriptor, raw.baseAddress!.advanced(by: offset), requested)
        }
        guard written > 0 else { throw Failure.invalid }
        offset += written
        if observed == false {
            observed = true
            try applyDuringWriteHook(testMode, parent: parent)
        }
    }
}

private func createQuarantine(_ parent: BoundOutputParent) throws -> PrivateQuarantine {
    for _ in 0..<8 {
        let name = try freshPrivateName(prefix: ".chora-o4visionocr-quarantine-")
        if mkdirat(parent.descriptor, name, 0o700) != 0 {
            if errno == EEXIST { continue }
            throw Failure.invalid
        }
        var created = stat()
        guard fstatat(parent.descriptor, name, &created, AT_SYMLINK_NOFOLLOW) == 0,
              (created.st_mode & S_IFMT) == S_IFDIR,
              mode_t(created.st_mode & 0o7777) == 0o700,
              created.st_uid == geteuid(), created.st_gid == parent.identity.value.st_gid,
              created.st_dev == parent.identity.value.st_dev else { throw Failure.invalid }
        let descriptor = openat(parent.descriptor, name,
                                O_RDONLY | O_DIRECTORY | O_CLOEXEC | O_NOFOLLOW)
        guard descriptor >= 0 else { throw Failure.invalid }
        var opened = stat()
        guard fstat(descriptor, &opened) == 0, sameBoundObject(created, opened) else {
            _ = close(descriptor)
            throw Failure.invalid
        }
        return PrivateQuarantine(descriptor: descriptor, name: name,
                                 identity: BoundIdentity(value: opened))
    }
    throw Failure.invalid
}

private func quarantineAndRemoveExactEntry(_ parent: BoundOutputParent, name: String,
                                           expected: BoundIdentity,
                                           testMode: AtomicWriterTestMode) throws {
    let current = try statAt(parent.descriptor, name)
    guard samePreActionIdentity(expected.value, current) else { throw Failure.invalid }
    try applyBeforeCleanupMoveHook(testMode, parent: parent, name: name)

    let quarantine = try createQuarantine(parent)
    var keepQuarantine = true
    defer {
        _ = close(quarantine.descriptor)
        if keepQuarantine == false {
            _ = removeExactEmptyQuarantine(parent, quarantine: quarantine)
        }
    }
    guard renameatx_np(parent.descriptor, name, quarantine.descriptor, "entry",
                       UInt32(RENAME_EXCL)) == 0 else { throw Failure.invalid }
    let moved = try statAt(quarantine.descriptor, "entry")
    guard sameExactIdentity(expected.value, moved) else {
        // A replacement moved after the public-name check is foreign. Keeping
        // it in this exact private quarantine is the fail-closed outcome.
        throw Failure.invalid
    }
    guard unlinkat(quarantine.descriptor, "entry", 0) == 0,
          fsync(quarantine.descriptor) == 0 else { throw Failure.invalid }
    keepQuarantine = false
}

private func quarantinePublishedEntry(_ parent: BoundOutputParent, name: String,
                                      expected: BoundIdentity) throws {
    let quarantine = try createQuarantine(parent)
    var keepQuarantine = true
    defer {
        _ = close(quarantine.descriptor)
        if keepQuarantine == false {
            _ = removeExactEmptyQuarantine(parent, quarantine: quarantine)
        }
    }
    guard renameatx_np(parent.descriptor, name, quarantine.descriptor, "entry",
                       UInt32(RENAME_EXCL)) == 0 else { throw Failure.invalid }
    let moved = try statAt(quarantine.descriptor, "entry")
    if sameExactIdentity(expected.value, moved) {
        guard unlinkat(quarantine.descriptor, "entry", 0) == 0,
              fsync(quarantine.descriptor) == 0 else { throw Failure.invalid }
        keepQuarantine = false
    }
    // If the published name was swapped, its foreign inode remains under the
    // open quarantine descriptor while the requested final name is absent.
    try requireAbsentAt(parent.descriptor, name)
}

private func removeExactEmptyQuarantine(_ parent: BoundOutputParent,
                                        quarantine: PrivateQuarantine) -> Int32 {
    var current = stat()
    guard fstatat(parent.descriptor, quarantine.name, &current, AT_SYMLINK_NOFOLLOW) == 0,
          sameBoundObject(quarantine.identity.value, current) else { return -1 }
    return unlinkat(parent.descriptor, quarantine.name, AT_REMOVEDIR)
}

private func applyDuringWriteHook(_ mode: AtomicWriterTestMode,
                                  parent: BoundOutputParent) throws {
#if O4VISIONOCR_ATOMIC_WRITER_TESTING
    if mode == .observeDuringWrite {
        try requireAbsentAt(parent.descriptor, parent.finalName)
    }
#else
    guard mode == .none else { throw Failure.invalid }
#endif
}

private func applyAfterStagingHook(_ mode: AtomicWriterTestMode,
                                   parent: BoundOutputParent,
                                   stagingName: String) throws {
#if O4VISIONOCR_ATOMIC_WRITER_TESTING
    if mode == .failAfterWrite || mode == .cleanupSwapAfterCheck {
        throw Failure.invalid
    }
#else
    guard mode == .none else { throw Failure.invalid }
#endif
}

private func applyAfterPublishCheckHook(_ mode: AtomicWriterTestMode,
                                        parent: BoundOutputParent,
                                        stagingName: String) throws {
#if O4VISIONOCR_ATOMIC_WRITER_TESTING
    switch mode {
    case .publishSwapAfterCheck:
        try replaceTestEntryWithForeign(parent, name: stagingName, symlink: false)
    case .occupyFinalAfterCheck:
        try createTestForeignFile(parent.descriptor, name: parent.finalName)
    case .hardlinkAfterCheck:
        guard linkat(parent.descriptor, stagingName, parent.descriptor,
                     ".chora-o4visionocr-test-owned-backup", 0) == 0 else { throw Failure.invalid }
    case .symlinkAfterCheck:
        try replaceTestEntryWithForeign(parent, name: stagingName, symlink: true)
    default:
        break
    }
#else
    guard mode == .none else { throw Failure.invalid }
#endif
}

private func applyBeforeCleanupMoveHook(_ mode: AtomicWriterTestMode,
                                        parent: BoundOutputParent, name: String) throws {
#if O4VISIONOCR_ATOMIC_WRITER_TESTING
    if mode == .cleanupSwapAfterCheck {
        try replaceTestEntryWithForeign(parent, name: name, symlink: false)
    }
#else
    guard mode == .none else { throw Failure.invalid }
#endif
}

#if O4VISIONOCR_ATOMIC_WRITER_TESTING
private func replaceTestEntryWithForeign(_ parent: BoundOutputParent, name: String,
                                         symlink: Bool) throws {
    let backup = ".chora-o4visionocr-test-owned-backup"
    guard renameatx_np(parent.descriptor, name, parent.descriptor, backup,
                       UInt32(RENAME_EXCL)) == 0 else { throw Failure.invalid }
    if symlink {
        guard symlinkat(backup, parent.descriptor, name) == 0 else { throw Failure.invalid }
    } else {
        try createTestForeignFile(parent.descriptor, name: name)
    }
}

private func createTestForeignFile(_ parentDescriptor: Int32, name: String) throws {
    let descriptor = openat(parentDescriptor, name,
                            O_CREAT | O_EXCL | O_WRONLY | O_CLOEXEC | O_NOFOLLOW, 0o600)
    guard descriptor >= 0 else { throw Failure.invalid }
    defer { _ = close(descriptor) }
    let foreign = Data("foreign-inode".utf8)
    var offset = 0
    while offset < foreign.count {
        let written = foreign.withUnsafeBytes { raw in
            Darwin.write(descriptor, raw.baseAddress!.advanced(by: offset), raw.count - offset)
        }
        guard written > 0 else { throw Failure.invalid }
        offset += written
    }
    guard fchmod(descriptor, 0o400) == 0, fsync(descriptor) == 0 else { throw Failure.invalid }
}

private func executeAtomicWriterTestDriver() throws {
    let argv = Array(CommandLine.arguments.dropFirst())
    guard argv.count == 3, argv[0] == "--atomic-writer-self-test",
          let mode = AtomicWriterTestMode(rawValue: argv[1]), mode != .none else { throw Failure.invalid }
    let output = try exactFileURL(argv[2])
    try writeExclusiveReadonly(Data(repeating: 0x61, count: 1024 * 1024),
                               to: output, testMode: mode)
}
#endif

do {
#if O4VISIONOCR_ATOMIC_WRITER_TESTING
    try executeAtomicWriterTestDriver()
#else
    try execute()
#endif
} catch {
    // The bounded launcher owns diagnostics. Never disclose private paths.
    exit(1)
}
