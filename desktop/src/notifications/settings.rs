//! The notification settings the app keeps in its config directory: which
//! kinds are on (all, by default) and what the operating system answered when
//! the app first asked to notify. Nothing here is secret.

use std::fs;
use std::io;
use std::path::PathBuf;

use serde::{Deserialize, Serialize};

use super::toast::Kind;

const FILE: &str = "notifications.json";

/// What the operating system lets the app do.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Permission {
    /// Not asked yet: the app asks when it first has something to show.
    #[default]
    Unknown,
    Granted,
    Denied,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Kinds {
    pub finished: bool,
    pub failed: bool,
    pub requested: bool,
    pub cap_reached: bool,
    pub automatic: bool,
    pub checks_failed: bool,
    pub conflict: bool,
    pub changes_requested: bool,
    pub ready_to_merge: bool,
    pub fix_pushed: bool,
    pub fix_failed: bool,
    pub session_permission: bool,
    pub session_input: bool,
}

impl Default for Kinds {
    fn default() -> Self {
        Kinds {
            finished: true,
            failed: true,
            requested: true,
            cap_reached: true,
            automatic: true,
            checks_failed: true,
            conflict: true,
            changes_requested: true,
            ready_to_merge: true,
            fix_pushed: true,
            fix_failed: true,
            session_permission: true,
            session_input: true,
        }
    }
}

impl Kinds {
    pub fn get(&self, kind: Kind) -> bool {
        match kind {
            Kind::Finished => self.finished,
            Kind::Failed => self.failed,
            Kind::Requested => self.requested,
            Kind::CapReached => self.cap_reached,
            Kind::Automatic => self.automatic,
            Kind::ChecksFailed => self.checks_failed,
            Kind::Conflict => self.conflict,
            Kind::ChangesRequested => self.changes_requested,
            Kind::ReadyToMerge => self.ready_to_merge,
            Kind::FixPushed => self.fix_pushed,
            Kind::FixFailed => self.fix_failed,
            Kind::SessionPermission => self.session_permission,
            Kind::SessionInput => self.session_input,
        }
    }

    pub fn set(&mut self, kind: Kind, on: bool) {
        match kind {
            Kind::Finished => self.finished = on,
            Kind::Failed => self.failed = on,
            Kind::Requested => self.requested = on,
            Kind::CapReached => self.cap_reached = on,
            Kind::Automatic => self.automatic = on,
            Kind::ChecksFailed => self.checks_failed = on,
            Kind::Conflict => self.conflict = on,
            Kind::ChangesRequested => self.changes_requested = on,
            Kind::ReadyToMerge => self.ready_to_merge = on,
            Kind::FixPushed => self.fix_pushed = on,
            Kind::FixFailed => self.fix_failed = on,
            Kind::SessionPermission => self.session_permission = on,
            Kind::SessionInput => self.session_input = on,
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct Settings {
    pub kinds: Kinds,
    pub permission: Permission,
    /// Keys of the notifications that repeat while their condition lasts (the
    /// daily cap, one key per repository and pause) already shown, oldest
    /// first, so a restart or a replaced daemon does not announce one again.
    pub announced: Vec<String>,
}

/// Where the settings live; `None` keeps them in memory only.
#[derive(Debug, Clone, Default)]
pub struct Store {
    path: Option<PathBuf>,
}

impl Store {
    pub fn in_dir(dir: PathBuf) -> Store {
        Store {
            path: Some(dir.join(FILE)),
        }
    }

    #[cfg(test)]
    pub fn in_memory() -> Store {
        Store { path: None }
    }

    /// The saved settings; a missing or unreadable file is the defaults, so
    /// a damaged file never keeps the app from notifying.
    pub fn load(&self) -> Settings {
        let Some(path) = &self.path else {
            return Settings::default();
        };
        match fs::read(path) {
            Ok(data) => serde_json::from_slice(&data).unwrap_or_else(|e| {
                eprintln!("baum desktop: notification settings unreadable, using defaults: {e}");
                Settings::default()
            }),
            Err(_) => Settings::default(),
        }
    }

    pub fn save(&self, settings: &Settings) -> io::Result<()> {
        let Some(path) = &self.path else {
            return Ok(());
        };
        if let Some(dir) = path.parent() {
            fs::create_dir_all(dir)?;
        }
        let temporary = path.with_extension("json.tmp");
        fs::write(&temporary, serde_json::to_vec_pretty(settings)?)?;
        fs::rename(&temporary, path)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn scratch(name: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!("baum-desktop-{name}-{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        dir
    }

    #[test]
    fn everything_is_on_and_unasked_by_default() {
        let settings = Store::in_dir(scratch("default")).load();
        assert!(Kind::ALL.into_iter().all(|k| settings.kinds.get(k)));
        assert_eq!(settings.permission, Permission::Unknown);
    }

    #[test]
    fn settings_survive_a_restart() {
        let dir = scratch("roundtrip");
        let store = Store::in_dir(dir.clone());
        let mut settings = store.load();
        settings.kinds.set(Kind::Automatic, false);
        settings.permission = Permission::Denied;
        store.save(&settings).unwrap();
        let again = Store::in_dir(dir.clone()).load();
        assert_eq!(again, settings);
        assert!(!again.kinds.get(Kind::Automatic) && again.kinds.get(Kind::Failed));
        let _ = fs::remove_dir_all(dir);
    }

    #[test]
    fn a_new_kind_missing_from_an_old_file_is_on() {
        let dir = scratch("old");
        fs::create_dir_all(&dir).unwrap();
        fs::write(dir.join(FILE), r#"{"kinds":{"failed":false}}"#).unwrap();
        let settings = Store::in_dir(dir.clone()).load();
        assert!(!settings.kinds.failed && settings.kinds.finished && settings.kinds.cap_reached);
        // The kinds added after this file was written are on.
        assert!(
            [
                Kind::ChecksFailed,
                Kind::Conflict,
                Kind::ChangesRequested,
                Kind::ReadyToMerge,
                Kind::FixPushed,
                Kind::FixFailed,
                Kind::SessionPermission,
                Kind::SessionInput
            ]
            .into_iter()
            .all(|k| settings.kinds.get(k))
        );
        let _ = fs::remove_dir_all(dir);
    }

    #[test]
    fn a_file_from_before_the_session_kinds_keeps_its_choices() {
        let dir = scratch("before-sessions");
        fs::create_dir_all(&dir).unwrap();
        // Every kind the previous release had, one of them off.
        fs::write(
            dir.join(FILE),
            r#"{"kinds":{"finished":true,"failed":true,"requested":true,"cap_reached":true,"automatic":true,"checks_failed":true,"conflict":false,"changes_requested":true,"ready_to_merge":true,"fix_pushed":true,"fix_failed":true},"permission":"granted","announced":["cap:acme/widgets:2026-10-21T00:00:00Z"]}"#,
        )
        .unwrap();
        let settings = Store::in_dir(dir.clone()).load();
        assert!(!settings.kinds.conflict && settings.kinds.fix_failed);
        assert!(settings.kinds.session_permission && settings.kinds.session_input);
        assert_eq!(settings.permission, Permission::Granted);
        assert_eq!(settings.announced.len(), 1);
        let _ = fs::remove_dir_all(dir);
    }

    #[test]
    fn a_damaged_file_is_the_defaults() {
        let dir = scratch("damaged");
        fs::create_dir_all(&dir).unwrap();
        fs::write(dir.join(FILE), "{").unwrap();
        assert_eq!(Store::in_dir(dir.clone()).load(), Settings::default());
        let _ = fs::remove_dir_all(dir);
    }
}
