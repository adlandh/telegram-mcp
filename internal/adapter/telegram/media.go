package telegram

import (
	"context"
	"fmt"
	"io"
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
			info.Type, info.MIME, info.SizeBytes, info.Downloadable = "document", doc.MimeType, doc.Size, true
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
	data     []byte
	size     int64
	name     string
}

func selectSize(sizes []tg.PhotoSizeClass, preview bool) (tg.PhotoSizeClass, error) {
	var best tg.PhotoSizeClass
	var stripped *tg.PhotoStrippedSize
	bestArea := 0
	for _, size := range sizes {
		if s, ok := size.(*tg.PhotoStrippedSize); ok {
			stripped = s
		}
		s, ok := size.(interface {
			GetW() int
			GetH() int
		})
		if !ok {
			continue
		}
		if preview && (s.GetW() > 320 || s.GetH() > 320) {
			continue
		}
		if area := s.GetW() * s.GetH(); area > bestArea {
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

func fileFor(m *tg.Message, preview bool) (mediaFile, error) {
	var file mediaFile
	var sizes []tg.PhotoSizeClass
	var location func(string) tg.InputFileLocationClass
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
		info := mediaInfo(m)
		file.name, file.size = info.FileName, doc.Size
		if file.name == "" {
			ext := map[string]string{"video/mp4": ".mp4", "image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "audio/ogg": ".ogg", "audio/mpeg": ".mp3", "application/pdf": ".pdf", "application/zip": ".zip"}[doc.MimeType]
			if ext == "" {
				ext = ".bin"
			}
			file.name = info.Type + ext
		}
		if !preview {
			file.location = doc.AsInputDocumentFileLocation("")
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
	switch size := size.(type) {
	case *tg.PhotoCachedSize:
		file.data = size.Bytes
	case *tg.PhotoStrippedSize:
		file.data, err = thumbnail.Expand(size.Bytes)
		if err != nil {
			return file, err
		}
	default:
		file.location = location(size.GetType())
	}
	return file, nil
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
	return n, err
}

func (c *Client) Download(ctx context.Context, chat string, id int, preview bool, maxBytes int64) (domain.Download, error) {
	raw, entities, r, err := c.rawMessage(ctx, chat, id)
	if err != nil {
		return domain.Download{}, err
	}
	m, ok := raw.(*tg.Message)
	if !ok {
		return domain.Download{}, fmt.Errorf("message has no downloadable media")
	}
	// Fresh response entities supersede cached protection in both directions.
	protected := r.protected
	if fresh := describe(m.PeerID, entities); fresh.known {
		protected = fresh.protected
	}
	if m.Noforwards || protected {
		return domain.Download{}, fmt.Errorf("this chat restricts saving content (no-forward)")
	}
	file, err := fileFor(m, preview)
	if err != nil {
		return domain.Download{}, err
	}
	if maxBytes > 0 && file.size > maxBytes {
		return domain.Download{}, fmt.Errorf("file exceeds the effective size limit; maxMB can only lower TELEGRAM_MAX_DOWNLOAD_MB")
	}
	return saveDownload(c.downloadDir, fmt.Sprintf("%s_%d_%s", r.info.ID, id, safeName(file.name)), maxBytes, func(w io.Writer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if file.data != nil {
			_, err := w.Write(file.data)
			return err
		}
		// gotd follows FILE_MIGRATE to other DCs and caches those connections.
		_, err := downloader.NewDownloader().Download(c.api, file.location).Stream(ctx, w)
		return err
	})
}

func saveDownload(dir, name string, maxBytes int64, download func(io.Writer) error) (domain.Download, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return domain.Download{}, err
	}
	name = safeName(name)
	ext := filepath.Ext(name)
	file, err := os.CreateTemp(dir, "."+strings.TrimSuffix(name, ext)+"-*"+ext+".part")
	if err != nil {
		return domain.Download{}, err
	}
	defer func() { _ = file.Close(); _ = os.Remove(file.Name()) }()
	w := &cappedWriter{writer: file, limit: maxBytes}
	if err := download(w); err != nil {
		return domain.Download{}, fmt.Errorf("download failed: %w", err)
	}
	if err := file.Close(); err != nil {
		return domain.Download{}, err
	}
	path := filepath.Join(dir, strings.TrimSuffix(strings.TrimPrefix(filepath.Base(file.Name()), "."), ".part"))
	if err := os.Rename(file.Name(), path); err != nil {
		return domain.Download{}, err
	}
	return domain.Download{Path: path, Bytes: w.written}, nil
}
