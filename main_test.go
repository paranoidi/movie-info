package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMovieNameFromDirName(t *testing.T) {
	cases := map[string]string{
		"The.Matrix.1999.foo.bar.asdf": "The Matrix",
		"Some_Movie_Name_2020_1080p":   "Some Movie Name",
		"No.Year.Here":                 "No Year Here",
		"Alien 1979":                   "Alien",
		"The.Matrix.REMUX.foo":         "The Matrix",
		"The.Matrix.remastered.foo":    "The Matrix",
		"The.Matrix.DTS-HD.foo":        "The Matrix",
		"The.Matrix.Bluray.foo":        "The Matrix",
		"1917.2019.1080p.foo":          "1917",
	}
	for in, want := range cases {
		if got := movieNameFromDirName(in); got != want {
			t.Errorf("movieNameFromDirName(%q) = %q, want %q", in, got, want)
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
