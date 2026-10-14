package assemble

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/baumai/baum/bundle"
)

// Description images are downloaded under the hosted limits: each download
// has its own timeout, and all of them share one budget so capture cannot
// stall an assembly.
const (
	imageDownloadTimeout = 30 * time.Second
	imageCaptureTimeout  = 2 * time.Minute
)

// ErrImageTooLarge is what an ImageFetcher returns for an image over the
// byte limit it was given.
var ErrImageTooLarge = errors.New("image over the size limit")

// ImageFetcher downloads one description image from the signed URL GitHub
// rendered for it, reading at most maxBytes.
type ImageFetcher func(ctx context.Context, fetchURL string, maxBytes int64) ([]byte, error)

// captureImages records a state for every image the description renders and
// downloads the ones GitHub hosts, as the hosted capture does: it never
// fails the assembly, every problem becomes the image's state. The contents
// of the stored images are returned by ID. Neither a warning nor an error
// carries a URL, which is signed.
func (a *Assembler) captureImages(ctx context.Context, found []bundle.RenderedImage, redactor *bundle.Redactor, limits Limits) ([]bundle.DescriptionImage, map[string][]byte, int) {
	if len(found) == 0 {
		return nil, nil, 0
	}
	ctx, cancel := context.WithTimeout(ctx, imageCaptureTimeout)
	defer cancel()
	contents := map[string][]byte{}
	images := make([]bundle.DescriptionImage, 0, len(found))
	hosted, masked := 0, 0
	for _, candidate := range found {
		image := bundle.DescriptionImage{ID: candidate.ID, SourceURL: candidate.SourceURL, Position: len(images) + 1, State: candidate.State}
		if alt, count, err := redactor.RedactString(ctx, candidate.Alt); err == nil {
			image.Alt = alt
			masked += count
		}
		if image.State == "" {
			hosted++
			switch {
			case hosted > limits.MaxImages:
				image.State = bundle.AttachmentOverLimit
			case a.Images == nil:
				image.State = bundle.AttachmentFailed
			default:
				if content := a.download(ctx, candidate.FetchURL, limits.MaxImageBytes, &image); content != nil {
					contents[image.ID] = content
				}
			}
		}
		images = append(images, image)
	}
	return images, contents, masked
}

// download fills image from the signed source and returns the content of a
// stored image.
func (a *Assembler) download(ctx context.Context, source string, maxBytes int64, image *bundle.DescriptionImage) []byte {
	image.State = bundle.AttachmentFailed
	ctx, cancel := context.WithTimeout(ctx, imageDownloadTimeout)
	defer cancel()
	content, err := a.Images(ctx, source, maxBytes)
	switch {
	case errors.Is(err, ErrImageTooLarge):
		image.State = bundle.AttachmentTooLarge
		return nil
	case err != nil:
		a.warn("description image %d could not be downloaded: %v", image.Position, err)
		return nil
	case int64(len(content)) > maxBytes:
		image.State = bundle.AttachmentTooLarge
		return nil
	}
	contentType, width, height, ok := bundle.SniffImage(content)
	if !ok {
		image.State = bundle.AttachmentUnsupported
		return nil
	}
	sum := sha256.Sum256(content)
	image.State, image.ContentType, image.Size, image.Width, image.Height, image.SHA256 =
		bundle.AttachmentStored, contentType, int64(len(content)), width, height, hex.EncodeToString(sum[:])
	return content
}

// NewHTTPImageFetcher downloads description images over HTTPS. GitHub
// renders each image's source already signed, so the request carries no
// credentials of its own (the user's token is what asked GitHub for the
// signed source), and only GitHub's upload and asset hosts are contacted,
// redirects included. A nil transport is http.DefaultTransport.
func NewHTTPImageFetcher(transport http.RoundTripper) ImageFetcher {
	return httpImageFetcher(transport, bundle.GitHubContentURL)
}

// httpImageFetcher is NewHTTPImageFetcher for the hosts allow accepts.
func httpImageFetcher(transport http.RoundTripper, allow func(*url.URL) bool) ImageFetcher {
	client := &http.Client{Transport: transport, CheckRedirect: func(next *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !allow(next.URL) {
			return errors.New("redirect outside GitHub's upload hosts")
		}
		return nil
	}}
	return func(ctx context.Context, source string, maxBytes int64) ([]byte, error) {
		parsed, err := url.Parse(source)
		if err != nil || !allow(parsed) {
			return nil, errors.New("not a GitHub upload URL")
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			return nil, errors.New("download request could not be built")
		}
		response, err := client.Do(request)
		if err != nil {
			// The error text names the URL, which is signed.
			return nil, errors.New("download request failed")
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("download returned HTTP %d", response.StatusCode)
		}
		if response.ContentLength > maxBytes {
			return nil, ErrImageTooLarge
		}
		content, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
		if err != nil {
			return nil, errors.New("download was interrupted")
		}
		if int64(len(content)) > maxBytes {
			return nil, ErrImageTooLarge
		}
		return content, nil
	}
}
