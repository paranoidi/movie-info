package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormatProbe(t *testing.T) {
	audio := func(codec, lang, title string, channels int, bitRate string) probeStream {
		s := probeStream{CodecType: "audio", CodecName: codec, Channels: channels, BitRate: bitRate}
		s.Tags.Language = lang
		s.Tags.Title = title
		return s
	}
	subtitle := func(lang string) probeStream {
		s := probeStream{CodecType: "subtitle", CodecName: "subrip"}
		s.Tags.Language = lang
		return s
	}
	doc := probeDoc{
		Format: probeFormat{Duration: "125.5", Size: "1048576", BitRate: "128000"},
		Streams: []probeStream{
			{CodecType: "video", CodecName: "h264", Width: 1920, Height: 1080, RFrameRate: "30/1", PixFmt: "yuv420p"},
			subtitle("dut"),
			audio("truehd", "eng", "TrueHD Atmos 7.1", 8, "640000"),
			subtitle("en-US"),
			audio("aac", "", "", 2, "160000"),
			subtitle("en-US"),
		},
	}
	got := formatProbe(doc, nil)
	for _, want := range []string{
		"Media / 1.0 MB / 2:05 / 128 kbps",
		"Video: h264 / 1920×1080 / 30.00 fps / yuv420p",
		"Subtitles: NL, EN×2",
		"Audio:\n- EN / truehd / 8 ch / 640 kbps — TrueHD Atmos 7.1\n- UND / aac / 2 ch / 160 kbps",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("probe text missing %q:\n%s", want, got)
		}
	}

	// A file with neither subtitles nor audio gets neither block.
	got = formatProbe(probeDoc{Streams: doc.Streams[:1]}, nil)
	if strings.Contains(got, "Subtitles:") || strings.Contains(got, "Audio:") {
		t.Fatalf("video-only probe text has track blocks:\n%s", got)
	}

	// A language filter narrows the subtitle line; lowercase input still matches,
	// and audio is left alone.
	got = formatProbe(doc, []string{"en"})
	if !strings.Contains(got, "Subtitles: EN×2") || strings.Contains(got, "NL") {
		t.Errorf("filtered probe text = want only EN subtitles:\n%s", got)
	}
	if !strings.Contains(got, "- UND / aac") {
		t.Errorf("filtered probe text dropped an audio track:\n%s", got)
	}

	// A filter nothing matches drops the line rather than printing it empty.
	if got := formatProbe(doc, []string{"ZZ"}); strings.Contains(got, "Subtitles:") {
		t.Errorf("unmatched filter still printed a subtitle line:\n%s", got)
	}
}

func TestExternalSubs(t *testing.T) {
	dir := t.TempDir()
	write := func(rel string) {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	video := filepath.Join(dir, "Example.Movie.2015.1080p.mkv")
	write("Example.Movie.2015.1080p.mkv")
	write("Example.Movie.2015.1080p.en.srt")
	write("Example.Movie.2015.1080p.en.forced.srt")
	write("Example.Movie.2015.1080p.fi.ass")
	write("Example.Movie.2015.1080p.srt")    // no language token
	write("Example.Movie.2015.1080p.nfo")    // not a subtitle
	write("Subs/3_Brazilian Portuguese.srt") // nested, multi-word name
	write("Subs/2_Russian.sub")

	got := externalSubs(video, nil)
	for _, want := range []string{"EN×2 (srt)", "FI (ass)", "UND (srt)", "PT (srt)", "RU (sub)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("externalSubs = %q, want it to contain %q", got, want)
		}
	}

	if got := externalSubs(filepath.Join(t.TempDir(), "clip.mkv"), nil); got != "" {
		t.Fatalf("externalSubs with no sidecars = %q, want \"\"", got)
	}
	if got := externalSubs("", nil); got != "" {
		t.Fatalf("externalSubs(\"\") = %q, want \"\"", got)
	}

	if got := externalSubs(video, subtitleFilter([]string{"fin"})); got != "FI (ass)" {
		t.Errorf("externalSubs filtered to FI = %q, want %q", got, "FI (ass)")
	}
}

func TestSubtitleFileLang(t *testing.T) {
	stem := "Example.Movie.2015.1080p"
	cases := map[string]string{
		"Example.Movie.2015.1080p.en.srt":        "EN",
		"Example.Movie.2015.1080p.eng.sdh.srt":   "EN",
		"Example.Movie.2015.1080p.dut.forced.on": "NL",
		"Example.Movie.2015.1080p.srt":           "UND",
		"Example.Movie.2015.1080p.und.srt":       "UND",
		"2_English.srt":                          "EN",
		"Finnish.ass":                            "FI",
		"3_Brazilian Portuguese.srt":             "PT",
		"Norwegian.srt":                          "NO",
		"whatever.srt":                           "UND",
	}
	for name, want := range cases {
		if got := subtitleFileLang(name, stem); got != want {
			t.Errorf("subtitleFileLang(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestLangCode(t *testing.T) {
	cases := map[string]string{
		"eng":   "EN",
		"dut":   "NL", // ISO 639-2/B bibliographic code
		"fin":   "FI",
		"en-US": "EN",
		"und":   "UND",
		"":      "UND",
		"  ":    "UND",
		"zxx":   "ZXX",
		"nope":  "NOPE",
	}
	for raw, want := range cases {
		if got := langCode(raw); got != want {
			t.Errorf("langCode(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestLargestVideoFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, size int) {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("sample.mkv", 10)
	write("feature.MKV", 100)
	write("poster.jpg", 1000)

	got, err := largestVideoFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "feature.MKV" {
		t.Errorf("largestVideoFile = %q, want feature.MKV", got)
	}

	if _, err := largestVideoFile(t.TempDir()); err == nil {
		t.Error("largestVideoFile: want error for a directory with no video file")
	}
}
