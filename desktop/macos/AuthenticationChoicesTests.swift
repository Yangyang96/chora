import AppKit

@main struct AuthenticationChoicesTests {
    static func main() throws {
        _ = NSApplication.shared
        let choices: [[String: Any]] = [
            ["id": "gateway:api_key", "label": "Cloudflare API key"],
            ["id": "workers:api_key", "label": "Cloudflare API key"],
            ["id": "deepseek:api_key", "label": "DeepSeek API key"]
        ]
        let control = NSPopUpButton(frame: .zero)
        AuthenticationChoices.populate(control, choices: choices)
        control.selectItem(withTitle: "DeepSeek API key")
        guard AuthenticationChoices.value(control) == "deepseek:api_key" else {
            throw NSError(domain: "AuthenticationChoicesTests", code: 1, userInfo: [NSLocalizedDescriptionKey: "Selected DeepSeek but mapped to another provider"])
        }
        guard control.numberOfItems == choices.count else { fatalError("Duplicate labels lost a provider") }
        for (index, choice) in choices.enumerated() {
            control.selectItem(at: index)
            guard AuthenticationChoices.value(control) == choice["id"] as? String else { fatalError("Incorrect provider identity") }
        }
        guard Set(control.itemTitles).count == choices.count else { fatalError("Duplicate providers remain indistinguishable") }
        print("Authentication choices: duplicate labels preserve distinct provider identities")
    }
}
