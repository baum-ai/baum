//! What the tray shows, computed from what the daemon reports. Nothing here
//! touches the network or the window: the daemon's JSON goes in and a `View`
//! comes out, so the rules are unit-tested without either.
//!
//! The icon has three states. Idle is the default. Running means a job is
//! running or queued. Attention means something needs the user and they have
//! not yet opened the window since it appeared: a job failed, a repository's
//! daily cap paused its automatic generation, a diagnostics check reports a
//! problem, or the daemon is not running. Opening the window acknowledges
//! the first three; the last stays until the daemon runs again.

use std::collections::{BTreeSet, HashSet};

use serde::Deserialize;

#[derive(Deserialize)]
struct JobsBody {
    #[serde(default)]
    jobs: Vec<JobRow>,
}

#[derive(Deserialize)]
struct JobRow {
    #[serde(default)]
    id: String,
    #[serde(default)]
    state: String,
}

/// The queue as the tray needs it.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Jobs {
    pub running: usize,
    pub queued: usize,
    /// Ids of failed jobs. The daemon keeps finished jobs in its journal.
    pub failed: Vec<String>,
}

/// `GET /api/jobs`. A daemon without a runner answers `{"available":false}`,
/// which has no jobs.
pub fn parse_jobs(body: &[u8]) -> Result<Jobs, String> {
    let body: JobsBody =
        serde_json::from_slice(body).map_err(|e| format!("unreadable jobs list: {e}"))?;
    let mut jobs = Jobs::default();
    for row in body.jobs {
        match row.state.as_str() {
            "running" => jobs.running += 1,
            "queued" => jobs.queued += 1,
            "failed" => jobs.failed.push(row.id),
            _ => {}
        }
    }
    Ok(jobs)
}

#[derive(Deserialize)]
struct AutomaticBody {
    enabled: Option<bool>,
    #[serde(default)]
    repositories: Vec<RepositoryRow>,
}

#[derive(Deserialize)]
struct RepositoryRow {
    #[serde(default)]
    repository: String,
    #[serde(default)]
    enabled: bool,
    #[serde(default)]
    notice: String,
    #[serde(default)]
    paused_until: Option<String>,
}

/// Automatic generation as the tray needs it.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Automatic {
    /// The global switch.
    pub enabled: bool,
    /// Repositories whose daily cap paused automatic generation.
    pub capped: Vec<String>,
}

/// `GET /api/automatic`. `None` when the daemon has no scheduler.
pub fn parse_automatic(body: &[u8]) -> Result<Option<Automatic>, String> {
    let body: AutomaticBody =
        serde_json::from_slice(body).map_err(|e| format!("unreadable automatic status: {e}"))?;
    let Some(enabled) = body.enabled else {
        return Ok(None);
    };
    let capped = body
        .repositories
        .iter()
        .filter(|r| r.enabled && (r.paused_until.is_some() || r.notice == "daily_cap"))
        .map(|r| r.repository.clone())
        .collect();
    Ok(Some(Automatic { enabled, capped }))
}

#[derive(Deserialize)]
struct DiagnosticsBody {
    report: Option<ReportRow>,
}

#[derive(Deserialize)]
struct ReportRow {
    #[serde(default)]
    status: String,
    #[serde(default)]
    checks: Vec<CheckRow>,
}

#[derive(Deserialize)]
struct CheckRow {
    #[serde(default)]
    id: String,
    #[serde(default)]
    status: String,
}

/// The ids of the checks `GET /api/diagnostics` reports as problems
/// (warnings do not count). A report that is a problem without naming a check
/// counts as one problem named "report".
pub fn parse_diagnostics(body: &[u8]) -> Result<Vec<String>, String> {
    let body: DiagnosticsBody =
        serde_json::from_slice(body).map_err(|e| format!("unreadable diagnostics: {e}"))?;
    let Some(report) = body.report else {
        return Ok(Vec::new());
    };
    let mut problems: Vec<String> = report
        .checks
        .into_iter()
        .filter(|c| c.status == "problem")
        .map(|c| c.id)
        .collect();
    if problems.is_empty() && report.status == "problem" {
        problems.push("report".to_string());
    }
    Ok(problems)
}

/// The body of the request that sets the global automatic switch.
pub fn automatic_request(enabled: bool) -> String {
    format!("{{\"enabled\":{enabled}}}")
}

/// Whether the app is attached to a running daemon.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum Link {
    #[default]
    Connecting,
    Up,
    Down,
}

/// Everything last observed.
#[derive(Debug, Clone, Default)]
pub struct Observed {
    pub link: Link,
    pub jobs: Option<Jobs>,
    pub automatic: Option<Automatic>,
    /// Ids of the checks reporting a problem; `None` until diagnostics ran.
    pub diagnostics: Option<Vec<String>>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Icon {
    Idle,
    Running,
    Attention,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct View {
    pub icon: Icon,
    /// The first menu line and the tooltip: what the daemon is doing.
    pub summary: String,
    /// One line per thing needing the user; empty unless the icon is Attention.
    pub attention: Vec<String>,
    /// The global automatic switch; `None` while unknown, which disables the
    /// pause item.
    pub automatic: Option<bool>,
    pub running: usize,
    pub queued: usize,
}

impl Default for View {
    fn default() -> Self {
        Tracker::default().update(&Observed::default())
    }
}

/// Remembers what the user has already seen, so Attention clears when they
/// open the window and returns only for something new.
#[derive(Debug, Default)]
pub struct Tracker {
    acknowledged: HashSet<String>,
    /// Failed jobs found on the first look are old news.
    seeded: bool,
    current: BTreeSet<String>,
}

impl Tracker {
    pub fn update(&mut self, seen: &Observed) -> View {
        let running = seen.jobs.as_ref().map_or(0, |j| j.running);
        let queued = seen.jobs.as_ref().map_or(0, |j| j.queued);
        let mut keys = BTreeSet::new();
        if let Some(jobs) = &seen.jobs {
            keys.extend(jobs.failed.iter().map(|id| format!("failed:{id}")));
            if !self.seeded {
                self.acknowledged.extend(keys.iter().cloned());
                self.seeded = true;
            }
        }
        if let Some(automatic) = seen.automatic.as_ref().filter(|a| a.enabled) {
            keys.extend(automatic.capped.iter().map(|r| format!("cap:{r}")));
        }
        if let Some(problems) = &seen.diagnostics {
            keys.extend(problems.iter().map(|id| format!("diagnostics:{id}")));
        }
        // Anything that went away and comes back is new again. What a report
        // we do not have (a failed poll) would have said is not forgotten.
        self.acknowledged.retain(|k| {
            keys.contains(k)
                || (k.starts_with("failed:") && seen.jobs.is_none())
                || (k.starts_with("cap:") && seen.automatic.is_none())
                || (k.starts_with("diagnostics:") && seen.diagnostics.is_none())
        });
        let unseen: Vec<&String> = keys
            .iter()
            .filter(|k| !self.acknowledged.contains(*k))
            .collect();
        self.current = keys.clone();

        let mut attention = Vec::new();
        let count = |prefix: &str| unseen.iter().filter(|k| k.starts_with(prefix)).count();
        let failed = count("failed:");
        if failed > 0 {
            attention.push(count_label(failed, "review failed", "reviews failed"));
        }
        match count("cap:") {
            0 => {}
            1 => attention.push("Daily cap reached; automatic reviews paused".to_string()),
            n => attention.push(format!(
                "Daily cap reached in {n} repositories; automatic reviews paused"
            )),
        }
        match count("diagnostics:") {
            0 => {}
            1 => attention.push("A diagnostics check reports a problem".to_string()),
            n => attention.push(format!("{n} diagnostics checks report a problem")),
        }

        let (icon, summary) = match seen.link {
            Link::Connecting => (Icon::Idle, "Connecting to the baum daemon".to_string()),
            Link::Down => (
                Icon::Attention,
                "Background reviews are not running".to_string(),
            ),
            Link::Up if !attention.is_empty() => (Icon::Attention, summary(running, queued)),
            Link::Up if seen.jobs.is_none() => {
                (Icon::Idle, "Checking background reviews".to_string())
            }
            Link::Up if running + queued > 0 => (Icon::Running, summary(running, queued)),
            Link::Up => (Icon::Idle, summary(running, queued)),
        };
        View {
            icon,
            summary,
            attention,
            automatic: seen.automatic.as_ref().map(|a| a.enabled),
            running,
            queued,
        }
    }

    /// The user opened the window: what is showing now has been seen.
    pub fn acknowledge(&mut self) {
        self.acknowledged.extend(self.current.iter().cloned());
    }
}

fn count_label(n: usize, one: &str, many: &str) -> String {
    format!("{n} {}", if n == 1 { one } else { many })
}

fn summary(running: usize, queued: usize) -> String {
    match (running, queued) {
        (0, 0) => "Idle".to_string(),
        (r, 0) => format!("{r} running"),
        (0, q) => format!("{q} queued"),
        (r, q) => format!("{r} running, {q} queued"),
    }
}

/// What the pause item says, from the global switch.
pub fn pause_label(automatic: Option<bool>) -> &'static str {
    match automatic {
        Some(false) => "Resume automatic reviews",
        _ => "Pause automatic reviews",
    }
}

/// The value a click on the pause item sets: the opposite of what the daemon
/// last reported. `None` while that is unknown.
pub fn toggled(automatic: Option<bool>) -> Option<bool> {
    automatic.map(|enabled| !enabled)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn jobs(running: usize, queued: usize, failed: &[&str]) -> Jobs {
        Jobs {
            running,
            queued,
            failed: failed.iter().map(|s| s.to_string()).collect(),
        }
    }

    fn up(jobs: Jobs) -> Observed {
        Observed {
            link: Link::Up,
            jobs: Some(jobs),
            automatic: Some(Automatic {
                enabled: true,
                capped: Vec::new(),
            }),
            diagnostics: Some(Vec::new()),
        }
    }

    #[test]
    fn parses_job_counts_and_failures() {
        let body = br#"{"available":true,"jobs":[
            {"id":"a","state":"running","phase":"x"},
            {"id":"b","state":"queued"},{"id":"c","state":"queued"},
            {"id":"d","state":"failed","reason":"boom"},
            {"id":"e","state":"completed"},{"id":"f","state":"cancelled"},{"id":"g","state":"interrupted"}]}"#;
        assert_eq!(parse_jobs(body).unwrap(), jobs(1, 2, &["d"]));
        assert_eq!(
            parse_jobs(br#"{"available":false,"code":"runner_unavailable"}"#).unwrap(),
            Jobs::default()
        );
        assert!(parse_jobs(b"not json").is_err());
    }

    #[test]
    fn parses_the_global_switch_and_capped_repositories() {
        let body = br#"{"enabled":false,"cursor_agent_acknowledged":false,"repositories":[
            {"repository":"a/b","enabled":true,"paused_until":"2026-10-21T00:00:00Z","notice":"daily_cap","pending":0},
            {"repository":"a/c","enabled":true,"notice":"unavailable","pending":0},
            {"repository":"a/d","enabled":false,"notice":"daily_cap","pending":0}]}"#;
        assert_eq!(
            parse_automatic(body).unwrap(),
            Some(Automatic {
                enabled: false,
                capped: vec!["a/b".to_string()]
            })
        );
        assert_eq!(
            parse_automatic(br#"{"available":false,"code":"automatic_unavailable"}"#).unwrap(),
            None
        );
    }

    #[test]
    fn only_a_problem_counts_as_a_diagnostics_problem() {
        let report = |s| format!(r#"{{"local":true,"report":{{"status":"{s}","checks":[]}}}}"#);
        assert_eq!(
            parse_diagnostics(report("problem").as_bytes()).unwrap(),
            vec!["report"]
        );
        assert!(
            parse_diagnostics(report("warning").as_bytes())
                .unwrap()
                .is_empty()
        );
        assert!(
            parse_diagnostics(report("ok").as_bytes())
                .unwrap()
                .is_empty()
        );
        assert!(
            parse_diagnostics(br#"{"local":true,"report":null}"#)
                .unwrap()
                .is_empty()
        );
        let checks = br#"{"report":{"status":"problem","checks":[
            {"id":"git","status":"problem"},{"id":"gh","status":"warning"},{"id":"agent","status":"problem"}]}}"#;
        assert_eq!(parse_diagnostics(checks).unwrap(), vec!["git", "agent"]);
    }

    #[test]
    fn idle_running_and_queued() {
        let mut tracker = Tracker::default();
        let view = tracker.update(&up(jobs(0, 0, &[])));
        assert_eq!((view.icon, view.summary.as_str()), (Icon::Idle, "Idle"));
        let view = tracker.update(&up(jobs(1, 0, &[])));
        assert_eq!(
            (view.icon, view.summary.as_str()),
            (Icon::Running, "1 running")
        );
        let view = tracker.update(&up(jobs(2, 3, &[])));
        assert_eq!(view.summary, "2 running, 3 queued");
        let view = tracker.update(&up(jobs(0, 2, &[])));
        assert_eq!(
            (view.icon, view.summary.as_str()),
            (Icon::Running, "2 queued")
        );
    }

    #[test]
    fn a_new_failure_needs_attention_until_the_window_opens() {
        let mut tracker = Tracker::default();
        tracker.update(&up(jobs(0, 0, &["old"])));
        let view = tracker.update(&up(jobs(0, 0, &["old", "new"])));
        assert_eq!(view.icon, Icon::Attention);
        assert_eq!(view.attention, vec!["1 review failed"]);
        tracker.acknowledge();
        let view = tracker.update(&up(jobs(0, 0, &["old", "new"])));
        assert_eq!(view.icon, Icon::Idle);
        assert!(view.attention.is_empty());
        let view = tracker.update(&up(jobs(0, 0, &["old", "new", "newer"])));
        assert_eq!(view.icon, Icon::Attention);
    }

    #[test]
    fn failures_found_on_the_first_look_are_not_new() {
        let mut tracker = Tracker::default();
        let view = tracker.update(&up(jobs(0, 0, &["a", "b"])));
        assert_eq!(view.icon, Icon::Idle);
    }

    #[test]
    fn attention_wins_over_running_and_keeps_the_job_summary() {
        let mut tracker = Tracker::default();
        tracker.update(&up(jobs(0, 0, &[])));
        let view = tracker.update(&up(jobs(1, 1, &["x", "y"])));
        assert_eq!(view.icon, Icon::Attention);
        assert_eq!(view.summary, "1 running, 1 queued");
        assert_eq!(view.attention, vec!["2 reviews failed"]);
    }

    #[test]
    fn a_daily_cap_needs_attention_only_while_automatic_is_on() {
        let mut tracker = Tracker::default();
        let mut seen = up(jobs(0, 0, &[]));
        seen.automatic = Some(Automatic {
            enabled: true,
            capped: vec!["a/b".into()],
        });
        let view = tracker.update(&seen);
        assert_eq!(view.icon, Icon::Attention);
        assert_eq!(
            view.attention,
            vec!["Daily cap reached; automatic reviews paused"]
        );
        seen.automatic = Some(Automatic {
            enabled: false,
            capped: vec!["a/b".into()],
        });
        assert_eq!(tracker.update(&seen).icon, Icon::Idle);
    }

    #[test]
    fn each_capped_repository_and_each_problem_is_its_own_attention() {
        let mut tracker = Tracker::default();
        let mut seen = up(jobs(0, 0, &[]));
        seen.automatic = Some(Automatic {
            enabled: true,
            capped: vec!["a/b".into()],
        });
        seen.diagnostics = Some(vec!["git".into()]);
        tracker.update(&seen);
        tracker.acknowledge();
        assert_eq!(tracker.update(&seen).icon, Icon::Idle);
        // A second repository and a second problem are new.
        seen.automatic = Some(Automatic {
            enabled: true,
            capped: vec!["a/b".into(), "a/c".into()],
        });
        seen.diagnostics = Some(vec!["git".into(), "gh".into()]);
        let view = tracker.update(&seen);
        assert_eq!(
            view.attention,
            vec![
                "Daily cap reached; automatic reviews paused",
                "A diagnostics check reports a problem"
            ]
        );
    }

    #[test]
    fn a_failed_poll_does_not_make_old_failures_new() {
        let mut tracker = Tracker::default();
        tracker.update(&up(jobs(0, 0, &["old"])));
        let mut gap = up(jobs(0, 0, &[]));
        gap.jobs = None;
        gap.automatic = None;
        let view = tracker.update(&gap);
        assert_eq!(view.summary, "Checking background reviews");
        assert_eq!(view.automatic, None);
        let view = tracker.update(&up(jobs(0, 0, &["old"])));
        assert_eq!(view.icon, Icon::Idle);
    }

    #[test]
    fn a_diagnostics_problem_needs_attention_and_returns_after_it_clears() {
        let mut tracker = Tracker::default();
        let mut seen = up(jobs(0, 0, &[]));
        seen.diagnostics = Some(vec!["git".into()]);
        assert_eq!(tracker.update(&seen).icon, Icon::Attention);
        tracker.acknowledge();
        assert_eq!(tracker.update(&seen).icon, Icon::Idle);
        seen.diagnostics = Some(Vec::new());
        tracker.update(&seen);
        seen.diagnostics = Some(vec!["git".into()]);
        assert_eq!(tracker.update(&seen).icon, Icon::Attention);
    }

    #[test]
    fn a_missing_daemon_needs_attention_and_connecting_does_not() {
        let mut tracker = Tracker::default();
        let view = tracker.update(&Observed {
            link: Link::Down,
            ..Observed::default()
        });
        assert_eq!(view.icon, Icon::Attention);
        assert_eq!(view.summary, "Background reviews are not running");
        tracker.acknowledge();
        let view = tracker.update(&Observed {
            link: Link::Down,
            ..Observed::default()
        });
        assert_eq!(view.icon, Icon::Attention);
        let view = tracker.update(&Observed::default());
        assert_eq!(view.icon, Icon::Idle);
    }

    #[test]
    fn the_pause_item_follows_the_global_switch() {
        assert_eq!(pause_label(Some(true)), "Pause automatic reviews");
        assert_eq!(pause_label(Some(false)), "Resume automatic reviews");
        assert_eq!(toggled(Some(true)), Some(false));
        assert_eq!(toggled(Some(false)), Some(true));
        assert_eq!(toggled(None), None);
        assert_eq!(automatic_request(false), r#"{"enabled":false}"#);
        assert_eq!(automatic_request(true), r#"{"enabled":true}"#);
    }
}
