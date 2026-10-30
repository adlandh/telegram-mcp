package telegram

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/adlandh/telegram-mcp/internal/domain"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/telegram/thumbnail"
	"github.com/gotd/td/tg"
)

func mediaInfo(m *tg.Message) domain.Media {
	info := domain.Media{Type: "none"}
	if m.Media == nil {
		return info
	}

	info.Type = m.Media.TypeName()
	switch media := m.Media.(type) {
	case *tg.MessageMediaEmpty:
		info.Type = "none"
	case *tg.MessageMediaPhoto:
		if photo, ok := media.Photo.(*tg.Photo); ok {
			info.Type, info.MIME, info.Downloadable = "photo", "image/jpeg", true
			for _, size := range photo.Sizes {
				info.SizeBytes = max(info.SizeBytes, photoBytes(size))
			}
		}
	case *tg.MessageMediaDocument:
		if doc, ok := media.Document.(*tg.Document); ok {
			info = documentInfo(doc)
		}
	}

	return info
}

func documentInfo(doc *tg.Document) domain.Media {
	info := domain.Media{Type: "document", MIME: doc.MimeType, SizeBytes: doc.Size, Downloadable: true}
	if strings.HasPrefix(doc.MimeType, "image/") {
		info.Type = "image"
	}

	for _, attr := range doc.Attributes {
		switch a := attr.(type) {
		case *tg.DocumentAttributeFilename:
			info.FileName = a.FileName
		case *tg.DocumentAttributeVideo:
			info.Type, info.Duration = "video", a.Duration
		case *tg.DocumentAttributeAudio:
			info.Type, info.Duration = "audio", float64(a.Duration)
			if a.Voice {
				info.Type = "voice"
			}
		}
	}

	return info
}

func photoBytes(size tg.PhotoSizeClass) int64 {
	switch s := size.(type) {
	case *tg.PhotoSize:
		return int64(s.Size)
	case *tg.PhotoCachedSize:
		return int64(len(s.Bytes))
	case *tg.PhotoSizeProgressive:
		if len(s.Sizes) > 0 {
			return int64(slices.Max(s.Sizes))
		}
	}

	return 0
}

type mediaFile struct {
	location tg.InputFileLocationClass
	name     string
	data     []byte
	size     int64
	exact    int64
}

func selectSize(sizes []tg.PhotoSizeClass, preview bool) (tg.PhotoSizeClass, error) {
	var (
		best     tg.PhotoSizeClass
		stripped *tg.PhotoStrippedSize
	)

	bestArea := 0

	for _, size := range sizes {
		if s, ok := size.(*tg.PhotoStrippedSize); ok {
			stripped = s
		}

		if area := photoArea(size, preview); area > bestArea {
			best, bestArea = size, area
		}
	}

	if best == nil && stripped != nil && preview {
		return stripped, nil
	}

	if best == nil {
		return nil, fmt.Errorf("no suitable photo or thumbnail available")
	}

	return best, nil
}

// photoArea returns a size's pixel area, or 0 when it has no dimensions or is too large for a preview.
func photoArea(size tg.PhotoSizeClass, preview bool) int {
	s, ok := size.(interface {
		GetW() int
		GetH() int
	})
	if !ok || preview && (s.GetW() > 320 || s.GetH() > 320) {
		return 0
	}

	return s.GetW() * s.GetH()
}

func fileFor(m *tg.Message, preview bool) (mediaFile, error) {
	var (
		file     mediaFile
		sizes    []tg.PhotoSizeClass
		location func(string) tg.InputFileLocationClass
	)

	switch media := m.Media.(type) {
	case *tg.MessageMediaPhoto:
		photo, ok := media.Photo.(*tg.Photo)
		if !ok {
			return file, fmt.Errorf("photo is unavailable")
		}

		file.name, sizes = "photo.jpg", photo.Sizes
		location = func(size string) tg.InputFileLocationClass { return photo.AsInputPhotoFileLocation(size) }
	case *tg.MessageMediaDocument:
		doc, ok := media.Document.(*tg.Document)
		if !ok {
			return file, fmt.Errorf("document is unavailable")
		}

		file.name, file.size = documentName(doc), doc.Size
		if !preview {
			file.location, file.exact = doc.AsInputDocumentFileLocation(""), doc.Size
			return file, nil
		}

		sizes = doc.Thumbs
		location = func(size string) tg.InputFileLocationClass { return doc.AsInputDocumentFileLocation(size) }
	default:
		return file, fmt.Errorf("message has no downloadable media")
	}

	size, err := selectSize(sizes, preview)
	if err != nil {
		return file, err
	}

	file.size = photoBytes(size)
	if preview {
		file.name = "thumb.jpg"
	}

	err = file.load(size, location)

	return file, err
}

// documentName returns the document's file name, or one built from its media type and MIME type.
func documentName(doc *tg.Document) string {
	info := documentInfo(doc)
	if info.FileName != "" {
		return info.FileName
	}

	ext := map[string]string{"video/mp4": ".mp4", "image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "audio/ogg": ".ogg", "audio/mpeg": ".mp3", "application/pdf": ".pdf", "application/zip": ".zip"}[doc.MimeType]
	if ext == "" {
		ext = ".bin"
	}

	return info.Type + ext
}

// load takes inline sizes from the response and points other sizes at their file location.
func (file *mediaFile) load(size tg.PhotoSizeClass, location func(string) tg.InputFileLocationClass) error {
	switch size := size.(type) {
	case *tg.PhotoCachedSize:
		file.data = size.Bytes
	case *tg.PhotoStrippedSize:
		data, err := thumbnail.Expand(size.Bytes)
		if err != nil {
			return fmt.Errorf("expand thumbnail: %w", err)
		}

		file.data = data
	default:
		file.location = location(size.GetType())
	}

	return nil
}

func safeName(name string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune("/\\:*?\"<>|", r) {
			return '_'
		}

		return r
	}, name)
	name = strings.Trim(name, " .")

	runes := []rune(name)
	if len(runes) > 50 {
		// Keep a short extension so the file stays openable by type.
		ext := []rune(filepath.Ext(name))
		if len(ext) > 10 {
			ext = nil
		}

		runes = append(runes[:50-len(ext)], ext...)
	}

	if len(runes) == 0 {
		return "media.bin"
	}

	return string(runes)
}

type cappedWriter struct {
	writer         io.Writer
	limit, written int64
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if w.limit > 0 && int64(len(p)) > w.limit-w.written {
		return 0, fmt.Errorf("download exceeds the effective size limit; maxMB can only lower TELEGRAM_MAX_DOWNLOAD_MB")
	}

	n, err := w.writer.Write(p)
	w.written += int64(n)

	if err != nil {
		return n, fmt.Errorf("write: %w", err)
	}

	return n, nil
}

func (c *Client) Download(ctx context.Context, chat string, id int, preview bool, maxBytes int64) (domain.Download, error) {
	name, exact, stream, err := c.media(ctx, chat, id, preview, maxBytes)
	if err != nil {
		return domain.Download{}, err
	}

	return saveDownload(c.downloadDir, name, maxBytes, exact, stream)
}

func (c *Client) Preview(ctx context.Context, chat string, id int, maxBytes int64) (domain.Image, error) {
	_, _, stream, err := c.media(ctx, chat, id, true, maxBytes)
	if err != nil {
		return domain.Image{}, err
	}

	var buf bytes.Buffer
	if err := stream(&cappedWriter{writer: &buf, limit: maxBytes}); err != nil {
		return domain.Image{}, fmt.Errorf("download failed: %w", err)
	}

	mime := http.DetectContentType(buf.Bytes())
	if !strings.HasPrefix(mime, "image/") {
		return domain.Image{}, fmt.Errorf("preview is not an image (%s)", mime)
	}

	return domain.Image{Data: buf.Bytes(), MIME: mime}, nil
}

// media resolves a message's media for Download and Preview: it refuses
// protected content and declared sizes over maxBytes, and returns the file
// name, the exact expected size, and a function streaming the payload.
func (c *Client) media(ctx context.Context, chat string, id int, preview bool, maxBytes int64) (string, int64, func(io.Writer) error, error) {
	raw, entities, r, err := c.rawMessage(ctx, chat, id)
	if err != nil {
		return "", 0, nil, err
	}

	m, ok := raw.(*tg.Message)
	if !ok {
		return "", 0, nil, fmt.Errorf("message has no downloadable media")
	}
	// Fresh response entities supersede cached protection in both directions.
	protected := r.protected
	if fresh := describe(m.PeerID, entities); fresh.known {
		protected = fresh.protected
	}

	if m.Noforwards || protected {
		return "", 0, nil, fmt.Errorf("this chat restricts saving content (no-forward)")
	}

	file, err := fileFor(m, preview)
	if err != nil {
		return "", 0, nil, err
	}

	if maxBytes > 0 && file.size > maxBytes {
		return "", 0, nil, fmt.Errorf("file exceeds the effective size limit; maxMB can only lower TELEGRAM_MAX_DOWNLOAD_MB")
	}

	return fmt.Sprintf("%s_%d_%s", r.info.ID, id, file.name), file.exact, c.stream(ctx, file), nil
}

// stream returns a function writing file's payload: inline data as is, anything else from Telegram.
func (c *Client) stream(ctx context.Context, file mediaFile) func(io.Writer) error {
	return func(w io.Writer) error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("start download: %w", err)
		}

		if file.data != nil {
			if _, err := w.Write(file.data); err != nil {
				return fmt.Errorf("write inline media: %w", err)
			}

			return nil
		}
		// gotd follows FILE_MIGRATE to other DCs and caches those connections.
		if _, err := downloader.NewDownloader().Download(c.api, file.location).Stream(ctx, w); err != nil {
			return fmt.Errorf("stream media: %w", err)
		}

		return nil
	}
}

func saveDownload(dir, name string, maxBytes, exact int64, download func(io.Writer) error) (domain.Download, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return domain.Download{}, fmt.Errorf("create download directory: %w", err)
	}

	name = safeName(name)
	ext := filepath.Ext(name)

	file, err := os.CreateTemp(dir, "."+strings.TrimSuffix(name, ext)+"-*"+ext+".part")
	if err != nil {
		return domain.Download{}, fmt.Errorf("create download file: %w", err)
	}

	defer func() { _ = file.Close(); _ = os.Remove(file.Name()) }()

	w := &cappedWriter{writer: file, limit: maxBytes}
	if err := download(w); err != nil {
		return domain.Download{}, fmt.Errorf("download failed: %w", err)
	}

	if exact > 0 && w.written != exact {
		return domain.Download{}, fmt.Errorf("download incomplete: got %d of %d bytes", w.written, exact)
	}

	if err := file.Close(); err != nil {
		return domain.Download{}, fmt.Errorf("close download file: %w", err)
	}

	path := filepath.Join(dir, strings.TrimSuffix(strings.TrimPrefix(filepath.Base(file.Name()), "."), ".part"))
	if err := os.Rename(file.Name(), path); err != nil {
		return domain.Download{}, fmt.Errorf("save download: %w", err)
	}

	return domain.Download{Path: path, Bytes: w.written}, nil
}
