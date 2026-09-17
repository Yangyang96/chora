import AppKit

// NSPopUpButton.addItems(withTitles:) collapses duplicate titles. Build explicit
// menu items instead, and bind each visible choice to its provider-owned ID.
enum AuthenticationChoices {
    static func populate(_ control: NSPopUpButton, choices: [[String: Any]]) {
        let labels = choices.map { $0["label"] as? String ?? "Unknown" }
        let counts = Dictionary(grouping: labels, by: { $0 }).mapValues { $0.count }
        let menu = NSMenu()
        for (index, choice) in choices.enumerated() {
            let id = choice["id"] as? String ?? ""
            let label = labels[index]
            let title = (counts[label] ?? 0) > 1 ? label + " (" + id + ")" : label
            let item = NSMenuItem(title: title, action: nil, keyEquivalent: "")
            item.representedObject = id
            menu.addItem(item)
        }
        control.menu = menu
        control.selectItem(at: 0)
    }
    static func value(_ control: NSPopUpButton) -> String {
        control.selectedItem?.representedObject as? String ?? ""
    }
}
