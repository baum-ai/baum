package tilechecks

import (
	"context"
	"errors"

	"github.com/baum-ai/baum/internal/tilelibrary"
	"github.com/baum-ai/baum/tiles/library"
)

// ImportCheck is the render check of an imported tile type
// (tilelibrary.SampleCheck): it draws the type's first MaxSamples samples
// through the queue and answers the verdict. A check that could not run
// because no runner took it (the app is closed, or its window cannot draw
// tiles) answers tilelibrary.ErrNoRunner, so the import saves the type as
// not checked yet; a failed or hung check answers its verdict, which refuses
// the import. A queue that is full is an error the person retries.
func (c *Checker) ImportCheck(ctx context.Context, entry library.Entry) (string, error) {
	samples := entry.Samples
	if len(samples) > MaxSamples {
		samples = samples[:MaxSamples]
	}
	result, err := c.Submit(ctx, Candidate{Definition: entry.Definition, Samples: samples})
	switch {
	case errors.Is(err, ErrBusy):
		return "", &tilelibrary.FileError{Reason: "Other render checks are waiting; try the import again in a few seconds"}
	case errors.Is(err, ErrClosed):
		return "", tilelibrary.ErrNoRunner
	case err != nil:
		return "", err
	case result.Verdict == VerdictUnavailable:
		return "", tilelibrary.ErrNoRunner
	case result.Verdict == VerdictPass:
		return tilelibrary.VerdictPass, nil
	}
	return result.Verdict, nil
}
