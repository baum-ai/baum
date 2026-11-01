package analytics

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/repos"
)

// FlowReport is the pull request flow of the repositories in scope, from the
// feature log's pull requests: per local day, those opened and those merged,
// the person's own and everyone's.
type FlowReport struct {
	// Available is false without the decision store the feature log keeps.
	Available bool `json:"available"`
	// Yours is false when the person's GitHub login is not known, so their
	// own pull requests cannot be told from the team's.
	Yours bool `json:"yours"`
	// Unlisted names the repositories in scope the feature log has not
	// listed: those with decisions off, or no clone.
	Unlisted []string `json:"unlisted"`
	// Unread names those whose pull requests could not be read.
	Unread []string  `json:"unread"`
	Days   []FlowDay `json:"days"`
}

// FlowDay is one local day's flow.
type FlowDay struct {
	Date string     `json:"date"`
	Mine FlowCounts `json:"mine"`
	Team FlowCounts `json:"team"`
}

// FlowCounts counts a day's pull requests not opened by a bot. Opened leaves
// drafts out; OpenedMerged is how many of those opened have merged since, for
// the merge rate; Merged counts those merged that day, whenever they opened,
// with each one's time to merge.
type FlowCounts struct {
	Opened       int       `json:"opened"`
	OpenedMerged int       `json:"opened_merged"`
	Merged       int       `json:"merged"`
	MergeSeconds []float64 `json:"merge_seconds"`
}

func (s *Service) flow(ctx context.Context, start, today time.Time, loc *time.Location, q Query, picked []repos.Repository) FlowReport {
	report := FlowReport{Unlisted: []string{}, Unread: []string{}, Days: []FlowDay{}}
	if s.Pulls == nil {
		return report
	}
	report.Available = true
	login := ""
	if s.Login != nil {
		login = s.Login(ctx)
	}
	report.Yours = login != ""
	days := map[string]*FlowDay{}
	for d := start; !d.After(today); d = d.AddDate(0, 0, 1) {
		report.Days = append(report.Days, FlowDay{Date: d.Format(time.DateOnly), Mine: FlowCounts{MergeSeconds: []float64{}}, Team: FlowCounts{MergeSeconds: []float64{}}})
	}
	for i := range report.Days {
		days[report.Days[i].Date] = &report.Days[i]
	}
	// The feature log keeps a repository's pull requests current only while
	// it reads it, as FeatureWorker does: decisions on and a clone at hand.
	// Rows it kept from before would be stale, so such a repository is not
	// counted.
	reads := func(r repos.Repository) bool {
		return (r.Settings.Decisions == nil || *r.Settings.Decisions) && slices.ContainsFunc(r.Clones, func(c repos.Clone) bool { return c.Available })
	}
	if p, ok := s.Registry.(interface {
		Preferences(context.Context) (repos.Preferences, error)
	}); ok {
		if prefs, err := p.Preferences(ctx); err == nil {
			base := reads
			reads = func(r repos.Repository) bool { return r.Settings.DecisionsEnabled(prefs) && base(r) }
		}
	}
	for _, r := range picked {
		name := strings.ToLower(r.Repository)
		if q.Repository != "" && name != q.Repository {
			continue
		}
		if !reads(r) {
			report.Unlisted = append(report.Unlisted, name)
			continue
		}
		// As the feature log stores it, so the store's key finds its rows.
		pulls, listed, err := s.Pulls(ctx, r.Repository, start)
		if err != nil {
			report.Unread = append(report.Unread, name)
			continue
		}
		if !listed {
			report.Unlisted = append(report.Unlisted, name)
			continue
		}
		for _, p := range pulls {
			mine := report.Yours && strings.EqualFold(p.Author, login)
			if day := days[p.CreatedAt.In(loc).Format(time.DateOnly)]; day != nil && !p.Draft {
				for _, c := range flowParts(day, mine) {
					c.Opened++
					if p.MergedAt != nil {
						c.OpenedMerged++
					}
				}
			}
			if p.MergedAt == nil {
				continue
			}
			if day := days[p.MergedAt.In(loc).Format(time.DateOnly)]; day != nil {
				for _, c := range flowParts(day, mine) {
					c.Merged++
					c.MergeSeconds = append(c.MergeSeconds, p.MergedAt.Sub(p.CreatedAt).Seconds())
				}
			}
		}
	}
	return report
}

// flowParts is what a pull request counts in: the team's, and the person's
// own when it is theirs.
func flowParts(day *FlowDay, mine bool) []*FlowCounts {
	if mine {
		return []*FlowCounts{&day.Team, &day.Mine}
	}
	return []*FlowCounts{&day.Team}
}
