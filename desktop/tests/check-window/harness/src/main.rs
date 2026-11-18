//! A main window that loads HARNESS_URL, a mock daemon page, with the
//! desktop app's check window (`check_window.rs`, `navigation.rs` and
//! `declaration.rs` are copied in from the app at build time). Its page may
//! call `open_check_window` and `close_check_window`, which do what the app's
//! commands do with the main page's origin as the attached daemon, under the
//! app's capabilities for the daemon's pages (with only these permissions)
//! and for the check window (as it is). It exits after HARNESS_SECONDS.

#[allow(dead_code)]
mod check_window;
#[allow(dead_code)]
mod declaration;
#[allow(dead_code)]
mod navigation;

use std::sync::Arc;

use tauri::{AppHandle, WebviewUrl, WebviewWindowBuilder};

const MAIN: &str = "main";

#[tauri::command]
async fn open_check_window(app: AppHandle, webview: tauri::Webview) -> Result<(), String> {
    if webview.label() != MAIN {
        return Err("Only the main window can open the check window.".into());
    }
    let main = webview.url().map_err(|e| e.to_string())?;
    let daemon = main.origin();
    let url = check_window::page_url(&main, &daemon).ok_or("not a daemon page")?;
    eprintln!("OPEN {url}");
    check_window::open(&app, url, Arc::new(move || Some(daemon.clone())))
        .map_err(|e| format!("Cannot open the check window: {e}"))
}

#[tauri::command]
async fn close_check_window(app: AppHandle, webview: tauri::Webview) {
    if webview.label() == MAIN {
        check_window::close(&app);
        eprintln!("CLOSED");
    }
}

fn main() {
    let url: url::Url = std::env::var("HARNESS_URL")
        .expect("HARNESS_URL")
        .parse()
        .expect("HARNESS_URL is a URL");
    let seconds = std::env::var("HARNESS_SECONDS")
        .ok()
        .and_then(|s| s.parse().ok())
        .unwrap_or(120);
    tauri::Builder::default()
        .invoke_handler(tauri::generate_handler![
            open_check_window,
            close_check_window
        ])
        .setup(move |app| {
            WebviewWindowBuilder::new(app, MAIN, WebviewUrl::External(url.clone()))
                .inner_size(800.0, 600.0)
                .position(0.0, 0.0)
                .initialization_script(declaration::script(declaration::SHELL))
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
