package engine

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func writePNG(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 128, 200})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	p := filepath.Join(t.TempDir(), "in.png")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestImageConversions(t *testing.T) {
	ctx := context.Background()
	p := writePNG(t, 300, 200)
	data, _ := os.ReadFile(p)
	jb, img, err := toJPEG(ctx, p, data)
	if err != nil || img == nil || len(jb) < 100 || jb[0] != 0xFF || jb[1] != 0xD8 {
		t.Fatalf("toJPEG: %v", err)
	}
	if th := thumbnailOf(img); len(th) == 0 {
		t.Error("no thumbnail")
	}
	sq, err := squareJPEG(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(sq))
	if err != nil || cfg.Width != cfg.Height {
		t.Errorf("squareJPEG not square: %+v %v", cfg, err)
	}
	if _, err := exec.LookPath("cwebp"); err == nil {
		st, err := toSticker(ctx, p, data)
		if err != nil || !bytes.HasPrefix(st, []byte("RIFF")) {
			t.Errorf("toSticker: %v", err)
		}
	}
}

func TestVoiceConversion(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	ctx := context.Background()
	dir := t.TempDir()
	wav := filepath.Join(dir, "tone.wav")
	if out, err := exec.Command("ffmpeg", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=2", wav).CombinedOutput(); err != nil {
		t.Fatalf("make tone: %v %s", err, out)
	}
	ogg := filepath.Join(dir, "tone.ogg")
	if _, err := run(ctx, "ffmpeg", "-y", "-loglevel", "error", "-i", wav, "-vn", "-ac", "1", "-ar", "48000", "-c:a", "libopus", "-b:a", "32k", "-application", "voip", ogg); err != nil {
		t.Fatalf("opus encode (same args as voice notes): %v", err)
	}
	if p := ffprobe(ctx, ogg); p.Seconds < 1.9 || p.Seconds > 2.2 {
		t.Errorf("duration = %v", p.Seconds)
	}
	wf := voiceWaveform(ctx, ogg)
	if len(wf) != 64 {
		t.Fatalf("waveform length %d", len(wf))
	}
	max := byte(0)
	for _, v := range wf {
		if v > max {
			max = v
		}
	}
	if max != 100 {
		t.Errorf("waveform not normalized, peak %d", max)
	}
}

func TestDetectMime(t *testing.T) {
	cases := map[string]string{"a.jpg": "image/jpeg", "b.opus": "audio/ogg", "c.HEIC": "image/heic", "d.pdf": "application/pdf", "e.mp4": "video/mp4", "f.m4a": "audio/mp4"}
	for name, want := range cases {
		if got := detectMime(name, nil); got != want {
			t.Errorf("%s: %s", name, got)
		}
	}
	if autoKind("image/gif") != "gif" || autoKind("application/zip") != "document" {
		t.Error("autoKind")
	}
	if extFor("audio/ogg; codecs=opus", "") != ".ogg" || extFor("x/y", "report.pdf") != ".pdf" {
		t.Error("extFor")
	}
}
