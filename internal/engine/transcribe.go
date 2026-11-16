package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tanishqagg-dev/wassup/internal/api"
	"github.com/tanishqagg-dev/wassup/internal/config"
	"github.com/tanishqagg-dev/wassup/internal/store"
)

func init() {
	register(&Method{Name: "messages.transcribe", Summary: "Turn a voice note or audio message into text", Params: api.MessageRef{}, H: mTranscribe})
}

var whisperBins = []string{"whisper-cli", "whisper-cpp", "whisper"}

func whisperBinary() string {
	for _, b := range whisperBins {
		if p, err := exec.LookPath(b); err == nil {
			return p
		}
	}
	return ""
}

var modelMu sync.Mutex

// whisperModel returns the local ggml model, downloading it once from the whisper.cpp release on Hugging Face.
func whisperModel(ctx context.Context, name string) (string, error) {
	if name == "" {
		name = "base"
	}
	if strings.ContainsAny(name, "/\\.") {
		return "", fmt.Errorf("bad model name %q", name)
	}
	dir := filepath.Join(config.Home(), "models")
	path := filepath.Join(dir, "ggml-"+name+".bin")
	modelMu.Lock()
	defer modelMu.Unlock()
	if st, err := os.Stat(path); err == nil && st.Size() > 1<<20 {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	url := "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-" + name + ".bin"
	dctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(dctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download whisper model %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("download whisper model %s: HTTP %d (known models: tiny, base, small, medium, large-v3-turbo)", name, resp.StatusCode)
	}
	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", fmt.Errorf("download whisper model: %w", err)
	}
	f.Close()
	return path, os.Rename(tmp, path)
}

func transcribeWhisper(ctx context.Context, cfg config.TranscribeConfig, audio string) (string, error) {
	bin := whisperBinary()
	if bin == "" {
		return "", api.Errf(api.CodeUnsupported, "whisper.cpp isn't installed").WithHint("brew install whisper-cpp, or set [transcribe] engine = \"api\"")
	}
	if !have("ffmpeg") {
		return "", api.Errf(api.CodeUnsupported, "transcription needs ffmpeg to decode voice notes").WithHint("brew install ffmpeg")
	}
	model, err := whisperModel(ctx, cfg.Model)
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "wassup-stt-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	wav := filepath.Join(dir, "in.wav")
	if _, err := run(ctx, "ffmpeg", "-y", "-loglevel", "error", "-i", audio, "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", wav); err != nil {
		return "", err
	}
	lang := cfg.Language
	if lang == "" {
		lang = "auto"
	}
	base := filepath.Join(dir, "out")
	if _, err := run(ctx, bin, "-m", model, "-f", wav, "-l", lang, "-nt", "-np", "-otxt", "-of", base); err != nil {
		return "", err
	}
	b, err := os.ReadFile(base + ".txt")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func transcribeAPI(ctx context.Context, cfg config.TranscribeConfig, audio string) (string, error) {
	key := os.Getenv(cfg.APIKeyEnv)
	if key == "" {
		return "", api.Errf(api.CodeUnsupported, "no API key in $%s", cfg.APIKeyEnv).WithHint("export it in the daemon's environment, or install whisper.cpp for local transcription")
	}
	f, err := os.Open(audio)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, _ := w.CreateFormFile("file", filepath.Base(audio))
	if _, err := io.Copy(part, f); err != nil {
		return "", err
	}
	_ = w.WriteField("model", cfg.APIModel)
	if cfg.Language != "" && cfg.Language != "auto" {
		_ = w.WriteField("language", cfg.Language)
	}
	w.Close()
	actx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(actx, http.MethodPost, cfg.APIURL, &body)
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return "", api.Errf(api.CodeUnsupported, "transcription API returned %d: %s", resp.StatusCode, trunc(string(data), 200))
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.Text), nil
}

// transcribe returns (and caches) the text of a voice or audio message.
func (e *Engine) transcribe(ctx context.Context, sm *store.Message) (*api.Transcript, error) {
	if sm.Type != "voice" && sm.Type != "audio" && sm.Type != "video" && sm.Type != "video_note" {
		return nil, usage("message %s is a %s, not audio", sm.ID, sm.Type)
	}
	if t, eng, ok := e.DB.Transcript(ctx, sm.Chat, sm.ID); ok {
		return &api.Transcript{ID: sm.ID, Chat: sm.Chat, Text: t, Engine: eng, Cached: true}, nil
	}
	cfg := e.Config().Transcribe
	engine := strings.ToLower(cfg.Engine)
	switch engine {
	case "off":
		return nil, api.Errf(api.CodeUnsupported, "transcription is off").WithHint("set [transcribe] engine in: wassup config edit")
	case "", "auto":
		switch {
		case whisperBinary() != "":
			engine = "whisper.cpp"
		case os.Getenv(cfg.APIKeyEnv) != "":
			engine = "api"
		default:
			return nil, api.Errf(api.CodeUnsupported, "no transcription engine available").
				WithHint("brew install whisper-cpp (local and private), or set $" + cfg.APIKeyEnv + " for an OpenAI-compatible API")
		}
	}
	if sm.MediaPath == "" || !fileExists(sm.MediaPath) {
		if err := e.WaitConnected(ctx); err != nil {
			return nil, err
		}
	}
	dl, err := e.downloadMessage(ctx, sm, "")
	if err != nil {
		return nil, err
	}
	var text string
	switch engine {
	case "whisper.cpp", "whisper", "local":
		engine = "whisper.cpp/" + cfg.Model
		text, err = transcribeWhisper(ctx, cfg, dl.Path)
	case "api", "openai":
		engine = "api/" + cfg.APIModel
		text, err = transcribeAPI(ctx, cfg, dl.Path)
	default:
		return nil, usage("unknown [transcribe] engine %q", cfg.Engine)
	}
	if err != nil {
		return nil, err
	}
	_ = e.DB.PutTranscript(ctx, sm.Chat, sm.ID, text, engine, cfg.Language)
	return &api.Transcript{ID: sm.ID, Chat: sm.Chat, Text: text, Engine: engine}, nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func mTranscribe(c *Call) (any, error) {
	var p api.MessageRef
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	sm, err := c.E.findMessage(c.Ctx, p.Chat, p.ID)
	if err != nil {
		return nil, err
	}
	return c.E.transcribe(c.Ctx, sm)
}

func (e *Engine) autoTranscribe(sm store.Message) {
	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Minute)
	defer cancel()
	t, err := e.transcribe(ctx, &sm)
	if err != nil {
		e.Log.Debug().Err(err).Str("id", sm.ID).Msg("auto-transcribe failed")
		return
	}
	e.publish(api.Event{Type: "message.transcribed", Chat: sm.Chat, Data: map[string]any{"id": sm.ID, "text": t.Text}})
}
