package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/language"
)

// videoExts are the container extensions considered when picking the movie file
// to probe inside a directory.
var videoExts = map[string]bool{
	".mkv": true, ".mp4": true, ".m4v": true, ".webm": true, ".avi": true,
	".mov": true, ".mpg": true, ".mpeg": true, ".ts": true, ".ogv": true,
}

// probeDoc is the subset of `ffprobe -show_format -show_streams -of json` we render.
type probeDoc struct {
	Streams []probeStream `json:"streams"`
	Format  probeFormat   `json:"format"`
}

type probeStream struct {
	CodecType          string `json:"codec_type"`
	CodecName          string `json:"codec_name"`
	Width              int    `json:"width"`
	Height             int    `json:"height"`
	DisplayAspectRatio string `json:"display_aspect_ratio"`
	BitRate            string `json:"bit_rate"`
	RFrameRate         string `json:"r_frame_rate"`
	PixFmt             string `json:"pix_fmt"`
	Channels           int    `json:"channels"`
	Disposition        *struct {
		AttachedPic int `json:"attached_pic"`
	} `json:"disposition"`
	Tags struct {
		Language string `json:"language"`
		Title    string `json:"title"`
	} `json:"tags"`
}

type probeFormat struct {
	Filename string `json:"filename"`
	Duration string `json:"duration"`
	Size     string `json:"size"`
	BitRate  string `json:"bit_rate"`
}

// probeText runs ffprobe on the largest video file in dir and returns the
// formatted metadata block (no trailing newline). langs narrows the subtitle
// listings; empty reports every language.
func probeText(dir string, langs []string) (string, error) {
	path, err := largestVideoFile(dir)
	if err != nil {
		return "", err
	}
	out, err := exec.Command("ffprobe", "-show_format", "-show_streams", "-of", "json", path).Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", fmt.Errorf("ffprobe not found (install ffmpeg)")
		}
		return "", fmt.Errorf("ffprobe %s: %w", filepath.Base(path), err)
	}
	var doc probeDoc
	if err := json.Unmarshal(out, &doc); err != nil {
		return "", fmt.Errorf("ffprobe: %w", err)
	}
	return formatProbe(doc, langs), nil
}

// largestVideoFile returns the biggest video file directly inside dir — the
// feature, rather than a sample or an extra.
func largestVideoFile(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	best, bestSize := "", int64(-1)
	for _, e := range entries {
		if e.IsDir() || !videoExts[strings.ToLower(filepath.Ext(e.Name()))] {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if fi.Size() > bestSize {
			best, bestSize = filepath.Join(dir, e.Name()), fi.Size()
		}
	}
	if best == "" {
		return "", fmt.Errorf("no video file found in %s", dir)
	}
	return best, nil
}

// formatProbe renders container, video, subtitle and audio track lines. langs
// narrows the two subtitle lines to those languages; empty reports every one.
func formatProbe(doc probeDoc, langs []string) string {
	var b strings.Builder
	size := parseInt64(doc.Format.Size)
	if size == 0 {
		if st, err := os.Stat(doc.Format.Filename); err == nil {
			size = st.Size()
		}
	}
	fmt.Fprintf(&b, "Media / %s", formatBytes(size))
	if dur := parseFloat(doc.Format.Duration); dur > 0 {
		fmt.Fprintf(&b, " / %s", formatClockDuration(dur))
	}
	if br := parseInt64(doc.Format.BitRate); br > 0 {
		fmt.Fprintf(&b, " / %s", formatBitRate(br))
	}
	b.WriteByte('\n')

	for _, s := range doc.Streams {
		if s.CodecType != "video" || s.Width <= 0 {
			continue
		}
		if s.Disposition != nil && s.Disposition.AttachedPic != 0 {
			continue
		}
		fmt.Fprintf(&b, "Video: %s / %d×%d", nonEmpty(s.CodecName, "unknown"), s.Width, s.Height)
		if fps := parseFrameRate(s.RFrameRate); fps > 0 {
			fmt.Fprintf(&b, " / %.2f fps", fps)
		}
		if s.PixFmt != "" {
			fmt.Fprintf(&b, " / %s", s.PixFmt)
		}
		if s.DisplayAspectRatio != "" && s.DisplayAspectRatio != "0:1" {
			fmt.Fprintf(&b, " / DAR %s", s.DisplayAspectRatio)
		}
		b.WriteByte('\n')
	}

	filter := subtitleFilter(langs)
	if subs := formatSubtitleLangs(doc, filter); subs != "" {
		fmt.Fprintf(&b, "Subtitles: %s\n", subs)
	}
	if ext := externalSubs(doc.Format.Filename, filter); ext != "" {
		fmt.Fprintf(&b, "Subtitles (ext): %s\n", ext)
	}

	audioHeader := false
	for _, s := range doc.Streams {
		if s.CodecType != "audio" {
			continue
		}
		if !audioHeader {
			b.WriteString("Audio:\n")
			audioHeader = true
		}
		fmt.Fprintf(&b, "- %s / %s", langCode(s.Tags.Language), nonEmpty(s.CodecName, "unknown"))
		if s.Channels > 0 {
			fmt.Fprintf(&b, " / %d ch", s.Channels)
		}
		if br := parseInt64(s.BitRate); br > 0 {
			fmt.Fprintf(&b, " / %s", formatBitRate(br))
		}
		if title := strings.TrimSpace(s.Tags.Title); title != "" {
			fmt.Fprintf(&b, " — %s", title)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatSubtitleLangs lists subtitle track languages in first-seen order,
// collapsing repeats into "EN×2", with "(N more)" when filter hid some. Empty
// only when the file has no subtitle tracks at all.
func formatSubtitleLangs(doc probeDoc, filter map[string]bool) string {
	var order []string
	counts := map[string]int{}
	hidden := 0
	for _, s := range doc.Streams {
		if s.CodecType != "subtitle" {
			continue
		}
		code := langCode(s.Tags.Language)
		if filter != nil && !filter[code] {
			hidden++
			continue
		}
		if counts[code] == 0 {
			order = append(order, code)
		}
		counts[code]++
	}
	parts := make([]string, 0, len(order))
	for _, code := range order {
		if n := counts[code]; n > 1 {
			code = fmt.Sprintf("%s×%d", code, n)
		}
		parts = append(parts, code)
	}
	return withMore(parts, hidden)
}

// withMore joins a filtered listing, flagging with "(N more)" how many tracks the
// filter hid — so a narrowed line is never mistaken for the whole picture. A line
// whose languages were all hidden still reports "(N more)" rather than vanishing,
// which would read as "no subtitles" instead of "none in your languages".
func withMore(parts []string, hidden int) string {
	joined := strings.Join(parts, ", ")
	more := fmt.Sprintf("(%d more)", hidden)
	switch {
	case hidden == 0:
		return joined
	case joined == "":
		return more
	default:
		return joined + " " + more
	}
}

// subtitleFilter turns user-written language codes into the canonical uppercase
// set the subtitle listings use, so "fi", "FIN" and "Fi" all match an FI track.
// nil (no usable codes) means "report every language".
func subtitleFilter(langs []string) map[string]bool {
	set := make(map[string]bool, len(langs))
	for _, l := range langs {
		if l = strings.TrimSpace(l); l != "" {
			set[langCode(l)] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

// langCode maps an ffprobe language tag to an uppercase two-letter code ("dut" → "NL",
// "en-US" → "EN"). Untagged or undetermined tracks report "UND"; anything x/text cannot
// resolve is uppercased as-is.
func langCode(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	// language.Parse("und") resolves to base "en" at low confidence, so guard it first.
	if raw == "" || raw == "und" {
		return "UND"
	}
	tag, err := language.Parse(raw)
	if err != nil {
		return strings.ToUpper(raw)
	}
	base, conf := tag.Base()
	if conf == language.No {
		return strings.ToUpper(raw)
	}
	return strings.ToUpper(base.String())
}

// subtitleExts are the sidecar subtitle formats reported next to a video file.
var subtitleExts = map[string]bool{
	".srt": true, ".ass": true, ".ssa": true, ".vtt": true, ".sub": true,
	".sup": true, ".smi": true, ".ttml": true, ".dfxp": true,
}

// subtitleJunkTokens are filename tokens that look like a language code but never are
// (".forced", ".sdh", ".sub"). "und" is here too: language.Parse resolves it to English.
var subtitleJunkTokens = map[string]bool{
	"forced": true, "sdh": true, "cc": true, "sub": true, "subs": true,
	"subtitle": true, "subtitles": true, "default": true, "full": true, "und": true,
}

// subKey is one external-subtitle bucket: a language code and a file format.
type subKey struct{ lang, ext string }

// externalSubs lists sidecar subtitle files for videoPath — beside it, and in a
// Subs/ or Subtitles/ subdirectory — as `EN (srt), FI×2 (ass)`. filter narrows
// them to those languages, appending "(N more)"; nil keeps all. Empty only when
// there are no sidecar files at all.
func externalSubs(videoPath string, filter map[string]bool) string {
	if videoPath == "" {
		return ""
	}
	dir := filepath.Dir(videoPath)
	stem := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))

	var order []subKey
	counts := map[subKey]int{}
	hidden := 0
	scan := func(d string, descend bool) []string {
		var subDirs []string
		entries, err := os.ReadDir(d)
		if err != nil {
			return nil
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() {
				if lower := strings.ToLower(name); descend && (lower == "subs" || lower == "subtitles") {
					subDirs = append(subDirs, filepath.Join(d, name))
				}
				continue
			}
			ext := strings.ToLower(filepath.Ext(name))
			if !subtitleExts[ext] {
				continue
			}
			lang := subtitleFileLang(name, stem)
			if filter != nil && !filter[lang] {
				hidden++
				continue
			}
			k := subKey{lang: lang, ext: strings.TrimPrefix(ext, ".")}
			if counts[k] == 0 {
				order = append(order, k)
			}
			counts[k]++
		}
		return subDirs
	}
	for _, d := range scan(dir, true) {
		scan(d, false)
	}

	parts := make([]string, 0, len(order))
	for _, k := range order {
		lang := k.lang
		if n := counts[k]; n > 1 {
			lang = fmt.Sprintf("%s×%d", lang, n)
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", lang, k.ext))
	}
	return withMore(parts, hidden)
}

// subtitleFileLang derives a language code from a subtitle filename, e.g.
// "Movie.2015.en.forced.srt" (video stem "Movie.2015") → "EN", "Subs/2_English.srt" →
// "EN". Tokens are read right to left; "UND" when none of them names a language.
func subtitleFileLang(name, videoStem string) string {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	if videoStem != "" && len(base) > len(videoStem) && strings.EqualFold(base[:len(videoStem)], videoStem) {
		base = base[len(videoStem):]
	}
	tokens := strings.FieldsFunc(base, func(r rune) bool {
		return strings.ContainsRune("._- []()", r)
	})
	for i := len(tokens) - 1; i >= 0; i-- {
		if code, ok := subtitleLangToken(strings.ToLower(tokens[i])); ok {
			return code
		}
	}
	// Multi-word display names ("brazilian portuguese") survive tokenization only as a whole.
	if code, ok := subtitleLangNames[strings.ToLower(strings.Join(tokens, " "))]; ok {
		return code
	}
	return "UND"
}

// subtitleLangToken resolves one filename token to an uppercase language code,
// accepting both codes ("en", "eng", "dut") and English names ("english").
func subtitleLangToken(tok string) (string, bool) {
	if subtitleJunkTokens[tok] {
		return "", false
	}
	if len(tok) == 2 || len(tok) == 3 {
		if tag, err := language.Parse(tok); err == nil {
			if base, conf := tag.Base(); conf != language.No {
				return strings.ToUpper(base.String()), true
			}
		}
	}
	code, ok := subtitleLangNames[tok]
	return code, ok
}

// subtitleLangNames maps English language names as they appear in subtitle
// filenames ("Subs/2_English.srt") to their code. Codes themselves are resolved by
// x/text; this covers only the names common in subtitle releases.
var subtitleLangNames = map[string]string{
	"english": "EN", "spanish": "ES", "french": "FR", "german": "DE",
	"italian": "IT", "portuguese": "PT", "brazilian portuguese": "PT",
	"dutch": "NL", "danish": "DA", "swedish": "SV", "norwegian": "NO",
	"finnish": "FI", "icelandic": "IS", "polish": "PL", "czech": "CS",
	"slovak": "SK", "hungarian": "HU", "romanian": "RO", "bulgarian": "BG",
	"greek": "EL", "russian": "RU", "ukrainian": "UK", "turkish": "TR",
	"arabic": "AR", "hebrew": "HE", "hindi": "HI", "chinese": "ZH",
	"simplified chinese": "ZH", "traditional chinese": "ZH", "japanese": "JA",
	"korean": "KO", "thai": "TH", "vietnamese": "VI", "indonesian": "ID",
	"malay": "MS", "croatian": "HR", "serbian": "SR", "slovenian": "SL",
	"estonian": "ET", "latvian": "LV", "lithuanian": "LT", "persian": "FA",
	"farsi": "FA",
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func formatBytes(n int64) string {
	const (
		kb = 1000
		mb = 1000 * kb
		gb = 1000 * mb
	)
	switch {
	case n < kb:
		return fmt.Sprintf("%d B", n)
	case n < mb:
		return fmt.Sprintf("%.1f KB", float64(n)/kb)
	case n < gb:
		return fmt.Sprintf("%.1f MB", float64(n)/mb)
	default:
		return fmt.Sprintf("%.1f GB", float64(n)/gb)
	}
}

func formatClockDuration(sec float64) string {
	d := time.Duration(sec * float64(time.Second))
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

func formatBitRate(bps int64) string {
	if bps < 1000 {
		return fmt.Sprintf("%d bps", bps)
	}
	if bps < 1_000_000 {
		return fmt.Sprintf("%.0f kbps", float64(bps)/1000)
	}
	return fmt.Sprintf("%.2f Mbps", float64(bps)/1_000_000)
}

func parseFrameRate(s string) float64 {
	num, den, ok := strings.Cut(strings.TrimSpace(s), "/")
	if !ok {
		return parseFloat(s)
	}
	n, d := parseFloat(num), parseFloat(den)
	if d == 0 {
		return 0
	}
	return n / d
}

func parseFloat(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f < 0 {
		return 0
	}
	return f
}

func parseInt64(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return n
}
