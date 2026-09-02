package main

import "testing"

func TestMovieNameFromDirName(t *testing.T) {
	cases := map[string]string{
		"The.Matrix.1999.foo.bar.asdf": "The Matrix",
		"Some_Movie_Name_2020_1080p":   "Some Movie Name",
		"No.Year.Here":                 "No Year Here",
		"Alien 1979":                   "Alien",
	}
	for in, want := range cases {
		if got := movieNameFromDirName(in); got != want {
			t.Errorf("movieNameFromDirName(%q) = %q, want %q", in, got, want)
		}
	}
}
