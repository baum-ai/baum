fn main() {
    // Declaring the app's commands makes each one need a permission, so a
    // page only reaches the commands its capability names.
    let manifest = tauri_build::AppManifest::new().commands(&["connection", "reconnect"]);
    tauri_build::try_build(tauri_build::Attributes::new().app_manifest(manifest))
        .expect("tauri build script");
}
