fn main() {
    // Declaring the app's commands makes each one need a permission, so a
    // page only reaches the commands its capability names.
    let manifest = tauri_build::AppManifest::new().commands(&[
        "connection",
        "reconnect",
        "setup_state",
        "install_command",
        "uninstall_command",
        "install_hooks",
        "uninstall_hooks",
        "finish_setup",
    ]);
    tauri_build::try_build(tauri_build::Attributes::new().app_manifest(manifest))
        .expect("tauri build script");
}
