//! A window that loads HARNESS_URL with the desktop app's navigation
//! decisions (`navigation.rs` is copied in from the app at build time) and
//! logs each one instead of acting on it. It exits after HARNESS_SECONDS.

#[allow(dead_code)]
mod navigation;

use navigation::Decision;
use tauri::{WebviewUrl, WebviewWindowBuilder};

fn main() {
    let url: url::Url = std::env::var("HARNESS_URL")
        .expect("HARNESS_URL")
        .parse()
        .expect("HARNESS_URL is a URL");
    let seconds = std::env::var("HARNESS_SECONDS")
        .ok()
        .and_then(|s| s.parse().ok())
        .unwrap_or(9);
    let daemon = url.origin();
    tauri::Builder::default()
        .setup(move |app| {
            WebviewWindowBuilder::new(app, "main", WebviewUrl::External(url.clone()))
                .inner_size(1280.0, 900.0)
                .on_navigation(move |u| {
                    let decision = navigation::decide(u, Some(&daemon), false);
                    eprintln!("NAV {u} -> {decision:?}");
                    decision == Decision::Allow
                })
                .on_new_window(|u, _| {
                    eprintln!("NEWWINDOW {u} -> {:?}", navigation::decide_new_window(&u));
                    tauri::webview::NewWindowResponse::Deny
                })
                .build()?;
            let handle = app.handle().clone();
            std::thread::spawn(move || {
                std::thread::sleep(std::time::Duration::from_secs(seconds));
                handle.exit(0);
            });
            Ok(())
        })
        .run(tauri::generate_context!())
        .expect("harness runs");
}
