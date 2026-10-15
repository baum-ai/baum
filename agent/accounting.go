package agent

// UsageAttempt preserves the model and provider-reported buckets of one backend
// report, including unsuccessful retries and validation repairs.
type UsageAttempt struct {
	ModelSource   string `json:"model_source,omitempty"`
	Model         string `json:"model"`
	Usage         Usage  `json:"usage"`
	Calls         int    `json:"calls"`
	ChargedTokens int    `json:"charged_tokens"`
	Failed        bool   `json:"failed"`
}

// Accounting is additive provenance. Its absence in older records means unknown,
// never zero. Usage.Unreported marks a partial or missing provider report.
type Accounting struct {
	Usage         Usage          `json:"usage"`
	Attempts      []UsageAttempt `json:"attempts"`
	ChargedTokens int            `json:"charged_tokens"`
}

func (a *Accounting) Add(result Result, failed bool) {
	usage := result.Usage
	if usage.IsZero() {
		usage.Unreported = true
	}
	a.Usage.Add(usage)
	a.ChargedTokens += result.Tokens
	if len(result.UsageAttempts) > 0 {
		a.Attempts = append(a.Attempts, result.UsageAttempts...)
		return
	}
	a.Attempts = append(a.Attempts, UsageAttempt{ModelSource: result.ModelSource, Model: result.Model, Usage: usage, Calls: result.Calls, ChargedTokens: result.Tokens, Failed: failed})
}

// Clone detaches published accounting from a producer's subsequent updates.
func (a *Accounting) Clone() *Accounting {
	if a == nil {
		return nil
	}
	out := *a
	copyUsage := func(u Usage) Usage {
		if u.CacheCreate1h != nil {
			u.CacheCreate1h = new(*u.CacheCreate1h)
		}
		if u.Speed != nil {
			u.Speed = new(*u.Speed)
		}
		if u.MinPrompt != nil {
			u.MinPrompt = new(*u.MinPrompt)
		}
		if u.MaxPrompt != nil {
			u.MaxPrompt = new(*u.MaxPrompt)
		}
		return u
	}
	out.Usage = copyUsage(a.Usage)
	out.Attempts = append([]UsageAttempt(nil), a.Attempts...)
	for i := range out.Attempts {
		out.Attempts[i].Usage = copyUsage(out.Attempts[i].Usage)
	}
	return &out
}

// ClearStrings keeps exact numeric reports while withholding provider-controlled
// metadata until the host has redacted it, or after redaction fails.
func (a *Accounting) ClearStrings() {
	if a == nil {
		return
	}
	a.Usage.Speed = nil
	for i := range a.Attempts {
		a.Attempts[i].Model, a.Attempts[i].ModelSource = "", ""
		a.Attempts[i].Usage.Speed = nil
	}
}
