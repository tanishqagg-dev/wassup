package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tanishqagg-dev/wassup/internal/config"
)

// Real speech in, text out: macOS `say` makes the audio, whisper.cpp transcribes it.
// Skipped unless WASSUP_TEST_WHISPER=1 (it downloads the 75 MB tiny model once).
func TestWhisperTranscribesSpeech(t *testing.T) {
	if os.Getenv("WASSUP_TEST_WHISPER") == "" {
		t.Skip("set WASSUP_TEST_WHISPER=1 to run")
	}
	if whisperBinary() == "" || !have("ffmpeg") {
		t.Skip("whisper.cpp or ffmpeg missing")
	}
	if _, err := exec.LookPath("say"); err != nil {
		t.Skip("needs macOS say")
	}
	if os.Getenv("WASSUP_HOME") == "" {
		home, _ := os.UserHomeDir()
		t.Setenv("WASSUP_HOME", filepath.Join(home, ".wassup"))
	}
	dir := t.TempDir()
	aiff := filepath.Join(dir, "s.aiff")
	if out, err := exec.Command("say", "-o", aiff, "Running ten minutes late, see you at the station").CombinedOutput(); err != nil {
		t.Fatalf("say: %v %s", err, out)
	}
	ogg := filepath.Join(dir, "s.ogg")
	if _, err := run(context.Background(), "ffmpeg", "-y", "-loglevel", "error", "-i", aiff, "-c:a", "libopus", "-ac", "1", ogg); err != nil {
		t.Fatal(err)
	}
	text, err := transcribeWhisper(context.Background(), config.TranscribeConfig{Model: "tiny", Language: "en"}, ogg)
	if err != nil {
		t.Fatal(err)
	}
	low := strings.ToLower(text)
	for _, w := range []string{"late", "station"} {
		if !strings.Contains(low, w) {
			t.Errorf("transcript %q is missing %q", text, w)
		}
	}
	t.Logf("transcript: %s", text)
}
