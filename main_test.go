package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMovieNameFromDirName(t *testing.T) {
	cases := map[string]struct{ title, year string }{
		"Example.Movie.1999.foo.bar.asdf": {"Example Movie", "1999"},
		"Some_Movie_Name_2020_1080p":      {"Some Movie Name", "2020"},
		"No.Year.Here":                    {"No Year Here", ""},
		"Beacon 1979":                     {"Beacon", "1979"},
		"Example.Movie.REMUX.foo":         {"Example Movie", ""},
		"Example.Movie.remastered.foo":    {"Example Movie", ""},
		"Example.Movie.DTS-HD.foo":        {"Example Movie", ""},
		"Example.Movie.Bluray.foo":        {"Example Movie", ""},
		"1234.2019.1080p.foo":             {"1234", "2019"},
		"Example.Movie.UNRATED.foo":       {"Example Movie", ""},
		"Example.Movie.PROPER.foo":        {"Example Movie", ""},
		"Quarry.2018.Foo":                 {"Quarry", "2018"},
		"Quarry.2023.Bar":                 {"Quarry", "2023"},
	}
	for in, want := range cases {
		if title, year := movieNameFromDirName(in); title != want.title || year != want.year {
			t.Errorf("movieNameFromDirName(%q) = (%q, %q), want (%q, %q)", in, title, year, want.title, want.year)
		}
	}
}

func TestLoadCacheStaleVersionStillReturnsData(t *testing.T) {
	dir := t.TempDir()
	old := movieCache{Version: cacheVersion - 1, PosterBase64: "aGVsbG8="}
	data, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, cacheFileName), data, 0o644); err != nil {
		t.Fatal(err)
	}

	cache, ok := loadCache(dir)
	if ok {
		t.Fatal("loadCache: ok = true for stale version, want false")
	}
	if cache == nil {
		t.Fatal("loadCache: cache = nil for stale version, want salvageable data")
	}
	if string(cache.posterBytes()) != "hello" {
		t.Errorf("posterBytes() = %q, want %q", cache.posterBytes(), "hello")
	}
}

func TestThrottleAt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "last-request")
	throttleAt(path) // no file yet: returns immediately
	start := time.Now()
	throttleAt(path)
	if d := time.Since(start); d < 600*time.Millisecond || d > 1300*time.Millisecond {
		t.Fatalf("second call waited %v, want 600-1200ms", d)
	}
}
