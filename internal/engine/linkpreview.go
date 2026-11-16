package engine

import (
	"bytes"
	"context"
	"image"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"
)

var urlRe = regexp.MustCompile(`https?://[^\s<>"'` + "`" + `]+`)

type linkMeta struct {
	URL, Title, Description string
	Thumb                   []byte
}

var previewClient = &http.Client{Timeout: 6 * time.Second}

// firstURL returns the first http(s) URL in text, minus trailing punctuation.
func firstURL(text string) string {
	u := urlRe.FindString(text)
	return strings.TrimRight(u, ".,!?;:)]}'\"")
}

// fetchLinkPreview reads Open Graph tags. Any failure just means no preview.
func fetchLinkPreview(ctx context.Context, raw string) *linkMeta {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	// Sites serve their share cards to the WhatsApp crawler user agent.
	req.Header.Set("User-Agent", "WhatsApp/2.24.0 (wassup link preview)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := previewClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 || !strings.Contains(resp.Header.Get("Content-Type"), "html") {
		return nil
	}
	meta := &linkMeta{URL: raw}
	var imgURL, docTitle string
	z := html.NewTokenizer(io.LimitReader(resp.Body, 1<<20))
	inTitle := false
loop:
	for {
		switch z.Next() {
		case html.ErrorToken:
			break loop
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			tag := string(name)
			if tag == "body" {
				break loop
			}
			if tag == "title" {
				inTitle = true
				continue
			}
			if tag != "meta" || !hasAttr {
				continue
			}
			var key, content string
			for {
				k, v, more := z.TagAttr()
				switch strings.ToLower(string(k)) {
				case "property", "name":
					key = strings.ToLower(string(v))
				case "content":
					content = string(v)
				}
				if !more {
					break
				}
			}
			switch key {
			case "og:title", "twitter:title":
				if meta.Title == "" {
					meta.Title = content
				}
			case "og:description", "twitter:description", "description":
				if meta.Description == "" {
					meta.Description = content
				}
			case "og:image", "og:image:url", "twitter:image":
				if imgURL == "" {
					imgURL = content
				}
			}
		case html.TextToken:
			if inTitle && docTitle == "" {
				docTitle = strings.TrimSpace(string(z.Text()))
			}
		case html.EndTagToken:
			inTitle = false
		}
	}
	if meta.Title == "" {
		meta.Title = docTitle
	}
	if meta.Title == "" {
		return nil
	}
	if imgURL != "" {
		if iu, err := u.Parse(imgURL); err == nil {
			meta.Thumb = fetchThumb(ctx, iu.String())
		}
	}
	return meta
}

func fetchThumb(ctx context.Context, raw string) []byte {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	req.Header.Set("User-Agent", "WhatsApp/2.24.0 (wassup link preview)")
	resp, err := previewClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	return jpegBytes(resizeFit(img, 160), 70)
}
