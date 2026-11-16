import SwiftUI
import AppKit

@MainActor
enum SnapshotRenderer {
    static func render(to directory: URL) throws {
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        for name in ["welcome", "link-qr", "link-paircode", "link-success", "keep-running", "connect", "safety", "done", "menubar-2-drafts", "menubar-empty"] {
            let model = AppModel()
            model.snapshotMode = true; model.ready = true; model.approvalLoaded = true
            model.status.name = "Alex"; model.status.phone = "+91 98765 43210"
            switch name {
            case "welcome": model.step = .welcome
            case "link-qr":
                model.step = .link
                model.qrCode = "wassup-snapshot-only,not-a-real-login-code,0123456789abcdef,WhatsApp-for-your-AI"
                model.qrLifetime = 60; model.qrExpires = Date().addingTimeInterval(52)
            case "link-paircode": model.step = .link; model.phoneMode = true; model.pairCode = "ABCD-EFGH"
            case "link-success": model.step = .link; model.status.loggedIn = true; model.status.connected = true
            case "keep-running": model.step = .running
            case "connect":
                model.step = .connect
                model.apps[0].found = true; model.apps[0].connected = true
                model.apps[1].found = true; model.apps[2].found = true
            case "safety": model.step = .safety
            case "done": model.step = .done
            default:
                model.status.loggedIn = true; model.status.connected = true
                if name == "menubar-2-drafts" {
                    model.drafts = [
                        Draft(id: 12, summary: "Message Priya", preview: "Running ten minutes late. Save me a seat?", source: "mcp", expires: Date().addingTimeInterval(720)),
                        Draft(id: 13, summary: "Reply to Weekend plans", preview: "Saturday works for me. How about 7?", source: "mcp", expires: Date().addingTimeInterval(1500))
                    ]
                }
            }
            let menu = name.hasPrefix("menubar")
            let view = menu ? AnyView(MenuBarView(model: model)) : AnyView(SetupView(model: model))
            let renderer = ImageRenderer(content: view.environment(\.renderingSnapshot, true).environment(\.displayScale, 2).environment(\.colorScheme, .dark))
            renderer.scale = 2
            renderer.proposedSize = ProposedViewSize(width: menu ? 340 : 680, height: menu ? nil : 580)
            guard let cg = renderer.cgImage, let png = NSBitmapImageRep(cgImage: cg).representation(using: .png, properties: [:]) else {
                throw CLIError(code: "snapshot", message: "Couldn't render \(name).")
            }
            try png.write(to: directory.appendingPathComponent(name + ".png"))
            print("Rendered \(name).png (\(cg.width)×\(cg.height))")
        }
    }
}
