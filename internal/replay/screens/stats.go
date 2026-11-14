package screens

import (
	"math"
)

// SignTest is the two-sided exact sign test of wins against losses (ties
// left out): the probability, under even odds, of a split at least as
// uneven. It is 1 with no decisive pair.
func SignTest(wins, losses int) float64 {
	n := wins + losses
	if n == 0 {
		return 1
	}
	k := min(wins, losses)
	// P(X <= k) for X ~ Binomial(n, 1/2), doubled.
	p := 0.0
	for i := 0; i <= k; i++ {
		p += math.Exp(logChoose(n, i) - float64(n)*math.Ln2)
	}
	return math.Min(1, 2*p)
}

func logChoose(n, k int) float64 {
	a, _ := math.Lgamma(float64(n + 1))
	b, _ := math.Lgamma(float64(k + 1))
	c, _ := math.Lgamma(float64(n - k + 1))
	return a - b - c
}

// Wilson is the 95% Wilson score interval of a share of successes out of n.
func Wilson(successes, n int) (low, high float64) {
	if n == 0 {
		return 0, 1
	}
	const z = 1.959963984540054
	p := float64(successes) / float64(n)
	nf := float64(n)
	centre := (p + z*z/(2*nf)) / (1 + z*z/nf)
	half := z * math.Sqrt(p*(1-p)/nf+z*z/(4*nf*nf)) / (1 + z*z/nf)
	return math.Max(0, centre-half), math.Min(1, centre+half)
}

// Recall counts, over cases, how many must-know signals of each kind were
// given and how many the first screen showed.
type Recall struct {
	Recommendation, SevereRisks, NamedChanges, RiskiestChanges, MediumRisks, Breaking, MissingEvidence Count
	// All pools every signal.
	All Count
}

// Count is shown out of given.
type Count struct {
	Given int `json:"given"`
	Shown int `json:"shown"`
}

// Share is shown over given, or NaN with none given.
func (c Count) Share() float64 {
	if c.Given == 0 {
		return math.NaN()
	}
	return float64(c.Shown) / float64(c.Given)
}

func (c *Count) add(given, shown bool) {
	if given {
		c.Given++
		if shown {
			c.Shown++
		}
	}
}

// RecallOf sums the signals of first screens.
func RecallOf(screens []FirstScreen) Recall {
	var r Recall
	for _, f := range screens {
		r.Recommendation.add(f.Recommendation.Given, f.Recommendation.Visible)
		for _, s := range f.SevereRisks {
			r.SevereRisks.add(true, s.Visible)
		}
		for _, s := range f.NamedChanges {
			r.NamedChanges.add(true, s.Visible)
		}
		for _, s := range f.RiskiestChanges {
			r.RiskiestChanges.add(true, s.Visible)
		}
		for _, s := range f.MediumRisks {
			r.MediumRisks.add(true, s.Visible)
		}
		r.Breaking.add(f.Breaking.Given, f.Breaking.Visible)
		r.MissingEvidence.add(f.MissingEvidence.Count > 0, f.MissingEvidence.Visible)
	}
	for _, c := range []Count{r.Recommendation, r.SevereRisks, r.NamedChanges, r.RiskiestChanges, r.MediumRisks, r.Breaking, r.MissingEvidence} {
		r.All.Given += c.Given
		r.All.Shown += c.Shown
	}
	return r
}

// Kinds lists the signal counts by name, in a fixed order.
func (r Recall) Kinds() []struct {
	Name  string
	Count Count
} {
	return []struct {
		Name  string
		Count Count
	}{
		{"recommendation", r.Recommendation}, {"critical/high risks", r.SevereRisks}, {"changes the recommendation names", r.NamedChanges},
		{"riskiest changes", r.RiskiestChanges}, {"top medium risks", r.MediumRisks}, {"breaking change", r.Breaking}, {"missing evidence", r.MissingEvidence}, {"all", r.All},
	}
}
