// Description images: the rendered-image and markdown-upload walk of a pull
// request description, image identity and sniffing, the attachment listing
// of a bundle, and the archive that carries the images to a run.
package bundle

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// Capture states of a description image.
const (
	AttachmentStored      = "stored"
	AttachmentUnsupported = "unsupported"
	AttachmentTooLarge    = "too_large"
	AttachmentOverLimit   = "over_limit"
	AttachmentExternal    = "external"
	AttachmentFailed      = "failed"
)

// DescriptionImage is one image GitHub rendered from the description, in
// description order. Only a Stored image has its copy's fields set.
type DescriptionImage struct {
	ID        string
	SourceURL string
	// Alt is redacted.
	Alt string
	// Position is the image's 1-based place among the description's images.
	Position    int
	State       string
	ContentType string
	Size        int64
	Width       int
	Height      int
	SHA256      string
	// ObjectKey is where the host stored the copy.
	ObjectKey string
}

var attachmentIDPattern = regexp.MustCompile(`^att_[0-9a-f]{32}$`)

// ValidAttachmentID reports whether id has the shape AttachmentID produces.
func ValidAttachmentID(id string) bool { return attachmentIDPattern.MatchString(id) }

// AttachmentID derives an image's ID from its markdown source URL.
func AttachmentID(sourceURL string) string {
	sum := sha256.Sum256([]byte(sourceURL))
	return "att_" + hex.EncodeToString(sum[:16])
}

var (
	uploadUUID = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	// Upload URLs as the markdown writes them: current user-attachments and
	// legacy user-images links.
	markdownUpload = regexp.MustCompile(`https://(?:github\.com/user-attachments/assets/|(?:private-)?user-images\.githubusercontent\.com/)[^\s()<>"'\]]+`)
)

// MarkdownUploads maps each upload's asset UUID to its markdown source URL,
// without a query or fragment: a pasted signed link keeps no token.
func MarkdownUploads(body string) map[string]string {
	uploads := map[string]string{}
	for _, source := range markdownUpload.FindAllString(body, -1) {
		if i := strings.IndexAny(source, "?#"); i >= 0 {
			source = source[:i]
		}
		if id := uploadUUID.FindString(source); id != "" {
			if _, seen := uploads[strings.ToLower(id)]; !seen {
				uploads[strings.ToLower(id)] = source
			}
		}
	}
	return uploads
}

// RenderedImage is one image found in a rendered description.
type RenderedImage struct {
	ID, SourceURL, Alt string
	// FetchURL is the signed rendered source; it is never stored or logged.
	FetchURL string
	// State is preset for an image that is not downloaded.
	State string
}

// RenderedImages walks the img elements of body_html. GitHub renders none
// from HTML comments or code fences, so those are left out. A repeated image
// is listed once.
func RenderedImages(bodyHTML string, uploads map[string]string) []RenderedImage {
	root, err := html.Parse(strings.NewReader(bodyHTML))
	if err != nil {
		return nil
	}
	var images []RenderedImage
	seen := map[string]bool{}
	for node := range root.Descendants() {
		if node.Type != html.ElementNode || node.Data != "img" {
			continue
		}
		src, alt, canonical, class := attr(node, "src"), attr(node, "alt"), attr(node, "data-canonical-src"), attr(node, "class")
		if src == "" || strings.Contains(class, "emoji") {
			continue
		}
		image := RenderedImage{Alt: alt}
		parsed, err := url.Parse(src)
		switch {
		case err != nil || !githubUploadURL(parsed):
			// An external source may be presigned too, so its token is dropped.
			source := src
			if canonical != "" {
				source = canonical
			}
			image.SourceURL, image.State = unsignedURL(source), AttachmentExternal
		default:
			image.FetchURL = src
			if source, ok := uploads[strings.ToLower(uploadUUID.FindString(parsed.Path))]; ok {
				image.SourceURL = source
			} else {
				// Unmatched: identify it by the unsigned part of its source.
				image.SourceURL, image.State = unsignedURL(src), AttachmentFailed
			}
		}
		image.ID = AttachmentID(image.SourceURL)
		if !seen[image.ID] {
			seen[image.ID] = true
			images = append(images, image)
		}
	}
	return images
}

// unsignedURL drops a source's query and fragment, where signing tokens live.
func unsignedURL(raw string) string {
	if cut, _, found := strings.Cut(raw, "#"); found {
		raw = cut
	}
	if cut, _, found := strings.Cut(raw, "?"); found {
		raw = cut
	}
	return raw
}

func attr(node *html.Node, key string) string {
	for _, a := range node.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// githubUploadURL reports whether a rendered source is one of GitHub's upload
// hosts. Camo-proxied images are external.
func githubUploadURL(u *url.URL) bool {
	if u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return (host == "github.com" && strings.HasPrefix(u.Path, "/user-attachments/assets/")) ||
		host == "private-user-images.githubusercontent.com" || host == "user-images.githubusercontent.com"
}

// GitHubContentURL also admits the asset storage GitHub redirects uploads to.
func GitHubContentURL(u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	return githubUploadURL(u) || (u.Scheme == "https" && (host == "objects.githubusercontent.com" ||
		(strings.HasPrefix(host, "github-production-user-asset-") && strings.HasSuffix(host, ".s3.amazonaws.com"))))
}

// SniffImage accepts PNG, JPEG, GIF and WebP by content and reads their size.
func SniffImage(content []byte) (contentType string, width, height int, ok bool) {
	if len(content) >= 12 && string(content[0:4]) == "RIFF" && string(content[8:12]) == "WEBP" {
		width, height, ok = webpSize(content)
		return "image/webp", width, height, ok
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(content))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return "", 0, 0, false
	}
	return "image/" + format, config.Width, config.Height, true
}

// webpSize reads the canvas size from the first chunk's header.
func webpSize(b []byte) (int, int, bool) {
	if len(b) < 30 {
		return 0, 0, false
	}
	var width, height int
	switch string(b[12:16]) {
	case "VP8 ":
		if b[23] != 0x9d || b[24] != 0x01 || b[25] != 0x2a {
			return 0, 0, false
		}
		width, height = int(binary.LittleEndian.Uint16(b[26:28])&0x3fff), int(binary.LittleEndian.Uint16(b[28:30])&0x3fff)
	case "VP8L":
		if b[20] != 0x2f {
			return 0, 0, false
		}
		bits := binary.LittleEndian.Uint32(b[21:25])
		width, height = int(bits&0x3fff)+1, int((bits>>14)&0x3fff)+1
	case "VP8X":
		width = int(uint32(b[24])|uint32(b[25])<<8|uint32(b[26])<<16) + 1
		height = int(uint32(b[27])|uint32(b[28])<<8|uint32(b[29])<<16) + 1
	default:
		return 0, 0, false
	}
	return width, height, width > 0 && height > 0
}

var attachmentExtensions = map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/gif": "gif", "image/webp": "webp"}

// AttachmentPath is where a stored image's file sits in the run directory;
// only a stored image of an accepted type has one.
func AttachmentPath(image DescriptionImage) (string, bool) {
	extension, ok := attachmentExtensions[image.ContentType]
	if image.State != AttachmentStored || !ok || image.SHA256 == "" {
		return "", false
	}
	return "attachments/" + image.ID + "." + extension, true
}

// attachmentFileName is the only name an unpacked image may have.
var attachmentFileName = regexp.MustCompile(`^attachments/att_[0-9a-f]{32}\.(?:png|jpg|gif|webp)$`)

// DescriptionAttachments lists the stored images for the bundle, each under
// the nearest heading above it in the untrimmed body.
func DescriptionAttachments(body string, images []DescriptionImage) []Attachment {
	var attachments []Attachment
	for _, image := range images {
		path, ok := AttachmentPath(image)
		if !ok {
			continue
		}
		attachments = append(attachments, Attachment{
			ID: image.ID, Path: path, SourceURL: image.SourceURL, ContentType: image.ContentType,
			Alt: image.Alt, Heading: headingAbove(body, image.SourceURL), Position: image.Position,
			Width: image.Width, Height: image.Height, SHA256: image.SHA256,
		})
	}
	return attachments
}

var atxHeading = regexp.MustCompile(`^ {0,3}#{1,6}[ \t]+(.*?)(?:[ \t]+#+)?[ \t]*$`)

// headingAbove finds the last ATX heading on a line before the one holding
// source, outside code fences. The synced body may predate the capture's
// fetch, so a source it does not hold has no heading.
func headingAbove(body, source string) string {
	end := strings.Index(body, source)
	if end < 0 {
		return ""
	}
	end = max(strings.LastIndexByte(body[:end], '\n'), 0)
	heading, fenced := "", false
	for _, line := range strings.Split(body[:end], "\n") {
		line = strings.TrimRight(line, "\r")
		if trimmed := strings.TrimLeft(line, " "); strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fenced = !fenced
			continue
		}
		if match := atxHeading.FindStringSubmatch(line); !fenced && match != nil {
			heading = match[1]
		}
	}
	return heading
}

// WriteAttachments archives the stored copies as a plain tar of
// attachments/<id>.<ext> entries and returns the images it holds. open reads
// one stored copy. A copy that cannot be read or no longer matches its hash
// is left out of the archive, and so of the bundle: capture never fails
// assembly. skipped, when not nil, is told of each image left out and why.
func WriteAttachments(ctx context.Context, images []DescriptionImage, archive io.Writer, open func(context.Context, DescriptionImage) (io.ReadCloser, error), skipped func(DescriptionImage, error)) ([]DescriptionImage, error) {
	writer := tar.NewWriter(archive)
	var kept []DescriptionImage
	for _, image := range images {
		path, ok := AttachmentPath(image)
		if !ok {
			continue
		}
		content, err := readAttachment(ctx, open, image)
		if err != nil {
			if skipped != nil {
				skipped(image, err)
			}
			continue
		}
		if err := writer.WriteHeader(&tar.Header{Name: path, Typeflag: tar.TypeReg, Mode: 0400, Size: int64(len(content))}); err != nil {
			return nil, err
		}
		if _, err := writer.Write(content); err != nil {
			return nil, err
		}
		kept = append(kept, image)
	}
	return kept, writer.Close()
}

func readAttachment(ctx context.Context, open func(context.Context, DescriptionImage) (io.ReadCloser, error), image DescriptionImage) ([]byte, error) {
	body, err := open(ctx, image)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	content, err := io.ReadAll(io.LimitReader(body, image.Size+1))
	if err != nil {
		return nil, err
	}
	if sum := sha256.Sum256(content); int64(len(content)) != image.Size || hex.EncodeToString(sum[:]) != image.SHA256 {
		return nil, errors.New("stored description image does not match its hash")
	}
	return content, nil
}

// MaxAttachmentsArchiveBytes bounds an attachments archive: every image at
// its limit, a tar header and padding for each, and the end marker.
func MaxAttachmentsArchiveBytes(maxImages int, maxImageBytes int64) int64 {
	return int64(maxImages)*(maxImageBytes+1024) + 1024
}

// UnpackAttachments writes exactly the images the bundle lists to
// attachments/<id>.<ext> in the run directory, read-only. An archive holding
// anything else, missing a listed image or not matching its hash fails with ErrInvalidArchive. An empty archive path means the run has no images.
func UnpackAttachments(ctx context.Context, archive, dir string, attachments []Attachment, maxImageBytes int64) error {
	if archive == "" {
		if len(attachments) > 0 {
			return ErrInvalidArchive
		}
		return nil
	}
	listed := map[string]string{}
	for _, attachment := range attachments {
		if !attachmentFileName.MatchString(attachment.Path) {
			return ErrInvalidArchive
		}
		listed[attachment.Path] = attachment.SHA256
	}
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	root := filepath.Join(dir, "attachments")
	if err := os.Mkdir(root, 0700); err != nil {
		return err
	}
	reader := tar.NewReader(file)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return ErrInvalidArchive
		}
		digest, ok := listed[entry.Name]
		if !ok || entry.Typeflag != tar.TypeReg || entry.Size < 0 || (maxImageBytes > 0 && entry.Size > maxImageBytes) {
			return ErrInvalidArchive
		}
		delete(listed, entry.Name)
		output, err := os.OpenFile(filepath.Join(dir, entry.Name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.CopyN(io.MultiWriter(output, hash), reader, entry.Size)
		if err := errors.Join(copyErr, output.Close()); err != nil || hex.EncodeToString(hash.Sum(nil)) != digest {
			return ErrInvalidArchive
		}
	}
	if len(listed) > 0 {
		return ErrInvalidArchive
	}
	return os.Chmod(root, 0500)
}
