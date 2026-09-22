# movie-info

Show movie info in the terminal, including the cover image (kitty or sixel
graphics), using [TMDB](https://www.themoviedb.org/).

## Install

```sh
go install github.com/paranoidi/movie-info@latest
```

## Usage

```sh
export TMDB_API_KEY=your_tmdb_api_key

movie-info "Example Movie"   # search by title
movie-info tt1234567         # or by IMDb id
movie-info /path/to/movie/   # or a movie directory
```

Get an API key from TMDB: Settings → API.

### Flags

```
-show-title        show the movie title/year (hidden by default, e.g. for guessing games)
-wrap int          wrap text output to N characters (0 = no wrap) (default 79)
-short             print a single line: <runtime>\t<genres>\t<score> (no poster)
-probe             append ffprobe media details for a movie directory
-subtitles string  only report these subtitle languages, e.g. EN,FI (empty = all)
-image-protocol    image protocol: auto, kitty, sixel, none (default "auto")
-debug             print which image protocol was chosen and why
-init              create a default config file at $XDG_CONFIG_HOME/movie-info/config.json and exit
```

Auto-detection reads env vars (`TERM_PROGRAM`, `KITTY_WINDOW_ID`) that tmux
doesn't forward into the pane's environment. Inside tmux, if those are unset,
it falls back to `tmux show-environment -g`, which reflects the outer
terminal that started the tmux server — this can still be wrong (e.g. a tmux
server started years ago from a different terminal, or one attached from a
non-kitty-capable client). Use `-image-protocol=kitty` to force it either way.

### Config file

Instead of `TMDB_API_KEY`/flags, settings can be persisted in
`$XDG_CONFIG_HOME/movie-info/config.json` (falls back to
`~/.config/movie-info/config.json` on Linux if `XDG_CONFIG_HOME` is unset).
Run `movie-info -init` to create one to edit:

```json
{
  "api_key": "",
  "show_title": false,
  "probe": false,
  "subtitles": [],
  "wrap": 79,
  "image_protocol": "auto"
}
```

All fields are optional. Precedence: CLI flags > `TMDB_API_KEY` env var >
config file > built-in defaults. `-init` refuses to overwrite an existing
config file.

### Directory argument

When given a directory, movie-info resolves the movie in this order:

1. `.movie-info.json` in the directory, if present and current — used as-is,
   no network call, no API key needed.
2. A `*.nfo` file in the directory — the first IMDb id found in it (plain
   `tt1234567` or an imdb.com URL) is used to look up the movie.
3. Otherwise, the directory name is parsed as a title: dots/underscores
   become spaces, and everything from the first 4-digit year onward is
   dropped (`Example.Movie.1999.1080p.x264` → `Example Movie`).

After a successful TMDB fetch for a directory, the result (including the
poster, base64-encoded) is cached to `.movie-info.json` next to it for
future runs.

## Output

Poster image, then:

- Title / year (hidden by default)
- Runtime
- TMDB user score
- Genres
- Director
- Top billed actors

### Media details (`-probe`)

With `-probe` (or `"probe": true` in the config file) and a directory argument,
the largest video file in it is probed with `ffprobe` and the result is printed
below the movie info, separated by a blank line:

```
Media / 1.1 GB / 2:17:13 / 1.10 Mbps
Video: h264 / 1280×470 / 23.98 fps / yuv420p / DAR 256:109
Subtitles: NL, EN
Audio:
- EN / aac / 2 ch
```

Sidecar subtitle files beside the movie (and in a `Subs/` or `Subtitles/`
subdirectory) are reported on their own `Subtitles (ext):` line with their
format — `EN (srt), FI×2 (ass)` — covering srt, ass, ssa, vtt, sub, sup, smi,
ttml and dfxp. The language comes from the filename (`Example.Movie.en.srt`,
`Subs/2_English.srt`).

Subtitle languages are listed once each, with `×n` when several tracks share a
language; audio tracks get one row each, with the track title appended after
`—` when the file has one. Tracks with no language tag show `UND`. Needs
`ffprobe` (from ffmpeg) on `PATH` — without it, or without a video file in the
directory, a warning goes to stderr and the movie info prints as usual.
`-short` is unaffected: it stays a single line.

`-subtitles EN,FI` (or `"subtitles": ["EN", "FI"]` in the config file) narrows
both subtitle lines to those languages, leaving the audio tracks alone. Codes
are matched however they're written — `fi`, `FIN` and `FI` all select the same
track, and `UND` selects untagged ones. A line is left out entirely when no
track matches.

## Build

```sh
task build   # or: go build -o movie-info .
```

See `taskfile.yml` for lint/test/vuln-check tasks.
