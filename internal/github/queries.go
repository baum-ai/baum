package github

import (
	"time"

	"github.com/lastpersonlabs/baum/githubapi"
)

// The GraphQL documents, one per part of the bundle so that a part GitHub
// refuses (a fine-grained token without checks access, say) fails alone.
// Every pull-request-scoped page also reads the head, so a push between pages
// is noticed. They follow the hosted service's documents for the same rows,
// adding thread identity and resolution, the pull request's author
// association and requested reviewers, and branch protection.

type pageInfo struct {
	HasNextPage bool
	EndCursor   *string
}

type actor struct {
	Login    string
	Typename string `graphql:"__typename" json:"__typename"`
}

// reviewRequest is a pending review request: the requested reviewer is a
// user or a team; other reviewer types are left out, as the hosted service
// leaves them out.
type reviewRequest struct {
	RequestedReviewer struct {
		Typename string `graphql:"__typename" json:"__typename"`
		User     struct {
			Login string
		} `graphql:"... on User"`
		Team struct {
			Slug string
		} `graphql:"... on Team"`
	}
}

type pullRequestHeader struct {
	Number            int
	State             string
	IsDraft           bool
	Title             string
	Body              string
	BodyHTML          string `graphql:"bodyHTML"`
	CreatedAt         time.Time
	Author            *actor
	AuthorAssociation string
	BaseRefName       string
	BaseRefOID        string `graphql:"baseRefOid"`
	HeadRefName       string
	HeadRefOID        string `graphql:"headRefOid"`
	Merged            bool
	MergeCommit       *struct {
		OID string `graphql:"oid"`
	}
	ReviewRequests struct {
		Nodes []reviewRequest
	} `graphql:"reviewRequests(first: 100)"`
}

type pullRequestQuery struct {
	Repository *struct {
		NameWithOwner string
		PullRequest   *pullRequestHeader `graphql:"pullRequest(number: $number)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

type commitNode struct {
	OID           string `graphql:"oid"`
	Message       string
	CommittedDate time.Time
	Parents       struct {
		Nodes []struct {
			OID string `graphql:"oid"`
		}
	} `graphql:"parents(first: 100)"`
}

type commitsQuery struct {
	Repository struct {
		PullRequest *struct {
			HeadRefOID string `graphql:"headRefOid"`
			Commits    struct {
				Nodes []struct {
					Commit commitNode
				}
				PageInfo pageInfo
			} `graphql:"commits(first: 100, after: $cursor)"`
		} `graphql:"pullRequest(number: $number)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

type issueComment struct {
	FullDatabaseID    githubapi.BigInt
	AuthorAssociation string
	Author            *actor
	Body              string
	CreatedAt         time.Time
}

type commentsQuery struct {
	Repository struct {
		PullRequest *struct {
			HeadRefOID string `graphql:"headRefOid"`
			Comments   struct {
				Nodes    []issueComment
				PageInfo pageInfo
			} `graphql:"comments(first: 100, after: $cursor)"`
		} `graphql:"pullRequest(number: $number)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

type review struct {
	FullDatabaseID    githubapi.BigInt
	AuthorAssociation string
	Author            *actor
	State             string
	Body              string
	Commit            *struct {
		OID string `graphql:"oid"`
	}
	SubmittedAt *time.Time
}

type reviewsQuery struct {
	Repository struct {
		PullRequest *struct {
			HeadRefOID string `graphql:"headRefOid"`
			Reviews    struct {
				Nodes    []review
				PageInfo pageInfo
			} `graphql:"reviews(first: 100, after: $cursor)"`
		} `graphql:"pullRequest(number: $number)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

type reviewComment struct {
	FullDatabaseID    githubapi.BigInt
	AuthorAssociation string
	Author            *actor
	Body              string
	Path              string
	URL               string `graphql:"url"`
	CreatedAt         time.Time
	PullRequestReview *struct {
		FullDatabaseID githubapi.BigInt
	}
}

type reviewCommentConnection struct {
	Nodes    []reviewComment
	PageInfo pageInfo
}

type reviewThread struct {
	ID         string `graphql:"id"`
	Path       string
	DiffSide   string
	Line       *int
	IsResolved bool
	IsOutdated bool
	Comments   reviewCommentConnection `graphql:"comments(first: 100)"`
}

type reviewThreadsQuery struct {
	Repository struct {
		PullRequest *struct {
			HeadRefOID    string `graphql:"headRefOid"`
			ReviewThreads struct {
				Nodes    []reviewThread
				PageInfo pageInfo
			} `graphql:"reviewThreads(first: 50, after: $cursor)"`
		} `graphql:"pullRequest(number: $number)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

type threadCommentsQuery struct {
	Node *struct {
		Thread struct {
			ID       string                  `graphql:"id"`
			Comments reviewCommentConnection `graphql:"comments(first: 100, after: $cursor)"`
		} `graphql:"... on PullRequestReviewThread"`
	} `graphql:"node(id: $id)"`
}

type checkRun struct {
	DatabaseID  githubapi.BigInt
	Name        string
	Status      string
	Conclusion  *string
	Permalink   string
	StartedAt   *time.Time
	CompletedAt *time.Time
}

type checkRunConnection struct {
	Nodes    []checkRun
	PageInfo pageInfo
}

// checkSuite lists every run of the suite, re-runs included, as the hosted
// service syncs them; the mapping keeps the newest of each name.
type checkSuite struct {
	ID         string `graphql:"id"`
	DatabaseID githubapi.BigInt
	CheckRuns  checkRunConnection `graphql:"checkRuns(first: 100, filterBy: {checkType: ALL})"`
}

type statusContext struct {
	ID        string `graphql:"id"`
	Context   string
	State     string
	TargetURL *string `graphql:"targetUrl"`
	CreatedAt time.Time
}

type headChecksQuery struct {
	Repository struct {
		Object *struct {
			Commit struct {
				OID         string `graphql:"oid"`
				CheckSuites struct {
					Nodes    []checkSuite
					PageInfo pageInfo
				} `graphql:"checkSuites(first: 50, after: $cursor)"`
				Status *struct {
					Contexts []statusContext
				}
			} `graphql:"... on Commit"`
		} `graphql:"object(oid: $oid)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

type suiteRunsQuery struct {
	Node *struct {
		CheckSuite struct {
			ID        string             `graphql:"id"`
			CheckRuns checkRunConnection `graphql:"checkRuns(first: 100, after: $cursor, filterBy: {checkType: ALL})"`
		} `graphql:"... on CheckSuite"`
	} `graphql:"node(id: $id)"`
}

// requiredChecksQuery reads the protection that applies to the base branch
// in the two forms a reader without admin rights can see: the classic
// branch protection rule and the rulesets' required status checks.
type requiredChecksQuery struct {
	Repository struct {
		Ref *struct {
			RefUpdateRule *struct {
				RequiredStatusCheckContexts []*string
			}
			Rules struct {
				Nodes []struct {
					Type       string
					Parameters *struct {
						RequiredStatusChecks struct {
							RequiredStatusChecks []struct {
								Context string
							}
						} `graphql:"... on RequiredStatusChecksParameters"`
					}
				}
				PageInfo pageInfo
			} `graphql:"rules(first: 100)"`
		} `graphql:"ref(qualifiedName: $ref)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

type branchPullRequestsQuery struct {
	Repository struct {
		Ref *struct {
			AssociatedPullRequests struct {
				Nodes []struct {
					Number         int
					State          string
					HeadRefName    string
					BaseRepository *struct {
						NameWithOwner string
					}
				}
			} `graphql:"associatedPullRequests(first: 20, orderBy: {field: UPDATED_AT, direction: DESC})"`
		} `graphql:"ref(qualifiedName: $ref)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}
