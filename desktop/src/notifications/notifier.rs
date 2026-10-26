//! The decisions: whether a toast is shown at all, and what a click does.

use std::sync::{Arc, Mutex, MutexGuard};

use super::platform::{Platform, ShowError};
use super::settings::{Permission, Settings, Store};
use super::toast::{self, Kind, Route, Toast};

/// The window the toasts are about.
pub trait Host: Send + Sync {
    /// The pull request the window shows while it has focus, as clone and
    /// number; `None` when the window is unfocused, hidden or elsewhere.
    fn focused_pr(&self) -> Option<(String, u64)>;
    /// Brings the window to the front on `route`.
    fn open(&self, route: &Route);
}

/// What became of a toast, for tests and the log.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Outcome {
    Shown,
    /// Its kind is switched off.
    Off,
    /// The operating system refuses notifications.
    Denied,
    /// The window has focus on the same pull request.
    Focused,
    /// An event that repeats while its condition lasts, already shown.
    Duplicate,
    Failed,
}

/// Remembered dedupe keys; the oldest are forgotten.
const SEEN: usize = 64;

pub struct Notifier {
    host: Arc<dyn Host>,
    platform: Arc<dyn Platform>,
    store: Store,
    state: Mutex<State>,
}

/// Only the settings live behind the lock. It is never held across a call
/// into the window (which runs on the main thread, as does the menu handler
/// that takes this lock) or the operating system.
struct State {
    settings: Settings,
}

impl Notifier {
    pub fn new(host: Arc<dyn Host>, platform: Arc<dyn Platform>, store: Store) -> Notifier {
        let settings = store.load();
        Notifier {
            host,
            platform,
            store,
            state: Mutex::new(State { settings }),
        }
    }

    fn state(&self) -> MutexGuard<'_, State> {
        self.state
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner())
    }

    pub fn settings(&self) -> Settings {
        self.state().settings.clone()
    }

    /// Turns one kind on or off and keeps the choice. Turning one on after a
    /// refusal asks the operating system again, since the user may have
    /// changed their mind there.
    pub fn set_kind(&self, kind: Kind, on: bool) {
        let mut state = self.state();
        state.settings.kinds.set(kind, on);
        if on && state.settings.permission == Permission::Denied {
            state.settings.permission = Permission::Unknown;
        }
        self.save(&state.settings);
    }

    /// A daemon event, as the events stream delivers it.
    pub fn handle_event(&self, name: &str, data: &str) -> Option<Outcome> {
        let toast = toast::from_event(name, data)?;
        let outcome = self.deliver(&toast);
        if !matches!(outcome, Outcome::Shown | Outcome::Duplicate) {
            eprintln!("baum desktop: notification not shown ({outcome:?})");
        }
        Some(outcome)
    }

    pub fn deliver(&self, toast: &Toast) -> Outcome {
        // Decide from a snapshot; the lock is taken again only to record.
        let settings = self.settings();
        if !settings.kinds.get(toast.kind) {
            return Outcome::Off;
        }
        if settings.permission == Permission::Denied {
            return Outcome::Denied;
        }
        if let Some((clone, number)) = toast.route.pr()
            && self.host.focused_pr() == Some((clone.to_string(), number))
        {
            return Outcome::Focused;
        }
        if let Some(key) = &toast.dedupe
            && settings.announced.contains(key)
        {
            return Outcome::Duplicate;
        }
        // The first notification is the first use: only now is the operating
        // system asked.
        if settings.permission == Permission::Unknown {
            let answer = self.platform.request_permission();
            let mut state = self.state();
            if state.settings.permission == Permission::Unknown {
                state.settings.permission = answer;
                self.save(&state.settings);
            }
            if state.settings.permission == Permission::Denied {
                return Outcome::Denied;
            }
        }
        let host = self.host.clone();
        let route = toast.route.clone();
        match self
            .platform
            .show(toast, Box::new(move || host.open(&route)))
        {
            Ok(()) => {
                if let Some(key) = &toast.dedupe {
                    let mut state = self.state();
                    state.settings.announced.push(key.clone());
                    let excess = state.settings.announced.len().saturating_sub(SEEN);
                    state.settings.announced.drain(..excess);
                    self.save(&state.settings);
                }
                Outcome::Shown
            }
            Err(ShowError::Denied) => {
                let mut state = self.state();
                state.settings.permission = Permission::Denied;
                self.save(&state.settings);
                Outcome::Denied
            }
            Err(ShowError::Failed(reason)) => {
                eprintln!("baum desktop: cannot show a notification: {reason}");
                Outcome::Failed
            }
        }
    }

    fn save(&self, settings: &Settings) {
        if let Err(e) = self.store.save(settings) {
            eprintln!("baum desktop: cannot save notification settings: {e}");
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::{AtomicUsize, Ordering};

    #[derive(Default)]
    struct FakeHost {
        focused: Mutex<Option<(String, u64)>>,
        opened: Mutex<Vec<Route>>,
    }

    impl Host for FakeHost {
        fn focused_pr(&self) -> Option<(String, u64)> {
            self.focused.lock().unwrap().clone()
        }
        fn open(&self, route: &Route) {
            self.opened.lock().unwrap().push(route.clone());
        }
    }

    struct FakePlatform {
        /// The answer to the permission request.
        permission: Permission,
        /// Refuse at show time, as an OS that revoked permission would.
        refuse: bool,
        asked: AtomicUsize,
        shown: Mutex<Vec<Toast>>,
        clicks: Mutex<Vec<Box<dyn FnOnce() + Send>>>,
    }

    impl FakePlatform {
        fn new(permission: Permission) -> Arc<FakePlatform> {
            Arc::new(FakePlatform {
                permission,
                refuse: false,
                asked: AtomicUsize::new(0),
                shown: Mutex::default(),
                clicks: Mutex::default(),
            })
        }
        fn click(&self) {
            let click = self.clicks.lock().unwrap().pop().expect("a shown toast");
            click();
        }
    }

    impl Platform for FakePlatform {
        fn request_permission(&self) -> Permission {
            self.asked.fetch_add(1, Ordering::SeqCst);
            self.permission
        }
        fn show(&self, toast: &Toast, on_click: Box<dyn FnOnce() + Send>) -> Result<(), ShowError> {
            if self.refuse {
                return Err(ShowError::Denied);
            }
            self.shown.lock().unwrap().push(toast.clone());
            self.clicks.lock().unwrap().push(on_click);
            Ok(())
        }
    }

    fn setup(permission: Permission) -> (Notifier, Arc<FakeHost>, Arc<FakePlatform>) {
        let host = Arc::new(FakeHost::default());
        let platform = FakePlatform::new(permission);
        let notifier = Notifier::new(host.clone(), platform.clone(), Store::in_memory());
        (notifier, host, platform)
    }

    fn notice(kind: &str, extra: &str) -> String {
        format!(
            r#"{{"kind":"{kind}","repository":"acme/widgets","pr":7,"clone":"c1","title":"Fix it"{extra}}}"#
        )
    }

    const CAP: &str = r#"{"repository":"acme/widgets","enabled":true,"daily_cap":5.0,"spent_today":5.2,"paused_until":"2026-10-21T00:00:00Z","notice":"daily_cap","pending":0}"#;

    #[test]
    fn each_event_kind_is_shown() {
        let (notifier, _, platform) = setup(Permission::Granted);
        let events = [
            (
                "notice",
                notice("review_finished", r#","risk":4,"risk_status":"complete""#),
            ),
            ("notice", notice("review_failed", r#","reason":"timeout""#)),
            ("notice", notice("review_requested", "")),
            ("notice", notice("review_automatic", "")),
            ("automatic", CAP.to_string()),
            ("notice", notice("checks_failed", "")),
            ("notice", notice("conflict", "")),
            ("notice", notice("changes_requested", "")),
            ("notice", notice("ready_to_merge", "")),
            ("notice", notice("fix_pushed", r#","commit":"0123456""#)),
            ("notice", notice("fix_failed", r#","reason":"push_failed""#)),
        ];
        for (name, data) in &events {
            assert_eq!(
                notifier.handle_event(name, data),
                Some(Outcome::Shown),
                "{data}"
            );
        }
        let shown = platform.shown.lock().unwrap();
        let kinds: Vec<Kind> = shown.iter().map(|t| t.kind).collect();
        assert_eq!(
            kinds,
            [
                Kind::Finished,
                Kind::Failed,
                Kind::Requested,
                Kind::Automatic,
                Kind::CapReached,
                Kind::ChecksFailed,
                Kind::Conflict,
                Kind::ChangesRequested,
                Kind::ReadyToMerge,
                Kind::FixPushed,
                Kind::FixFailed
            ]
        );
        assert_eq!(shown[0].title, "Review finished: risk 4 of 5");
        assert_eq!(shown[1].title, "Review failed: timeout");
    }

    #[test]
    fn other_events_are_ignored() {
        let (notifier, _, platform) = setup(Permission::Granted);
        assert_eq!(notifier.handle_event("index", "{}"), None);
        assert_eq!(notifier.handle_event("reset", "{}"), None);
        assert!(platform.shown.lock().unwrap().is_empty());
    }

    #[test]
    fn clicking_a_pull_request_notification_opens_its_page() {
        let (notifier, host, platform) = setup(Permission::Granted);
        notifier.handle_event("notice", &notice("review_finished", ""));
        platform.click();
        assert_eq!(
            *host.opened.lock().unwrap(),
            [Route::Pr {
                clone: "c1".into(),
                number: 7
            }]
        );
    }

    #[test]
    fn clicking_the_cap_notification_opens_the_jobs_list() {
        let (notifier, host, platform) = setup(Permission::Granted);
        notifier.handle_event("automatic", CAP);
        platform.click();
        assert_eq!(*host.opened.lock().unwrap(), [Route::Jobs]);
    }

    #[test]
    fn the_cap_is_announced_once_per_pause() {
        let (notifier, _, platform) = setup(Permission::Granted);
        assert_eq!(
            notifier.handle_event("automatic", CAP),
            Some(Outcome::Shown)
        );
        assert_eq!(
            notifier.handle_event("automatic", CAP),
            Some(Outcome::Duplicate)
        );
        let next_day = CAP.replace("2026-10-21", "2026-10-22");
        assert_eq!(
            notifier.handle_event("automatic", &next_day),
            Some(Outcome::Shown)
        );
        assert_eq!(platform.shown.lock().unwrap().len(), 2);
    }

    #[test]
    fn no_cap_set_no_notification() {
        let (notifier, _, platform) = setup(Permission::Granted);
        let uncapped = CAP.replace(r#""daily_cap":5.0"#, r#""daily_cap":null"#);
        assert_eq!(notifier.handle_event("automatic", &uncapped), None);
        assert!(platform.shown.lock().unwrap().is_empty());
    }

    #[test]
    fn a_focused_window_on_the_same_pull_request_is_not_interrupted() {
        let (notifier, host, platform) = setup(Permission::Granted);
        *host.focused.lock().unwrap() = Some(("c1".into(), 7));
        let same = notice("review_finished", "");
        assert_eq!(
            notifier.handle_event("notice", &same),
            Some(Outcome::Focused)
        );
        // Another pull request, or another clone of it, still notifies.
        let other = same.replace(r#""pr":7"#, r#""pr":8"#);
        assert_eq!(
            notifier.handle_event("notice", &other),
            Some(Outcome::Shown)
        );
        let other_clone = same.replace("c1", "c2");
        assert_eq!(
            notifier.handle_event("notice", &other_clone),
            Some(Outcome::Shown)
        );
        // So does the cap, which belongs to no pull request.
        assert_eq!(
            notifier.handle_event("automatic", CAP),
            Some(Outcome::Shown)
        );
        assert_eq!(platform.shown.lock().unwrap().len(), 3);
        // Once the window loses focus the same pull request notifies again.
        *host.focused.lock().unwrap() = None;
        assert_eq!(notifier.handle_event("notice", &same), Some(Outcome::Shown));
    }

    #[test]
    fn a_kind_switched_off_is_not_shown_and_the_others_are() {
        let (notifier, _, platform) = setup(Permission::Granted);
        notifier.set_kind(Kind::Failed, false);
        let failed = notice("review_failed", "");
        assert_eq!(notifier.handle_event("notice", &failed), Some(Outcome::Off));
        assert_eq!(
            notifier.handle_event("notice", &notice("review_finished", "")),
            Some(Outcome::Shown)
        );
        notifier.set_kind(Kind::Failed, true);
        assert_eq!(
            notifier.handle_event("notice", &failed),
            Some(Outcome::Shown)
        );
        assert_eq!(platform.shown.lock().unwrap().len(), 2);
    }

    #[test]
    fn every_kind_can_be_switched_off() {
        let (notifier, _, platform) = setup(Permission::Granted);
        for kind in Kind::ALL {
            notifier.set_kind(kind, false);
        }
        for (name, data) in [
            ("notice", notice("review_finished", "")),
            ("notice", notice("review_failed", "")),
            ("notice", notice("review_requested", "")),
            ("notice", notice("review_automatic", "")),
            ("automatic", CAP.to_string()),
            ("notice", notice("checks_failed", "")),
            ("notice", notice("conflict", "")),
            ("notice", notice("changes_requested", "")),
            ("notice", notice("ready_to_merge", "")),
            ("notice", notice("fix_pushed", "")),
            ("notice", notice("fix_failed", "")),
        ] {
            assert_eq!(
                notifier.handle_event(name, &data),
                Some(Outcome::Off),
                "{data}"
            );
        }
        assert!(platform.shown.lock().unwrap().is_empty());
        // Switching off asks the operating system for nothing.
        assert_eq!(platform.asked.load(Ordering::SeqCst), 0);
    }

    #[test]
    fn permission_is_asked_on_first_use_only() {
        let (notifier, _, platform) = setup(Permission::Granted);
        assert_eq!(platform.asked.load(Ordering::SeqCst), 0);
        notifier.handle_event("notice", &notice("review_finished", ""));
        notifier.handle_event("notice", &notice("review_failed", ""));
        assert_eq!(platform.asked.load(Ordering::SeqCst), 1);
        assert_eq!(notifier.settings().permission, Permission::Granted);
    }

    #[test]
    fn denied_permission_shows_nothing_and_is_not_asked_again() {
        let (notifier, _, platform) = setup(Permission::Denied);
        let finished = notice("review_finished", "");
        assert_eq!(
            notifier.handle_event("notice", &finished),
            Some(Outcome::Denied)
        );
        assert_eq!(
            notifier.handle_event("notice", &finished),
            Some(Outcome::Denied)
        );
        assert!(platform.shown.lock().unwrap().is_empty());
        assert_eq!(platform.asked.load(Ordering::SeqCst), 1);
        assert_eq!(notifier.settings().permission, Permission::Denied);
    }

    #[test]
    fn switching_a_kind_on_again_asks_again_after_a_refusal() {
        let (notifier, _, platform) = setup(Permission::Denied);
        let finished = notice("review_finished", "");
        notifier.handle_event("notice", &finished);
        notifier.set_kind(Kind::Failed, false);
        assert_eq!(notifier.settings().permission, Permission::Denied);
        notifier.set_kind(Kind::Failed, true);
        assert_eq!(notifier.settings().permission, Permission::Unknown);
        notifier.handle_event("notice", &finished);
        assert_eq!(platform.asked.load(Ordering::SeqCst), 2);
    }

    /// A fake host whose window call waits until `set_kind` (the menu handler,
    /// on another thread) has returned; it deadlocks if `deliver` holds the
    /// settings lock across the call.
    struct BlockingHost {
        release: Mutex<Option<std::sync::mpsc::Receiver<()>>>,
        released: AtomicUsize,
    }

    impl Host for BlockingHost {
        fn focused_pr(&self) -> Option<(String, u64)> {
            let release = self.release.lock().unwrap().take();
            if let Some(release) = release
                && release
                    .recv_timeout(std::time::Duration::from_secs(10))
                    .is_ok()
            {
                self.released.fetch_add(1, Ordering::SeqCst);
            }
            None
        }
        fn open(&self, _: &Route) {}
    }

    #[test]
    fn the_menu_can_change_a_kind_while_a_window_call_is_pending() {
        let (release, wait) = std::sync::mpsc::channel();
        let host = Arc::new(BlockingHost {
            release: Mutex::new(Some(wait)),
            released: AtomicUsize::new(0),
        });
        let platform = FakePlatform::new(Permission::Granted);
        let notifier = Arc::new(Notifier::new(host.clone(), platform, Store::in_memory()));
        let delivering = notifier.clone();
        let worker = std::thread::spawn(move || {
            delivering.handle_event("notice", &notice("review_finished", ""))
        });
        // Let the worker reach the window call, then act like the menu.
        std::thread::sleep(std::time::Duration::from_millis(200));
        notifier.set_kind(Kind::Failed, false);
        release.send(()).unwrap();
        assert_eq!(worker.join().unwrap(), Some(Outcome::Shown));
        assert_eq!(
            host.released.load(Ordering::SeqCst),
            1,
            "set_kind waited for deliver"
        );
    }

    /// The same for the permission request, which can block on a system
    /// prompt.
    struct BlockingPlatform {
        release: Mutex<Option<std::sync::mpsc::Receiver<()>>>,
        released: AtomicUsize,
    }

    impl Platform for BlockingPlatform {
        fn request_permission(&self) -> Permission {
            let release = self.release.lock().unwrap().take();
            if let Some(release) = release
                && release
                    .recv_timeout(std::time::Duration::from_secs(10))
                    .is_ok()
            {
                self.released.fetch_add(1, Ordering::SeqCst);
            }
            Permission::Granted
        }
        fn show(&self, _: &Toast, _: Box<dyn FnOnce() + Send>) -> Result<(), ShowError> {
            Ok(())
        }
    }

    #[test]
    fn the_menu_can_change_a_kind_while_permission_is_being_asked() {
        let (release, wait) = std::sync::mpsc::channel();
        let platform = Arc::new(BlockingPlatform {
            release: Mutex::new(Some(wait)),
            released: AtomicUsize::new(0),
        });
        let notifier = Arc::new(Notifier::new(
            Arc::new(FakeHost::default()),
            platform.clone(),
            Store::in_memory(),
        ));
        let delivering = notifier.clone();
        let worker = std::thread::spawn(move || {
            delivering.handle_event("notice", &notice("review_finished", ""))
        });
        std::thread::sleep(std::time::Duration::from_millis(200));
        notifier.set_kind(Kind::Failed, false);
        release.send(()).unwrap();
        assert_eq!(worker.join().unwrap(), Some(Outcome::Shown));
        assert_eq!(
            platform.released.load(Ordering::SeqCst),
            1,
            "set_kind waited for the request"
        );
    }

    #[test]
    fn a_dedupe_key_is_used_up_only_by_a_toast_that_was_shown() {
        let (notifier, _, platform) = setup(Permission::Denied);
        assert_eq!(
            notifier.handle_event("automatic", CAP),
            Some(Outcome::Denied)
        );
        assert!(notifier.settings().announced.is_empty());
        assert!(platform.shown.lock().unwrap().is_empty());
        // Allowed later, the same pause is still news.
        let granted = FakePlatform::new(Permission::Granted);
        let again = Notifier::new(Arc::new(FakeHost::default()), granted, Store::in_memory());
        assert_eq!(again.handle_event("automatic", CAP), Some(Outcome::Shown));
        assert_eq!(again.settings().announced.len(), 1);
    }

    #[test]
    fn a_pause_is_announced_once_across_restarts() {
        let dir =
            std::env::temp_dir().join(format!("baum-desktop-announced-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        let first = FakePlatform::new(Permission::Granted);
        let n = Notifier::new(
            Arc::new(FakeHost::default()),
            first.clone(),
            Store::in_dir(dir.clone()),
        );
        assert_eq!(n.handle_event("automatic", CAP), Some(Outcome::Shown));
        let second = FakePlatform::new(Permission::Granted);
        let n = Notifier::new(
            Arc::new(FakeHost::default()),
            second.clone(),
            Store::in_dir(dir.clone()),
        );
        assert_eq!(n.handle_event("automatic", CAP), Some(Outcome::Duplicate));
        assert!(second.shown.lock().unwrap().is_empty());
        let next_day = CAP.replace("2026-10-21", "2026-10-22");
        assert_eq!(n.handle_event("automatic", &next_day), Some(Outcome::Shown));
        let _ = std::fs::remove_dir_all(dir);
    }

    #[test]
    fn a_refusal_when_showing_is_remembered() {
        let host = Arc::new(FakeHost::default());
        let platform = Arc::new(FakePlatform {
            refuse: true,
            ..Arc::try_unwrap(FakePlatform::new(Permission::Granted))
                .ok()
                .unwrap()
        });
        let notifier = Notifier::new(host, platform.clone(), Store::in_memory());
        let finished = notice("review_finished", "");
        assert_eq!(
            notifier.handle_event("notice", &finished),
            Some(Outcome::Denied)
        );
        assert_eq!(notifier.settings().permission, Permission::Denied);
        assert_eq!(
            notifier.handle_event("notice", &finished),
            Some(Outcome::Denied)
        );
    }
}
