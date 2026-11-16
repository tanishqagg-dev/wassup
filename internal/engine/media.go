package engine

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"math"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	_ "golang.org/x/image/bmp"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waMmsRetry"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/tanishqagg-dev/wassup/internal/api"
	"github.com/tanishqagg-dev/wassup/internal/store"
)

const maxUpload = 2 << 30 // WhatsApp's document limit

type prepared struct {
	msg    *waE2E.Message
	kind   string
	handle string
}

func expandPath(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func detectMime(path string, head []byte) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".opus":
		return "audio/ogg"
	case ".heic", ".heif":
		return "image/heic"
	case ".webp":
		return "image/webp"
	case ".m4a":
		return "audio/mp4"
	case ".mp3":
		return "audio/mpeg"
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return strings.SplitN(t, ";", 2)[0]
	}
	return strings.SplitN(http.DetectContentType(head), ";", 2)[0]
}

func have(tool string) bool { _, err := exec.LookPath(tool); return err == nil }

func run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 400 {
			msg = msg[len(msg)-400:]
		}
		return nil, fmt.Errorf("%s failed: %v: %s", name, err, msg)
	}
	return out, nil
}

type probe struct {
	Seconds       float64
	Width, Height int
}

func ffprobe(ctx context.Context, path string) probe {
	var p probe
	if !have("ffprobe") {
		return p
	}
	out, err := run(ctx, "ffprobe", "-v", "error", "-print_format", "json", "-show_format", "-show_streams", path)
	if err != nil {
		return p
	}
	var res struct {
		Format  struct{ Duration string } `json:"format"`
		Streams []struct {
			CodecType string `json:"codec_type"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"streams"`
	}
	if json.Unmarshal(out, &res) == nil {
		p.Seconds, _ = strconv.ParseFloat(res.Format.Duration, 64)
		for _, s := range res.Streams {
			if s.CodecType == "video" && p.Width == 0 {
				p.Width, p.Height = s.Width, s.Height
			}
		}
	}
	return p
}

func tempPath(ext string) string {
	f, err := os.CreateTemp("", "wassup-*"+ext)
	if err != nil {
		return filepath.Join(os.TempDir(), fmt.Sprintf("wassup-%d%s", time.Now().UnixNano(), ext))
	}
	name := f.Name()
	f.Close()
	return name
}

// resizeFit scales img down so it fits within max x max.
func resizeFit(img image.Image, max int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= max && h <= max {
		return img
	}
	scale := float64(max) / math.Max(float64(w), float64(h))
	nw, nh := int(math.Max(1, float64(w)*scale)), int(math.Max(1, float64(h)*scale))
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	return dst
}

func flatten(img image.Image) image.Image {
	b := img.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, &image.Uniform{color.White}, image.Point{}, draw.Src)
	draw.Draw(dst, b, img, b.Min, draw.Over)
	return dst
}

func jpegBytes(img image.Image, q int) []byte {
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, flatten(img), &jpeg.Options{Quality: q})
	return buf.Bytes()
}

func thumbnailOf(img image.Image) []byte { return jpegBytes(resizeFit(img, 100), 60) }

// toJPEG converts any image we can read (or that sips/ffmpeg can read) to JPEG bytes.
func toJPEG(ctx context.Context, path string, data []byte) ([]byte, image.Image, error) {
	if img, _, err := image.Decode(bytes.NewReader(data)); err == nil {
		return jpegBytes(img, 90), img, nil
	}
	out := tempPath(".jpg")
	defer os.Remove(out)
	var err error
	switch {
	case runtime.GOOS == "darwin" && have("sips"):
		_, err = run(ctx, "sips", "-s", "format", "jpeg", path, "--out", out)
	case have("ffmpeg"):
		_, err = run(ctx, "ffmpeg", "-y", "-loglevel", "error", "-i", path, "-frames:v", "1", out)
	default:
		err = errors.New("no converter found (install ffmpeg)")
	}
	if err != nil {
		return nil, nil, err
	}
	jb, err := os.ReadFile(out)
	if err != nil {
		return nil, nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(jb))
	return jb, img, err
}

// voiceWaveform builds WhatsApp's 64-sample waveform from decoded PCM.
func voiceWaveform(ctx context.Context, path string) []byte {
	if !have("ffmpeg") {
		return nil
	}
	pcm, err := run(ctx, "ffmpeg", "-loglevel", "error", "-i", path, "-ac", "1", "-ar", "8000", "-f", "s16le", "-")
	if err != nil || len(pcm) < 128 {
		return nil
	}
	samples := len(pcm) / 2
	const n = 64
	per := samples / n
	if per == 0 {
		return nil
	}
	vals := make([]float64, n)
	peak := 0.0
	for i := 0; i < n; i++ {
		var sum float64
		for j := 0; j < per; j++ {
			v := int16(binary.LittleEndian.Uint16(pcm[(i*per+j)*2:]))
			sum += math.Abs(float64(v))
		}
		vals[i] = sum / float64(per)
		peak = math.Max(peak, vals[i])
	}
	out := make([]byte, n)
	for i, v := range vals {
		if peak > 0 {
			out[i] = byte(math.Round(v / peak * 100))
		}
	}
	return out
}

func autoKind(mimeType string) string {
	switch {
	case mimeType == "image/gif":
		return "gif"
	case strings.HasPrefix(mimeType, "image/"):
		return "image"
	case strings.HasPrefix(mimeType, "video/"):
		return "video"
	case strings.HasPrefix(mimeType, "audio/"):
		return "audio"
	}
	return "document"
}

// prepareMedia converts (when needed), uploads and wraps a file in the right message type.
func (e *Engine) prepareMedia(ctx context.Context, to types.JID, path, as, caption, fileName string) (*prepared, error) {
	path = expandPath(path)
	st, err := os.Stat(path)
	if err != nil {
		return nil, usage("can't read file %s: %v", path, err)
	}
	if st.IsDir() {
		return nil, usage("%s is a folder, not a file", path)
	}
	if st.Size() == 0 {
		return nil, usage("%s is empty", path)
	}
	if st.Size() > maxUpload {
		return nil, usage("%s is larger than WhatsApp's 2 GB limit", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	mt := detectMime(path, head)
	kind := strings.ToLower(as)
	if kind == "" || kind == "auto" {
		kind = autoKind(mt)
	}
	if fileName == "" {
		fileName = filepath.Base(path)
	}
	var tmp []string
	defer func() {
		for _, t := range tmp {
			os.Remove(t)
		}
	}()
	convert := func(ext string, args ...string) (string, error) {
		if !have("ffmpeg") {
			return "", usage("converting %s to %s needs ffmpeg", filepath.Base(path), kind).WithHint("install ffmpeg (brew install ffmpeg) or send with --as document")
		}
		out := tempPath(ext)
		tmp = append(tmp, out)
		full := append([]string{"-y", "-loglevel", "error", "-i", path}, args...)
		full = append(full, out)
		if _, err := run(ctx, "ffmpeg", full...); err != nil {
			return "", err
		}
		return out, nil
	}

	msg := &waE2E.Message{}
	var mediaType whatsmeow.MediaType
	var fill func(up whatsmeow.UploadResponse)
	switch kind {
	case "image", "photo":
		kind = "image"
		var img image.Image
		if mt != "image/jpeg" {
			data, img, err = toJPEG(ctx, path, data)
			if err != nil {
				return nil, usage("can't convert %s to JPEG: %v", filepath.Base(path), err).WithHint("send it with --as document instead")
			}
			mt = "image/jpeg"
		} else if img, _, err = image.Decode(bytes.NewReader(data)); err != nil {
			img = nil
		}
		im := &waE2E.ImageMessage{Mimetype: new(mt), Caption: optStr(caption)}
		if img != nil {
			im.JPEGThumbnail = thumbnailOf(img)
			im.Width, im.Height = proto.Uint32(uint32(img.Bounds().Dx())), proto.Uint32(uint32(img.Bounds().Dy()))
		}
		mediaType = whatsmeow.MediaImage
		msg.ImageMessage = im
		fill = func(up whatsmeow.UploadResponse) {
			im.URL, im.DirectPath, im.MediaKey, im.FileEncSHA256, im.FileSHA256, im.FileLength = new(up.URL), new(up.DirectPath), up.MediaKey, up.FileEncSHA256, up.FileSHA256, new(up.FileLength)
		}
	case "video", "gif", "video_note":
		src := path
		gif := kind == "gif"
		if mt != "video/mp4" || gif && mt == "image/gif" {
			out, err := convert(".mp4", "-movflags", "+faststart", "-pix_fmt", "yuv420p", "-vf", "scale=trunc(iw/2)*2:trunc(ih/2)*2", "-c:v", "libx264", "-preset", "veryfast", "-c:a", "aac")
			if err != nil {
				return nil, err
			}
			src = out
			if data, err = os.ReadFile(out); err != nil {
				return nil, err
			}
			mt = "video/mp4"
		}
		p := ffprobe(ctx, src)
		vm := &waE2E.VideoMessage{Mimetype: new(mt), Caption: optStr(caption)}
		if gif {
			vm.GifPlayback = new(true)
		}
		if p.Seconds > 0 {
			vm.Seconds = proto.Uint32(uint32(math.Ceil(p.Seconds)))
		}
		if p.Width > 0 {
			vm.Width, vm.Height = proto.Uint32(uint32(p.Width)), proto.Uint32(uint32(p.Height))
		}
		if have("ffmpeg") {
			thumb := tempPath(".jpg")
			tmp = append(tmp, thumb)
			if _, err := run(ctx, "ffmpeg", "-y", "-loglevel", "error", "-ss", "0.3", "-i", src, "-frames:v", "1", "-vf", "scale=200:-2", thumb); err == nil {
				if tb, err := os.ReadFile(thumb); err == nil {
					vm.JPEGThumbnail = tb
				}
			}
		}
		mediaType = whatsmeow.MediaVideo
		if kind == "video_note" {
			msg.PtvMessage = vm
			kind = "video_note"
		} else {
			msg.VideoMessage = vm
		}
		fill = func(up whatsmeow.UploadResponse) {
			vm.URL, vm.DirectPath, vm.MediaKey, vm.FileEncSHA256, vm.FileSHA256, vm.FileLength = new(up.URL), new(up.DirectPath), up.MediaKey, up.FileEncSHA256, up.FileSHA256, new(up.FileLength)
		}
	case "voice", "ptt":
		kind = "voice"
		src := path
		isOpus := mt == "audio/ogg" || strings.HasSuffix(strings.ToLower(path), ".opus")
		if !isOpus {
			out, err := convert(".ogg", "-vn", "-ac", "1", "-ar", "48000", "-c:a", "libopus", "-b:a", "32k", "-application", "voip")
			if err != nil {
				return nil, err
			}
			src = out
			if data, err = os.ReadFile(out); err != nil {
				return nil, err
			}
		}
		p := ffprobe(ctx, src)
		am := &waE2E.AudioMessage{Mimetype: new("audio/ogg; codecs=opus"), PTT: new(true)}
		if p.Seconds > 0 {
			am.Seconds = proto.Uint32(uint32(math.Ceil(p.Seconds)))
		}
		am.Waveform = voiceWaveform(ctx, src)
		mediaType = whatsmeow.MediaAudio
		msg.AudioMessage = am
		fill = func(up whatsmeow.UploadResponse) {
			am.URL, am.DirectPath, am.MediaKey, am.FileEncSHA256, am.FileSHA256, am.FileLength = new(up.URL), new(up.DirectPath), up.MediaKey, up.FileEncSHA256, up.FileSHA256, new(up.FileLength)
		}
	case "audio", "music":
		kind = "audio"
		src := path
		switch mt {
		case "audio/mpeg", "audio/mp4", "audio/aac", "audio/ogg", "audio/amr":
		default:
			out, err := convert(".m4a", "-vn", "-c:a", "aac", "-b:a", "128k")
			if err != nil {
				return nil, err
			}
			src, mt = out, "audio/mp4"
			if data, err = os.ReadFile(out); err != nil {
				return nil, err
			}
		}
		p := ffprobe(ctx, src)
		am := &waE2E.AudioMessage{Mimetype: new(mt)}
		if p.Seconds > 0 {
			am.Seconds = proto.Uint32(uint32(math.Ceil(p.Seconds)))
		}
		mediaType = whatsmeow.MediaAudio
		msg.AudioMessage = am
		fill = func(up whatsmeow.UploadResponse) {
			am.URL, am.DirectPath, am.MediaKey, am.FileEncSHA256, am.FileSHA256, am.FileLength = new(up.URL), new(up.DirectPath), up.MediaKey, up.FileEncSHA256, up.FileSHA256, new(up.FileLength)
		}
	case "sticker":
		if mt != "image/webp" {
			data, err = toSticker(ctx, path, data)
			if err != nil {
				return nil, err
			}
		}
		sm := &waE2E.StickerMessage{Mimetype: new("image/webp"), Width: proto.Uint32(512), Height: proto.Uint32(512)}
		mediaType = whatsmeow.MediaImage
		msg.StickerMessage = sm
		fill = func(up whatsmeow.UploadResponse) {
			sm.URL, sm.DirectPath, sm.MediaKey, sm.FileEncSHA256, sm.FileSHA256, sm.FileLength = new(up.URL), new(up.DirectPath), up.MediaKey, up.FileEncSHA256, up.FileSHA256, new(up.FileLength)
		}
	case "document", "doc", "file":
		kind = "document"
		if mt == "" {
			mt = "application/octet-stream"
		}
		dm := &waE2E.DocumentMessage{Mimetype: new(mt), FileName: new(fileName), Title: new(fileName), Caption: optStr(caption)}
		mediaType = whatsmeow.MediaDocument
		msg.DocumentMessage = dm
		fill = func(up whatsmeow.UploadResponse) {
			dm.URL, dm.DirectPath, dm.MediaKey, dm.FileEncSHA256, dm.FileSHA256, dm.FileLength = new(up.URL), new(up.DirectPath), up.MediaKey, up.FileEncSHA256, up.FileSHA256, new(up.FileLength)
		}
	default:
		return nil, usage("unknown --as %q (use image, video, gif, audio, voice, document or sticker)", as)
	}

	cl := e.Client()
	var up whatsmeow.UploadResponse
	if to.Server == types.NewsletterServer {
		up, err = cl.UploadNewsletter(ctx, data, mediaType)
	} else {
		up, err = cl.Upload(ctx, data, mediaType)
	}
	if err != nil {
		return nil, fmt.Errorf("upload failed: %w", err)
	}
	if to.Server == types.NewsletterServer {
		up.MediaKey, up.FileEncSHA256 = nil, nil
	}
	fill(up)
	return &prepared{msg: msg, kind: kind, handle: up.Handle}, nil
}

func toSticker(ctx context.Context, path string, data []byte) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		_, img, err = toJPEG(ctx, path, data)
		if err != nil {
			return nil, usage("can't read %s as an image: %v", filepath.Base(path), err)
		}
	}
	fit := resizeFit(img, 512)
	canvas := image.NewRGBA(image.Rect(0, 0, 512, 512))
	off := image.Pt((512-fit.Bounds().Dx())/2, (512-fit.Bounds().Dy())/2)
	draw.Draw(canvas, fit.Bounds().Add(off), fit, fit.Bounds().Min, draw.Over)
	pngPath := tempPath(".png")
	webpPath := tempPath(".webp")
	defer os.Remove(pngPath)
	defer os.Remove(webpPath)
	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		return nil, err
	}
	if err := os.WriteFile(pngPath, buf.Bytes(), 0o600); err != nil {
		return nil, err
	}
	switch {
	case have("cwebp"):
		_, err = run(ctx, "cwebp", "-quiet", "-q", "80", pngPath, "-o", webpPath)
	case have("ffmpeg"):
		_, err = run(ctx, "ffmpeg", "-y", "-loglevel", "error", "-i", pngPath, "-c:v", "libwebp", "-q:v", "80", webpPath)
	default:
		return nil, usage("making a sticker needs cwebp or ffmpeg").WithHint("brew install webp, or pass a 512x512 .webp file")
	}
	if err != nil {
		return nil, err
	}
	return os.ReadFile(webpPath)
}

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// downloadable returns the media part of a message.
func downloadable(m *waE2E.Message) (whatsmeow.DownloadableMessage, string) {
	switch {
	case m.GetImageMessage() != nil:
		return m.GetImageMessage(), m.GetImageMessage().GetMimetype()
	case m.GetVideoMessage() != nil:
		return m.GetVideoMessage(), m.GetVideoMessage().GetMimetype()
	case m.GetPtvMessage() != nil:
		return m.GetPtvMessage(), m.GetPtvMessage().GetMimetype()
	case m.GetAudioMessage() != nil:
		return m.GetAudioMessage(), m.GetAudioMessage().GetMimetype()
	case m.GetDocumentMessage() != nil:
		return m.GetDocumentMessage(), m.GetDocumentMessage().GetMimetype()
	case m.GetStickerMessage() != nil:
		return m.GetStickerMessage(), m.GetStickerMessage().GetMimetype()
	}
	return nil, ""
}

func setDirectPath(m *waE2E.Message, p string) {
	switch {
	case m.GetImageMessage() != nil:
		m.ImageMessage.DirectPath = &p
	case m.GetVideoMessage() != nil:
		m.VideoMessage.DirectPath = &p
	case m.GetPtvMessage() != nil:
		m.PtvMessage.DirectPath = &p
	case m.GetAudioMessage() != nil:
		m.AudioMessage.DirectPath = &p
	case m.GetDocumentMessage() != nil:
		m.DocumentMessage.DirectPath = &p
	case m.GetStickerMessage() != nil:
		m.StickerMessage.DirectPath = &p
	}
}

func extFor(mimeType, fileName string) string {
	if fileName != "" && filepath.Ext(fileName) != "" {
		return filepath.Ext(fileName)
	}
	base := strings.TrimSpace(strings.SplitN(mimeType, ";", 2)[0])
	switch base {
	case "image/jpeg":
		return ".jpg"
	case "audio/ogg":
		return ".ogg"
	case "audio/mpeg":
		return ".mp3"
	case "audio/mp4":
		return ".m4a"
	case "video/mp4":
		return ".mp4"
	case "image/webp":
		return ".webp"
	}
	if exts, _ := mime.ExtensionsByType(base); len(exts) > 0 {
		return exts[0]
	}
	return ".bin"
}

var unsafeName = strings.NewReplacer("/", "_", "\\", "_", ":", "_", "\x00", "", "\n", "_", "\r", "_", "\t", "_", "\u2028", "_", "\u2029", "_")

// downloadMessage fetches, decrypts and saves the media of a stored message.
// If the CDN copy expired it asks the sender's phone to re-upload, like the official apps do.
func (e *Engine) downloadMessage(ctx context.Context, sm *store.Message, out string) (*api.DownloadResult, error) {
	if sm.Revoked {
		return nil, usage("message %s was deleted", sm.ID)
	}
	if out == "" && sm.MediaPath != "" && fileExists(sm.MediaPath) {
		st, _ := os.Stat(sm.MediaPath)
		return &api.DownloadResult{Path: sm.MediaPath, Mime: sm.MediaMime, Size: st.Size()}, nil
	}
	if len(sm.Raw) == 0 {
		return nil, usage("message %s has no stored media keys", sm.ID)
	}
	var m waE2E.Message
	if err := proto.Unmarshal(sm.Raw, &m); err != nil {
		return nil, err
	}
	dm, mt := downloadable(&m)
	if dm == nil {
		return nil, usage("message %s has no media", sm.ID)
	}
	cl := e.Client()
	data, err := cl.Download(ctx, dm)
	if errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith404) || errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith410) || errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith403) {
		e.Log.Info().Str("id", sm.ID).Msg("media expired on CDN, requesting re-upload")
		if path, rerr := e.requestMediaRetry(ctx, sm, dm.GetMediaKey()); rerr != nil {
			return nil, rerr
		} else {
			setDirectPath(&m, path)
			if raw, err := proto.Marshal(&m); err == nil {
				_ = e.DB.SetRaw(ctx, sm.Chat, sm.ID, raw)
			}
			dm, _ = downloadable(&m)
			data, err = cl.Download(ctx, dm)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	ext := extFor(mt, sm.MediaName)
	target := out
	chat, _ := types.ParseJID(sm.Chat)
	defName := sm.ID + ext
	if sm.MediaName != "" {
		name := unsafeName.Replace(sm.MediaName)
		if r := []rune(name); len(r) > 120 {
			name = string(r[len(r)-120:])
		}
		defName = unsafeName.Replace(sm.ID) + "-" + name
	}
	if target == "" {
		target = filepath.Join(e.Acct.MediaDir, unsafeName.Replace(chat.User), defName)
	} else {
		target = expandPath(target)
		if st, err := os.Stat(target); (err == nil && st.IsDir()) || strings.HasSuffix(out, string(os.PathSeparator)) {
			target = filepath.Join(target, defName)
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(target, data, 0o600); err != nil {
		return nil, err
	}
	abs, _ := filepath.Abs(target)
	_ = e.DB.SetMediaPath(ctx, sm.Chat, sm.ID, abs)
	return &api.DownloadResult{Path: abs, Mime: mt, Size: int64(len(data))}, nil
}

func (e *Engine) requestMediaRetry(ctx context.Context, sm *store.Message, mediaKey []byte) (string, error) {
	info := e.infoFor(sm)
	ch := make(chan *events.MediaRetry, 1)
	e.waitMu.Lock()
	e.mediaRetries[sm.ID] = ch
	e.waitMu.Unlock()
	defer func() {
		e.waitMu.Lock()
		delete(e.mediaRetries, sm.ID)
		e.waitMu.Unlock()
	}()
	if err := e.Client().SendMediaRetryReceipt(ctx, info, mediaKey); err != nil {
		return "", fmt.Errorf("media expired and the re-upload request failed: %w", err)
	}
	select {
	case evt := <-ch:
		res, err := whatsmeow.DecryptMediaRetryNotification(evt, mediaKey)
		if err != nil {
			return "", fmt.Errorf("media re-upload failed: %w", err)
		}
		if res.GetResult() != waMmsRetry.MediaRetryNotification_SUCCESS {
			return "", api.Errf(api.CodeNotFound, "the phone no longer has this media (%s)", res.GetResult().String())
		}
		return res.GetDirectPath(), nil
	case <-time.After(45 * time.Second):
		return "", api.Errf(api.CodeTimeout, "media expired and the phone didn't re-upload it within 45s").WithHint("make sure the phone is online, then retry")
	case <-ctx.Done():
		return "", api.Errf(api.CodeTimeout, "cancelled")
	}
}

var dlSem = make(chan struct{}, 3)

func (e *Engine) maybeAutoDownload(sm *store.Message) {
	cfg := e.Config().Media
	if len(cfg.AutoDownload) == 0 {
		return
	}
	want := false
	for _, t := range cfg.AutoDownload {
		if strings.EqualFold(t, sm.Type) || t == "*" || t == "all" {
			want = true
		}
	}
	if !want || (cfg.MaxAutoDownloadMB > 0 && sm.MediaSize > int64(cfg.MaxAutoDownloadMB)<<20) {
		return
	}
	cp := *sm
	go func() {
		dlSem <- struct{}{}
		defer func() { <-dlSem }()
		ctx, cancel := context.WithTimeout(e.ctx, 5*time.Minute)
		defer cancel()
		if res, err := e.downloadMessage(ctx, &cp, ""); err != nil {
			e.Log.Warn().Err(err).Str("id", cp.ID).Msg("auto-download failed")
		} else {
			e.publish(api.Event{Type: "media.downloaded", Chat: cp.Chat, Data: map[string]any{"id": cp.ID, "path": res.Path, "mime": res.Mime, "size": res.Size}})
		}
	}()
}
