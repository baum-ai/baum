// Description images: the rendered-image and markdown-upload walk of a pull
// request description, image identity and sniffing, the attachment listing
// of a bundle, and the archive that carries the images to a run. Local
// reviews carry description video clips the same way.
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
	"math"
	"math/bits"
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

// DescriptionImage is one captured image. Native session copies have Origins
// and no SourceURL. Only a Stored image has its copy's fields set.
type DescriptionImage struct {
	Origins    []ImageOrigin `json:"origins,omitempty"`
	ID         string
	SourceURL  string
	SourceURLs []string
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

// Local reviews also capture MP4 and WebM clips from the description, under
// limits of their own.
const (
	MaxVideos     = 3
	MaxVideoBytes = 100 << 20
)

// IsVideo reports whether contentType is a captured clip's.
func IsVideo(contentType string) bool { return strings.HasPrefix(contentType, "video/") }

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
	// Video marks an uploaded clip, which RenderedMedia lists.
	Video bool
}

// RenderedImages walks the img elements of body_html. GitHub renders none
// from HTML comments or code fences, so those are left out. A repeated image
// is listed once.
func RenderedImages(bodyHTML string, uploads map[string]string) []RenderedImage {
	return renderedMedia(bodyHTML, uploads, false)
}

// RenderedMedia is RenderedImages that also lists the video elements GitHub
// renders for uploaded clips.
func RenderedMedia(bodyHTML string, uploads map[string]string) []RenderedImage {
	return renderedMedia(bodyHTML, uploads, true)
}

func renderedMedia(bodyHTML string, uploads map[string]string, videos bool) []RenderedImage {
	root, err := html.Parse(strings.NewReader(bodyHTML))
	if err != nil {
		return nil
	}
	var images []RenderedImage
	seen := map[string]bool{}
	for node := range root.Descendants() {
		video := videos && node.Type == html.ElementNode && node.Data == "video"
		if node.Type != html.ElementNode || (node.Data != "img" && !video) {
			continue
		}
		src, alt, canonical, class := attr(node, "src"), attr(node, "alt"), attr(node, "data-canonical-src"), attr(node, "class")
		if src == "" || strings.Contains(class, "emoji") {
			continue
		}
		image := RenderedImage{Alt: alt, Video: video}
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

// SniffMedia is SniffImage, or for a video only an MP4 or WebM clip whose
// frame size it can read.
func SniffMedia(content []byte, video bool) (contentType string, width, height int, ok bool) {
	switch {
	case !video:
		return SniffImage(content)
	case len(content) >= 8 && string(content[4:8]) == "ftyp":
		width, height, ok = mp4Size(content)
		return "video/mp4", width, height, ok
	case bytes.HasPrefix(content, []byte{0x1a, 0x45, 0xdf, 0xa3}):
		width, height, ok = webmSize(content)
		return "video/webm", width, height, ok
	}
	return "", 0, 0, false
}

// mp4Size reads the first track header with a frame size, wherever the
// movie box sits.
func mp4Size(b []byte) (int, int, bool) {
	var width, height int
	mp4Boxes(b, "moov", func(moov []byte) bool {
		return mp4Boxes(moov, "trak", func(trak []byte) bool {
			return mp4Boxes(trak, "tkhd", func(tkhd []byte) bool {
				at := 76 // version, flags, times, track, duration, layer, volume, matrix
				if len(tkhd) > 0 && tkhd[0] == 1 {
					at = 88
				}
				if len(tkhd) >= at+8 {
					width, height = int(binary.BigEndian.Uint32(tkhd[at:])>>16), int(binary.BigEndian.Uint32(tkhd[at+4:])>>16)
				}
				return width > 0 && height > 0
			})
		})
	})
	if width <= 0 || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}

// mp4Boxes calls visit with the payload of each of b's boxes of one type
// until it returns true, and reports whether it did.
func mp4Boxes(b []byte, kind string, visit func([]byte) bool) bool {
	for len(b) >= 8 {
		size, header := uint64(binary.BigEndian.Uint32(b)), uint64(8)
		switch size {
		case 0:
			size = uint64(len(b))
		case 1:
			if len(b) < 16 {
				return false
			}
			size, header = binary.BigEndian.Uint64(b[8:]), 16
		}
		if size < header || size > uint64(len(b)) {
			return false
		}
		if string(b[4:8]) == kind && visit(b[header:size]) {
			return true
		}
		b = b[size:]
	}
	return false
}

// webmSize reads the first video track's pixel size of a WebM file.
func webmSize(b []byte) (int, int, bool) {
	if string(ebmlFirst(ebmlFirst(b, 0x1a45dfa3), 0x4282)) != "webm" {
		return 0, 0, false
	}
	var width, height uint64
	ebmlChildren(b, 0x18538067, func(segment []byte) bool {
		return ebmlChildren(segment, 0x1654ae6b, func(tracks []byte) bool {
			return ebmlChildren(tracks, 0xae, func(entry []byte) bool {
				return ebmlChildren(entry, 0xe0, func(video []byte) bool {
					width, height = ebmlUint(ebmlFirst(video, 0xb0)), ebmlUint(ebmlFirst(video, 0xba))
					return width > 0 && height > 0 && width <= math.MaxInt32 && height <= math.MaxInt32
				})
			})
		})
	})
	if width == 0 || height == 0 || width > math.MaxInt32 || height > math.MaxInt32 {
		return 0, 0, false
	}
	return int(width), int(height), true
}

// ebmlChildren calls visit with the payload of each of b's elements with one
// ID until it returns true, and reports whether it did. An element of unknown
// or overlong size runs to the end of b.
func ebmlChildren(b []byte, id uint64, visit func([]byte) bool) bool {
	for len(b) > 0 {
		elementID, n := ebmlVint(b, false)
		if n == 0 {
			return false
		}
		size, m := ebmlVint(b[n:], true)
		if m == 0 {
			return false
		}
		b = b[n+m:]
		size = min(size, uint64(len(b)))
		if elementID == id && visit(b[:size]) {
			return true
		}
		b = b[size:]
	}
	return false
}

// ebmlFirst is the payload of b's first element with one ID, nil when none.
func ebmlFirst(b []byte, id uint64) (payload []byte) {
	ebmlChildren(b, id, func(p []byte) bool { payload = p; return true })
	return payload
}

// ebmlVint reads a variable-length ID (marker kept) or size (marker dropped,
// all ones meaning unknown) and its length, zero when malformed.
func ebmlVint(b []byte, size bool) (uint64, int) {
	if len(b) == 0 || b[0] == 0 {
		return 0, 0
	}
	n := bits.LeadingZeros8(b[0]) + 1
	if len(b) < n {
		return 0, 0
	}
	v := uint64(b[0])
	if size {
		v &= 0xff >> n
	}
	for _, c := range b[1:n] {
		v = v<<8 | uint64(c)
	}
	if size && v == 1<<(7*n)-1 {
		return math.MaxUint64, n
	}
	return v, n
}

// ebmlUint reads an element's payload as an unsigned integer.
func ebmlUint(payload []byte) uint64 {
	var v uint64
	if len(payload) <= 8 {
		for _, c := range payload {
			v = v<<8 | uint64(c)
		}
	}
	return v
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

var attachmentExtensions = map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/gif": "gif", "image/webp": "webp", "video/mp4": "mp4", "video/webm": "webm"}

// attachmentPath is where a stored image's file sits in the run directory;
// only a stored image of an accepted type has one.
func attachmentPath(image DescriptionImage) (string, bool) {
	extension, ok := attachmentExtensions[image.ContentType]
	if image.State != AttachmentStored || !ok || image.SHA256 == "" {
		return "", false
	}
	return "attachments/" + image.ID + "." + extension, true
}

// attachmentFileName is the only name an unpacked image may have.
var attachmentFileName = regexp.MustCompile(`^attachments/att_[0-9a-f]{32}\.(?:png|jpg|gif|webp|mp4|webm)$`)

// DescriptionAttachments lists the stored images for the bundle, each under
// the nearest heading above it in the untrimmed body.
func DescriptionAttachments(body string, images []DescriptionImage) []Attachment {
	var attachments []Attachment
	for _, image := range images {
		path, ok := attachmentPath(image)
		if !ok {
			continue
		}
		attachments = append(attachments, Attachment{
			ID: image.ID, Path: path, SourceURL: image.SourceURL, SourceURLs: image.SourceURLs, Origins: image.Origins, ContentType: image.ContentType,
			Alt: image.Alt, Heading: headingAbove(body, image.SourceURL), Context: contextAround(body, image.SourceURL), Position: image.Position,
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

// An image's context keeps up to contextSideBytes of the lines before and
// after its own, and contextLineBytes on each side of it on its line, so it
// fits the alt text cap (max_image_alt_bytes, 512) whole.
const (
	contextSideBytes = 160
	contextLineBytes = 80
)

var (
	htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	// A media reference outside a tag: a markdown image or a bare upload URL.
	mediaReference = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)|` + markdownUpload.String())
	htmlTag        = regexp.MustCompile(`</?[a-zA-Z][^>]*>`)
	mediaTag       = regexp.MustCompile(`(?i)^<(?:img|video|source)\b[^>]*\bsrc=`)
	blankRun       = regexp.MustCompile(`[ \t]+`)
)

// contextAround is the description text around source, within its heading's
// section and outside code fences: its own line and the lines before and
// after it, each part cut at whitespace to its limit. Authors caption media above, below, in a table's
// header or a details summary, so the reader decides which part describes
// it. The image itself reads [this], any other media [media].
func contextAround(body, source string) string {
	body = strings.ReplaceAll(htmlComment.ReplaceAllString(body, ""), "\x00", "")
	at := strings.Index(body, source)
	if at < 0 || source == "" {
		return ""
	}
	lines := strings.Split(body, "\n")
	line := strings.Count(body[:at], "\n")
	start, end, fenced := 0, len(lines), false
	for i, l := range lines {
		if trimmed := strings.TrimLeft(l, " "); strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fenced = !fenced
			lines[i] = ""
			continue
		}
		switch {
		case fenced:
			lines[i] = ""
		case i != line && atxHeading.MatchString(strings.TrimRight(l, "\r")):
			if i > line {
				end = i
			} else {
				start = i + 1
			}
		}
		if end < len(lines) {
			break
		}
	}
	own := contextText(lines[line:line+1], source)
	if at := strings.Index(own, thisMark); at >= 0 {
		own = lastWords(own[:at], contextLineBytes) + thisMark + firstWords(own[at+len(thisMark):], contextLineBytes)
	} else {
		own = firstWords(own, 2*contextLineBytes)
	}
	before := lastWords(contextText(lines[start:line], source), contextSideBytes)
	after := firstWords(contextText(lines[line+1:end], source), contextSideBytes)
	context := strings.TrimSpace(strings.Join([]string{before, own, after}, "\n"))
	return strings.ReplaceAll(context, thisMark, "[this]")
}

// firstWords keeps the whole words of s that fit in limit bytes. Cuts fall
// only on whitespace, so the redaction that runs later sees every token
// whole.
func firstWords(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:max(strings.LastIndexAny(s[:limit+1], " \n"), 0)] + "…"
}

// lastWords is firstWords from the end of s, starting at a line when one
// fits, so a secret's keyword stays with its value on the line.
func lastWords(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	from := len(s) - limit - 1
	at := strings.IndexByte(s[from:], '\n')
	if at < 0 {
		at = strings.IndexByte(s[from:], ' ')
	}
	if at >= 0 {
		return "…" + s[from+at+1:]
	}
	return "…"
}

// thisMark stands for the image while its context is cut. The text never
// holds it (contextAround drops NUL bytes), unlike "[this]", and it is as
// long, so the cuts measure the context as it ends up.
const thisMark = "\x00this\x00"

// thisRun is the image's mark repeated with only space between, as a linked
// image gives.
var thisRun = regexp.MustCompile(regexp.QuoteMeta(thisMark) + `(?:\s*` + regexp.QuoteMeta(thisMark) + `)+`)

// contextText joins lines as plain text with media replaced and empty lines
// dropped: a tag holding source reads as the image (once for a run of them,
// as a linked image has two), an img, video or source tag [media], and any
// other tag a space, so no text is spliced together.
func contextText(lines []string, source string) string {
	var kept []string
	for _, l := range lines {
		l = htmlTag.ReplaceAllStringFunc(l, func(tag string) string {
			switch {
			case strings.Contains(tag, source):
				return thisMark
			case mediaTag.MatchString(tag):
				return "[media]"
			}
			return " "
		})
		l = mediaReference.ReplaceAllStringFunc(l, func(media string) string {
			if strings.Contains(media, source) {
				return thisMark
			}
			return "[media]"
		})
		l = thisRun.ReplaceAllString(l, thisMark)
		l = strings.TrimSpace(blankRun.ReplaceAllString(l, " "))
		if l != "" {
			kept = append(kept, l)
		}
	}
	return strings.Join(kept, "\n")
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
		path, ok := attachmentPath(image)
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
// anything else, missing a listed image or not matching its hash fails with
// ErrInvalidArchive. An empty archive path means the run has no images.
//
// The caller must bound the archive's size before calling (see
// MaxAttachmentsArchiveBytes). A maxImageBytes of zero puts no per-entry
// limit before the hash check; a listed video may reach MaxVideoBytes.
func UnpackAttachments(ctx context.Context, archive, dir string, attachments []Attachment, maxImageBytes int64) error {
	if archive == "" {
		if len(attachments) > 0 {
			return ErrInvalidArchive
		}
		return nil
	}
	listed := map[string]Attachment{}
	for _, attachment := range attachments {
		if !attachmentFileName.MatchString(attachment.Path) {
			return ErrInvalidArchive
		}
		listed[attachment.Path] = attachment
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
		attachment, ok := listed[entry.Name]
		limit := maxImageBytes
		if IsVideo(attachment.ContentType) && limit > 0 {
			limit = max(limit, MaxVideoBytes)
		}
		if !ok || entry.Typeflag != tar.TypeReg || entry.Size < 0 || (limit > 0 && entry.Size > limit) {
			return ErrInvalidArchive
		}
		delete(listed, entry.Name)
		output, err := os.OpenFile(filepath.Join(dir, entry.Name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.CopyN(io.MultiWriter(output, hash), reader, entry.Size)
		if err := errors.Join(copyErr, output.Close()); err != nil || hex.EncodeToString(hash.Sum(nil)) != attachment.SHA256 {
			return ErrInvalidArchive
		}
	}
	if len(listed) > 0 {
		return ErrInvalidArchive
	}
	return os.Chmod(root, 0500)
}
