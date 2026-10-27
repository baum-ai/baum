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
//!
//! The menu and the panel also show what is left of the agents' plans
//! (`GET /api/plan-usage`) and the agent sessions waiting for the user's
//! permission or answer (`GET /api/live-sessions`).

use std::collections::{BTreeSet, HashSet};

use serde::{Deserialize, Serialize};

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
struct WaitingBody {
    waiting: Option<usize>,
}

/// `GET /api/index?view=waiting`: how many pull requests wait on the user
/// (review requested from them, or their own needing changes), counted by the
/// daemon as the app's inbox groups them.
pub fn parse_waiting(body: &[u8]) -> Result<usize, String> {
    let body: WaitingBody =
        serde_json::from_slice(body).map_err(|e| format!("unreadable waiting count: {e}"))?;
    body.waiting
        .ok_or_else(|| "the daemon did not report a waiting count".to_string())
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

/// One window of a plan, as `GET /api/plan-usage` reports it.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct PlanWindow {
    pub name: String,
    /// `None` when the window reset since the figure was taken.
    #[serde(default)]
    pub used_percent: Option<f64>,
    #[serde(default)]
    pub resets_at: Option<String>,
}

/// A plan the daemon could read.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Plan {
    pub provider: String,
    #[serde(default)]
    pub available: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub plan: Option<String>,
    #[serde(default)]
    pub windows: Vec<PlanWindow>,
    #[serde(default)]
    pub fetched_at: String,
}

#[derive(Deserialize)]
struct PlansBody {
    #[serde(default)]
    plans: Vec<serde_json::Value>,
}

/// `GET /api/plan-usage`: the plans it could read, each with at least one
/// window. A provider that is not installed, signed out or has not run yet
/// is left out, as is a plan in a shape this app does not know, so the tray
/// shows only what it can.
pub fn parse_plan_usage(body: &[u8]) -> Result<Vec<Plan>, String> {
    let body: PlansBody =
        serde_json::from_slice(body).map_err(|e| format!("unreadable plan usage: {e}"))?;
    Ok(body
        .plans
        .into_iter()
        .filter_map(|plan| serde_json::from_value::<Plan>(plan).ok())
        .filter(|plan| plan.available && !plan.windows.is_empty())
        .collect())
}

/// One plan window as the menu shows it.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Meter {
    /// "Claude Code · 5-hour".
    pub label: String,
    /// The share left, as a whole percent; `None` after a reset the figure
    /// predates.
    pub left: Option<u8>,
    /// When it resets, in Unix seconds.
    pub resets_at: Option<i64>,
}

impl Meter {
    /// "62% left · resets in 3 h".
    pub fn text(&self, now: i64) -> String {
        let Some(left) = self.left else {
            return "reset since baum last saw it".to_string();
        };
        match self.resets_at {
            Some(at) => format!("{left}% left · resets in {}", span(at - now)),
            None => format!("{left}% left"),
        }
    }
}

fn provider_name(provider: &str) -> &str {
    match provider {
        "claude" => "Claude Code",
        "codex" => "Codex",
        other => other,
    }
}

fn capital(text: &str) -> String {
    let mut chars = text.chars();
    chars.next().map_or_else(String::new, |first| {
        first.to_uppercase().chain(chars).collect()
    })
}

fn meters(plans: &[Plan]) -> Vec<Meter> {
    plans
        .iter()
        .flat_map(|plan| {
            plan.windows.iter().map(|win| Meter {
                label: format!("{} · {}", provider_name(&plan.provider), capital(&win.name)),
                left: win
                    .used_percent
                    .filter(|used| used.is_finite())
                    .map(|used| (100.0 - used).round().clamp(0.0, 100.0) as u8),
                resets_at: win.resets_at.as_deref().and_then(parse_time),
            })
        })
        .collect()
}

/// "3 h", "25 min", "2 d": a span in seconds rounded to a readable unit, as
/// the app rounds it.
pub fn span(seconds: i64) -> String {
    let minutes = (seconds.max(0) as f64 / 60.0).round() as i64;
    if minutes < 60 {
        format!("{} min", minutes.max(1))
    } else if minutes < 48 * 60 {
        format!("{} h", (minutes as f64 / 60.0).round() as i64)
    } else {
        format!("{} d", (minutes as f64 / 1440.0).round() as i64)
    }
}

/// An RFC 3339 time, as Go writes one, in Unix seconds.
pub fn parse_time(text: &str) -> Option<i64> {
    let bytes = text.as_bytes();
    let number = |from: usize, to: usize| -> Option<i64> {
        let part = text.get(from..to)?;
        part.bytes()
            .all(|b| b.is_ascii_digit())
            .then(|| part.parse().ok())?
    };
    if bytes.len() < 20
        || bytes[4] != b'-'
        || bytes[7] != b'-'
        || !matches!(bytes[10], b'T' | b't')
        || bytes[13] != b':'
        || bytes[16] != b':'
    {
        return None;
    }
    let (year, month, day) = (number(0, 4)?, number(5, 7)?, number(8, 10)?);
    let (hour, minute, second) = (number(11, 13)?, number(14, 16)?, number(17, 19)?);
    if !(1..=12).contains(&month) || !(1..=31).contains(&day) || hour > 23 || minute > 59 {
        return None;
    }
    let mut zone = text.get(19..)?;
    if let Some(fraction) = zone.strip_prefix('.') {
        zone = fraction.trim_start_matches(|c: char| c.is_ascii_digit());
    }
    let offset = match zone {
        "Z" | "z" => 0,
        _ if zone.len() == 6 && zone.as_bytes()[3] == b':' => {
            let sign = match zone.as_bytes()[0] {
                b'+' => 1,
                b'-' => -1,
                _ => return None,
            };
            let (h, m) = (
                zone[1..3].parse::<i64>().ok()?,
                zone[4..6].parse::<i64>().ok()?,
            );
            sign * (h * 3600 + m * 60)
        }
        _ => return None,
    };
    // Days since 1970-01-01, by Howard Hinnant's days_from_civil.
    let y = if month <= 2 { year - 1 } else { year };
    let era = y.div_euclid(400);
    let yoe = y - era * 400;
    let doy = (153 * ((month + 9) % 12) + 2) / 5 + day - 1;
    let days = era * 146_097 + yoe * 365 + yoe / 4 - yoe / 100 + doy - 719_468;
    Some(days * 86_400 + hour * 3600 + minute * 60 + second - offset)
}

#[derive(Deserialize)]
struct LiveBody {
    #[serde(default)]
    sessions: Vec<serde_json::Value>,
}

#[derive(Deserialize)]
struct LiveRow {
    agent: String,
    session_id: String,
    #[serde(default)]
    state: String,
    #[serde(default)]
    waiting_reason: Option<String>,
    #[serde(default)]
    waiting_since: Option<String>,
    #[serde(default)]
    waiting_message: Option<String>,
    #[serde(default)]
    title: Option<String>,
    #[serde(default)]
    repository: String,
}

/// An agent session waiting for the user's permission or answer.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct NeedsYou {
    /// The session's `agent:id` key, which opens its transcript.
    pub key: String,
    /// "Claude needs your permission to use Bash", as the agent said it, or
    /// what kind of wait it is.
    pub title: String,
    /// The session's name, else its repository.
    pub detail: String,
}

fn agent_name(agent: &str) -> &str {
    match agent {
        "claude-code" | "claude_code" => "Claude Code",
        "codex" => "Codex",
        "cursor" => "Cursor",
        other => other,
    }
}

/// `GET /api/live-sessions`: the sessions asking for a permission or an
/// answer, the longest wait first. A session that only finished its turn is
/// not one: that is no question, and the Sessions page lists it.
pub fn parse_live(body: &[u8]) -> Result<Vec<NeedsYou>, String> {
    let body: LiveBody =
        serde_json::from_slice(body).map_err(|e| format!("unreadable live sessions: {e}"))?;
    let mut waiting: Vec<(Option<i64>, NeedsYou)> = body
        .sessions
        .into_iter()
        .filter_map(|row| serde_json::from_value::<LiveRow>(row).ok())
        .filter(|row| row.state != "ended" && !row.session_id.is_empty())
        .filter_map(|row| {
            let agent = agent_name(&row.agent).to_string();
            let fallback = match row.waiting_reason.as_deref()? {
                "permission" => format!("{agent} needs your permission"),
                "input" => format!("{agent} is waiting for your answer"),
                _ => return None,
            };
            let title = row
                .waiting_message
                .filter(|m| !m.trim().is_empty())
                .unwrap_or(fallback);
            let detail = row
                .title
                .filter(|t| !t.trim().is_empty())
                .unwrap_or(row.repository);
            let since = row.waiting_since.as_deref().and_then(parse_time);
            Some((
                since,
                NeedsYou {
                    key: format!("{}:{}", row.agent, row.session_id),
                    title,
                    detail,
                },
            ))
        })
        .collect();
    waiting.sort_by(|(a, x), (b, y)| a.cmp(b).then_with(|| x.key.cmp(&y.key)));
    Ok(waiting.into_iter().map(|(_, n)| n).collect())
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
    /// Pull requests waiting on the user; `None` while unknown, such as a
    /// daemon without GitHub discovery.
    pub waiting: Option<usize>,
    /// The plans the daemon could read; `None` until it answered.
    pub plans: Option<Vec<Plan>>,
    /// Agent sessions waiting on the user; `None` while unknown.
    pub needs: Option<Vec<NeedsYou>>,
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
    /// Pull requests waiting on the user while the daemon is up and said so.
    pub waiting: Option<usize>,
    /// Agent sessions waiting for the user's permission or answer.
    pub needs: Vec<NeedsYou>,
    /// One line per readable plan window; empty when none can be read.
    pub usage: Vec<Meter>,
}

impl View {
    /// "3 pull requests waiting on you"; nothing when none wait or it is not
    /// known, which is also when the badge is clear.
    pub fn waiting_label(&self) -> Option<String> {
        match self.waiting? {
            0 => None,
            1 => Some("1 pull request waiting on you".to_string()),
            n => Some(format!("{n} pull requests waiting on you")),
        }
    }

    /// The tray's tooltip: what the daemon is doing, then who waits on whom.
    pub fn tooltip(&self) -> String {
        match self.waiting_label() {
            Some(waiting) => format!("baum: {} · {waiting}", self.summary),
            None => format!("baum: {}", self.summary),
        }
    }

    /// The dock or taskbar badge; `None` clears it, as zero does.
    pub fn badge(&self) -> Option<i64> {
        self.waiting
            .filter(|n| *n > 0)
            .map(|n| i64::try_from(n).unwrap_or(i64::MAX))
    }
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

        let plans = seen.plans.as_deref().filter(|_| seen.link == Link::Up);
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
            // A daemon that is not running has nobody waiting on anything.
            waiting: seen.waiting.filter(|_| seen.link == Link::Up),
            needs: seen
                .needs
                .clone()
                .filter(|_| seen.link == Link::Up)
                .unwrap_or_default(),
            usage: plans.map(meters).unwrap_or_default(),
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
            waiting: None,
            plans: None,
            needs: None,
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
    fn the_waiting_count_reads_the_daemons_answer() {
        assert_eq!(parse_waiting(br#"{"waiting":3}"#), Ok(3));
        assert_eq!(parse_waiting(br#"{"waiting":0}"#), Ok(0));
        assert!(parse_waiting(br#"{"entries":[]}"#).is_err());
        assert!(parse_waiting(b"nope").is_err());
    }

    #[test]
    fn waiting_pull_requests_show_in_the_tooltip_and_the_badge() {
        let waiting = |link, waiting| {
            Tracker::default().update(&Observed {
                link,
                waiting,
                ..Observed::default()
            })
        };
        let view = waiting(Link::Up, Some(3));
        assert_eq!(
            view.waiting_label().as_deref(),
            Some("3 pull requests waiting on you")
        );
        assert_eq!(
            view.tooltip(),
            "baum: Checking background reviews · 3 pull requests waiting on you"
        );
        assert_eq!(view.badge(), Some(3));
        let one = waiting(Link::Up, Some(1));
        assert_eq!(
            one.waiting_label().as_deref(),
            Some("1 pull request waiting on you")
        );
        // Zero clears the badge and says nothing; so does not knowing, and a
        // daemon that is not running.
        for view in [
            waiting(Link::Up, Some(0)),
            waiting(Link::Up, None),
            waiting(Link::Down, Some(5)),
            waiting(Link::Connecting, Some(5)),
        ] {
            assert_eq!(view.waiting_label(), None);
            assert_eq!(view.badge(), None);
            assert!(!view.tooltip().contains("waiting"), "{}", view.tooltip());
        }
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

    const PLANS: &[u8] = br#"{"plans":[
        {"provider":"claude","available":true,"windows":[
            {"name":"5-hour","used_percent":38,"resets_at":"2026-10-26T15:00:00Z"},
            {"name":"weekly","used_percent":74.4,"resets_at":"2026-10-28T12:00:00.123+01:00"}],
         "fetched_at":"2026-10-26T12:00:00Z"},
        {"provider":"codex","available":true,"plan":"pro","windows":[
            {"name":"weekly","used_percent":null,"resets_at":"2026-10-26T10:00:00Z","reset":true}],
         "fetched_at":"2026-10-26T11:40:00Z"}]}"#;

    #[test]
    fn plan_windows_become_menu_lines() {
        let mut observed = up(jobs(0, 0, &[]));
        observed.plans = Some(parse_plan_usage(PLANS).unwrap());
        let view = Tracker::default().update(&observed);
        let now = parse_time("2026-10-26T12:00:00Z").unwrap();
        let lines: Vec<(String, String)> = view
            .usage
            .iter()
            .map(|m| (m.label.clone(), m.text(now)))
            .collect();
        assert_eq!(
            lines,
            vec![
                (
                    "Claude Code · 5-hour".into(),
                    "62% left · resets in 3 h".into()
                ),
                (
                    "Claude Code · Weekly".into(),
                    "26% left · resets in 47 h".into()
                ),
                (
                    "Codex · Weekly".into(),
                    "reset since baum last saw it".into()
                ),
            ]
        );
        // A daemon that is not running vouches for no figures.
        observed.link = Link::Down;
        assert!(Tracker::default().update(&observed).usage.is_empty());
    }

    #[test]
    fn only_plans_that_could_be_read_are_kept() {
        let body = br#"{"plans":[
            {"provider":"claude","available":false,"reason":"Claude Code is not signed in on this computer.","windows":[],"fetched_at":"2026-10-26T12:00:00Z"},
            {"provider":"codex","available":true,"windows":[],"fetched_at":"2026-10-26T12:00:00Z"},
            {"provider":"codex","available":true,"windows":"weekly"},
            42]}"#;
        assert_eq!(parse_plan_usage(body).unwrap(), Vec::new());
        assert_eq!(parse_plan_usage(br#"{}"#).unwrap(), Vec::new());
        assert!(parse_plan_usage(b"not json").is_err());
        let mut observed = up(jobs(0, 0, &[]));
        observed.plans = Some(parse_plan_usage(body).unwrap());
        let view = Tracker::default().update(&observed);
        assert!(view.usage.is_empty());
    }

    #[test]
    fn rfc3339_times_parse_with_any_offset() {
        assert_eq!(parse_time("1970-01-01T00:00:00Z"), Some(0));
        assert_eq!(parse_time("2026-10-26T12:00:00Z"), Some(1_793_016_000));
        assert_eq!(
            parse_time("2026-10-26T13:30:00.5+01:30"),
            parse_time("2026-10-26T12:00:00Z")
        );
        assert_eq!(parse_time("2024-02-29T00:00:00-00:00"), Some(1_709_164_800));
        for bad in [
            "",
            "2026-10-26",
            "2026-13-01T00:00:00Z",
            "2026-10-26T12:00:00",
            "2026-10-26T12:00:00+0100",
        ] {
            assert_eq!(parse_time(bad), None, "{bad}");
        }
    }

    #[test]
    fn spans_round_as_the_app_rounds_them() {
        assert_eq!(span(10), "1 min");
        assert_eq!(span(25 * 60), "25 min");
        assert_eq!(span(5 * 3600), "5 h");
        assert_eq!(span(72 * 3600), "3 d");
        assert_eq!(span(-5), "1 min");
    }

    #[test]
    fn sessions_asking_for_permission_or_an_answer_need_the_user() {
        let body = br#"{"sessions":[
            {"agent":"claude-code","session_id":"b","state":"idle","waiting_reason":"input","waiting_since":"2026-10-26T12:05:00Z","repository":"acme/demo"},
            {"agent":"claude-code","session_id":"a","state":"running","waiting_reason":"permission","waiting_since":"2026-10-26T12:00:00Z","waiting_message":"Claude needs your permission to use Bash","title":"Tidy the app's buttons","repository":"acme/demo"},
            {"agent":"codex","session_id":"c","state":"idle","waiting_reason":"turn_finished","repository":"acme/demo"},
            {"agent":"codex","session_id":"d","state":"ended","waiting_reason":"permission","repository":"acme/demo"},
            {"agent":"codex","session_id":"e","state":"running","repository":"acme/demo"},
            {"session_id":"f"}]}"#;
        assert_eq!(
            parse_live(body).unwrap(),
            vec![
                NeedsYou {
                    key: "claude-code:a".into(),
                    title: "Claude needs your permission to use Bash".into(),
                    detail: "Tidy the app's buttons".into(),
                },
                NeedsYou {
                    key: "claude-code:b".into(),
                    title: "Claude Code is waiting for your answer".into(),
                    detail: "acme/demo".into(),
                },
            ]
        );
        assert_eq!(parse_live(br#"{"available":false}"#).unwrap(), Vec::new());
        let mut observed = up(jobs(0, 0, &[]));
        observed.needs = Some(parse_live(body).unwrap());
        assert_eq!(Tracker::default().update(&observed).needs.len(), 2);
        observed.link = Link::Down;
        assert!(Tracker::default().update(&observed).needs.is_empty());
    }
}
