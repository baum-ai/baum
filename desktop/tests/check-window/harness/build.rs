fn main() {
    // Declaring the commands makes each one need a permission, as the app's do.
    let manifest =
        tauri_build::AppManifest::new().commands(&["open_check_window", "close_check_window"]);
    tauri_build::try_build(tauri_build::Attributes::new().app_manifest(manifest))
        .expect("tauri build script");
}
