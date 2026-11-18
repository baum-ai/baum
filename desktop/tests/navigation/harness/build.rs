fn main() {
    // Declaring the command makes it need a permission, as the app's do.
    let manifest = tauri_build::AppManifest::new().commands(&["record_call"]);
    tauri_build::try_build(tauri_build::Attributes::new().app_manifest(manifest))
        .expect("tauri build script");
}
