package review

import bundlepkg "github.com/baum-ai/baum/bundle"

func validationItems(s bundlepkg.Session) []bundlepkg.SessionValidation {
	if s.Restricted || s.Validations == nil {
		return nil
	}
	return *s.Validations
}

func setValidation(doc *reviewDocument, b *bundlepkg.Bundle) {
	doc.Index.Validation, doc.Stats.Validation, doc.Review.Sources.SessionValidation = nil, nil, nil
	count := 0
	stats := &documentValidationStats{}
	for _, session := range b.Sessions {
		if session.Restricted {
			continue
		}
		if session.Validations == nil {
			continue
		}
		count++
		stats.NotVerified += len(session.NotVerified)
		stats.SetAside += len(session.SetAside)
		for _, item := range *session.Validations {
			doc.Index.Validation = append(doc.Index.Validation, documentValidation{SessionValidation: item, Session: session.RootSessionID})
			switch item.CommitLabel.State {
			case "head":
				stats.Head++
			case "earlier":
				stats.Earlier++
			case "uncommitted":
				stats.Uncommitted++
			default:
				stats.Unknown++
			}
		}
	}
	if count > 0 {
		doc.Stats.Validation, doc.Review.Sources.SessionValidation = stats, &count
	}
}
