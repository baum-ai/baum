package reviewgroups

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/locallink"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewindex"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/viewserver"
)

// Registry is the part of *repos.Registry the handler reads: the picked
// repositories and their clones.
type Registry interface {
	Repositories(context.Context) ([]repos.Repository, error)
}

// API serves /api/groups. It has no authentication; the daemon mounts it
// behind its authenticated host.
type API struct {
	Store    *Store
	Registry Registry
	// Index is the review index, for the members' current heads; nil leaves
	// every head unknown, so no group reads as outdated.
	Index func(context.Context) (reviewindex.Snapshot, error)
	// Changed is called after every mutation.
	Changed func()
	// Patch reads one file's patch; nil uses a locallink.Patcher.
	Patch func(context.Context, locallink.PatchRequest) (locallink.PatchResult, error)

	patches chan struct{}
}

// View is a group as the API answers it.
type View struct {
	Group
	Summary
	Outdated bool `json:"outdated"`
}

const maxPatchReads = 4

// Handler routes /api/groups.
func (h *API) Handler() http.Handler {
	h.patches = make(chan struct{}, maxPatchReads)
	if h.Patch == nil {
		patcher := new(locallink.Patcher)
		h.Patch = patcher.Patch
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/groups", h.list)
	mux.HandleFunc("POST /api/groups", h.create)
	mux.HandleFunc("GET /api/groups/{id}", h.get)
	mux.HandleFunc("PATCH /api/groups/{id}", h.update)
	mux.HandleFunc("DELETE /api/groups/{id}", h.remove)
	mux.HandleFunc("GET /api/groups/{id}/versions", h.versions)
	mux.HandleFunc("GET /api/groups/{id}/document", h.document)
	mux.HandleFunc("GET /api/groups/{id}/patch", h.patch)
	return mux
}

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func failure(w http.ResponseWriter, status int, code, message string) {
	respond(w, status, map[string]string{"code": code, "message": message})
}

// fail answers an error of the store or the rules.
func fail(w http.ResponseWriter, err error) {
	var refused *InvalidError
	switch {
	case errors.As(err, &refused):
		failure(w, http.StatusBadRequest, refused.Code, refused.Message)
	case errors.Is(err, ErrNotFound), errors.Is(err, store.ErrNotFound):
		failure(w, http.StatusNotFound, "not_found", "The review group or its review was not found.")
	case errors.Is(err, ErrConflict):
		failure(w, http.StatusConflict, "conflict", err.Error())
	default:
		failure(w, http.StatusInternalServerError, "internal", "The review group could not be read or saved.")
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		failure(w, http.StatusBadRequest, CodeInvalid, "The request is not valid JSON for this route.")
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		failure(w, http.StatusBadRequest, CodeInvalid, "Send one JSON request.")
		return false
	}
	return true
}

func (h *API) changed() {
	if h.Changed != nil {
		h.Changed()
	}
}

// heads are the open pull requests' current heads by repository identity.
func (h *API) heads(ctx context.Context) map[string]map[int]string {
	out := map[string]map[int]string{}
	if h.Index == nil {
		return out
	}
	snapshot, err := h.Index(ctx)
	if err != nil {
		return out
	}
	for _, entry := range snapshot.Entries {
		if entry.Historical || entry.PullRequest.HeadSHA == "" {
			continue
		}
		repository := strings.ToLower(entry.PullRequest.Repository)
		if out[repository] == nil {
			out[repository] = map[int]string{}
		}
		out[repository][entry.PullRequest.Number] = entry.PullRequest.HeadSHA
	}
	return out
}

func (h *API) view(ctx context.Context, g Group, heads map[string]map[int]string) (View, error) {
	summary, err := h.Store.Summary(ctx, g.ID)
	if err != nil {
		return View{}, err
	}
	return View{Group: g, Summary: summary, Outdated: Outdated(g.Members, summary.Reviewed, heads[g.Repository])}, nil
}

func (h *API) list(w http.ResponseWriter, r *http.Request) {
	groups, err := h.Store.List(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	heads := h.heads(r.Context())
	views := make([]View, 0, len(groups))
	for _, g := range groups {
		v, err := h.view(r.Context(), g, heads)
		if err != nil {
			fail(w, err)
			return
		}
		views = append(views, v)
	}
	respond(w, http.StatusOK, map[string]any{"available": true, "groups": views})
}

func (h *API) get(w http.ResponseWriter, r *http.Request) {
	g, err := h.Store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	v, err := h.view(r.Context(), g, h.heads(r.Context()))
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, v)
}

// pickedRepository is the registry's identity of a picked repository, by
// any spelling of its name.
func (h *API) pickedRepository(ctx context.Context, name string) (repos.Repository, error) {
	picked, err := h.Registry.Repositories(ctx)
	if err != nil {
		return repos.Repository{}, err
	}
	for _, repository := range picked {
		if strings.EqualFold(repository.Repository, name) {
			return repository, nil
		}
	}
	return repos.Repository{}, &InvalidError{Code: CodeNotPicked, Message: "Pick the repository in baum before grouping its pull requests."}
}

func (h *API) create(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name    string `json:"name"`
		Members []struct {
			Repository string `json:"repository"`
			Number     int    `json:"number"`
		} `json:"members"`
	}
	if !decode(w, r, &request) {
		return
	}
	if len(request.Members) == 0 {
		fail(w, invalid("A review group has %d to %d pull requests.", MinMembers, MaxMembers))
		return
	}
	numbers := make([]int, 0, len(request.Members))
	for _, member := range request.Members {
		if !strings.EqualFold(member.Repository, request.Members[0].Repository) {
			failure(w, http.StatusBadRequest, CodeCrossRepository, "A review group's pull requests must be in one repository. Groups across repositories are not supported yet.")
			return
		}
		numbers = append(numbers, member.Number)
	}
	repository, err := h.pickedRepository(r.Context(), request.Members[0].Repository)
	if err != nil {
		fail(w, err)
		return
	}
	g, err := h.Store.Create(r.Context(), repository.Repository, request.Name, numbers)
	if err != nil {
		fail(w, err)
		return
	}
	h.changed()
	v, err := h.view(r.Context(), g, h.heads(r.Context()))
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusCreated, v)
}

func (h *API) update(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name    *string `json:"name"`
		Members []int   `json:"members"`
	}
	if !decode(w, r, &request) {
		return
	}
	g, err := h.Store.Update(r.Context(), r.PathValue("id"), request.Name, request.Members)
	if err != nil {
		fail(w, err)
		return
	}
	h.changed()
	v, err := h.view(r.Context(), g, h.heads(r.Context()))
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, v)
}

func (h *API) remove(w http.ResponseWriter, r *http.Request) {
	if err := h.Store.Delete(r.Context(), r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	h.changed()
	w.WriteHeader(http.StatusNoContent)
}

func (h *API) versions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := h.Store.Get(r.Context(), id); err != nil {
		fail(w, err)
		return
	}
	versions, err := h.Store.Versions(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, versions)
}

// version loads the version the request names (?version=, the latest when
// absent) of a group that exists.
func (h *API) version(w http.ResponseWriter, r *http.Request) (Group, store.Version, bool) {
	id := r.PathValue("id")
	g, err := h.Store.Get(r.Context(), id)
	if err != nil {
		fail(w, err)
		return Group{}, store.Version{}, false
	}
	n := 0
	if raw := r.URL.Query().Get("version"); raw != "" {
		if n, err = strconv.Atoi(raw); err != nil || n < 0 {
			failure(w, http.StatusBadRequest, CodeInvalid, "version must be a nonnegative number")
			return Group{}, store.Version{}, false
		}
	}
	var version store.Version
	if n == 0 {
		version, err = h.Store.Latest(r.Context(), id)
	} else {
		version, err = h.Store.Version(r.Context(), id, n)
	}
	if err != nil {
		fail(w, err)
		return Group{}, store.Version{}, false
	}
	if len(version.Document) > viewserver.MaxDocumentBytes {
		failure(w, http.StatusRequestEntityTooLarge, "too_large", "stored document too large")
		return Group{}, store.Version{}, false
	}
	return g, version, true
}

func (h *API) document(w http.ResponseWriter, r *http.Request) {
	_, version, ok := h.version(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(version.Document)
}

// patchDocument is what the patch route reads of a group review.
type patchDocument struct {
	Review struct {
		Repository string `json:"repository"`
	} `json:"review"`
	Stack struct {
		Members []ReviewedMember `json:"members"`
	} `json:"stack"`
	Index struct {
		Hunks []struct {
			ID       string `json:"id"`
			Path     string `json:"path"`
			NewRange []int  `json:"new_range"`
		} `json:"hunks"`
		FileComparisons []struct {
			BaseRevision string `json:"base_revision"`
			Changes      []struct {
				Path string `json:"path"`
			} `json:"changes"`
		} `json:"file_comparisons"`
	} `json:"index"`
}

var hunkHeader = regexp.MustCompile(`(?m)^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

// holdsRange reports whether patch has a hunk whose new side is the range.
func holdsRange(patch string, start, count int) bool {
	for _, match := range hunkHeader.FindAllStringSubmatch(patch, -1) {
		c, _ := strconv.Atoi(match[1])
		d := 1
		if match[2] != "" {
			d, _ = strconv.Atoi(match[2])
		}
		if c == start && d == count {
			return true
		}
	}
	return false
}

// patch answers the patch of a file the group review cites, from the member
// whose comparison changes it: the one whose patch holds the cited hunk's
// range when several do, else the first. The answer is {"patch": null} when
// no member's patch can be read, as for a pull request.
func (h *API) patch(w http.ResponseWriter, r *http.Request) {
	g, version, ok := h.version(w, r)
	if !ok {
		return
	}
	path, hunk := r.URL.Query().Get("path"), r.URL.Query().Get("hunk")
	var doc patchDocument
	if path == "" || json.Unmarshal(version.Document, &doc) != nil {
		http.NotFound(w, r)
		return
	}
	cited, start, count := false, 0, -1
	for _, item := range doc.Index.Hunks {
		if item.Path != path {
			continue
		}
		cited = true
		if item.ID == hunk && len(item.NewRange) == 2 {
			start, count = item.NewRange[0], item.NewRange[1]
		}
	}
	if !cited {
		http.NotFound(w, r)
		return
	}
	type candidate struct{ head, base string }
	var candidates []candidate
	for i, member := range doc.Stack.Members {
		if i >= len(doc.Index.FileComparisons) {
			break
		}
		comparison := doc.Index.FileComparisons[i]
		for _, change := range comparison.Changes {
			if change.Path == path {
				candidates = append(candidates, candidate{member.HeadSHA, comparison.BaseRevision})
				break
			}
		}
	}
	clone := h.clone(r.Context(), g.Repository)
	answer := map[string]*string{"patch": nil}
	if clone == "" || len(candidates) == 0 {
		respond(w, http.StatusOK, answer)
		return
	}
	select {
	case h.patches <- struct{}{}:
		defer func() { <-h.patches }()
	case <-r.Context().Done():
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	for _, c := range candidates {
		result, err := h.Patch(ctx, locallink.PatchRequest{Dir: clone, Repository: doc.Review.Repository, Head: c.head, MergeBase: c.base, Path: path})
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			failure(w, http.StatusInternalServerError, "internal", "cannot read this patch")
			return
		}
		if !result.Available {
			continue
		}
		if answer["patch"] == nil {
			answer["patch"] = &result.Patch
		}
		if count < 0 || holdsRange(result.Patch, start, count) {
			answer["patch"] = &result.Patch
			break
		}
	}
	respond(w, http.StatusOK, answer)
}

// clone is the path of an available registered clone of repository, "" when
// there is none.
func (h *API) clone(ctx context.Context, repository string) string {
	picked, err := h.Registry.Repositories(ctx)
	if err != nil {
		return ""
	}
	for _, r := range picked {
		if !strings.EqualFold(r.Repository, repository) {
			continue
		}
		for _, c := range r.Clones {
			if c.Available {
				return c.Path
			}
		}
	}
	return ""
}
