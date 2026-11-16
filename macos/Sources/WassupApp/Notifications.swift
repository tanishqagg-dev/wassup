import Foundation
import UserNotifications

@MainActor
final class Notifications: NSObject, UNUserNotificationCenterDelegate {
    private weak var model: AppModel?
    private let center = UNUserNotificationCenter.current()
    init(model: AppModel) {
        self.model = model
        super.init()
        center.delegate = self
        let approve = UNNotificationAction(identifier: "approve", title: "Approve", options: [.authenticationRequired])
        let decline = UNNotificationAction(identifier: "decline", title: "Decline", options: [.destructive])
        center.setNotificationCategories([UNNotificationCategory(identifier: "draft", actions: [approve, decline], intentIdentifiers: [])])
    }
    func requestPermission() async -> String {
        do {
            let granted = try await center.requestAuthorization(options: [.alert, .sound, .badge])
            return granted ? "" : "Notifications are off. Approvals are still in your menu bar."
        } catch { return "Notifications aren't available. Approvals are still in your menu bar." }
    }
    func post(_ data: JSONValue) {
        guard data["id"].int > 0 else { return }
        let content = UNMutableNotificationContent()
        content.title = data["summary"].string
        content.body = data["preview"].string
        content.categoryIdentifier = "draft"
        content.userInfo = ["draftID": data["id"].int, "binaryPath": model?.cli?.binary.path ?? ""]
        content.sound = .default
        center.add(UNNotificationRequest(identifier: "draft-\(data["id"].int)", content: content, trigger: nil))
    }
    func remove(_ id: Int) { center.removeDeliveredNotifications(withIdentifiers: ["draft-\(id)"]) }
    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification, withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) {
        completionHandler([.banner, .sound])
    }
    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse, withCompletionHandler completionHandler: @escaping () -> Void) {
        let action = response.actionIdentifier
        let id = response.notification.request.content.userInfo["draftID"] as? Int
        let binaryPath = response.notification.request.content.userInfo["binaryPath"] as? String
        Task { @MainActor [weak self] in
            if let id, ["approve", "decline"].contains(action) { self?.model?.handleNotificationAction(id, approve: action == "approve", binaryPath: binaryPath) }
            else { self?.model?.openSetup?() }
        }
        completionHandler()
    }
}
