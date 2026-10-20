import Foundation

/// Restarting the coordinator (#179), from the menu bar or Settings ▸ Coordinator: both ask
/// first, with a destructive Restart, since it stops whatever the coordinator is doing.
public enum CoordinatorRestart {
    /// The core's `app.action` id.
    public static let actionID = "agent:restart"
    public static let title = "Restart the coordinator?"
    public static let message = "This stops whatever the coordinator is doing right now, including work it's in the middle of. It starts again on your next request and picks up its conversation."
    public static let confirm = "Restart"
}
