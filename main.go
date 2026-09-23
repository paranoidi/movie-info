package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color/palette"
	"image/draw"
	_ "image/jpeg"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/BourgeoisBear/rasterm"
	tmdb "github.com/ryanbradynd05/go-tmdb"
)

const posterBaseURL = "https://image.tmdb.org/t/p/w500"
const cacheFileName = ".movie-info.json"
const cacheVersion = 2

var imdbIDRe = regexp.MustCompile(`^tt\d+$`)
var imdbIDInTextRe = regexp.MustCompile(`tt\d+`)
var yearRe = regexp.MustCompile(`\b(19|20)\d{2}\b`)
var cutoffTagRe = regexp.MustCompile(`(?i)\b(1080p|720p|2160p|limited|remux|remastered|hybrid|dts-hd|dts|webrip|blu-ray|bluray|unrated|proper)\b`)

// MovieInfo is the subset of movie data we display and cache.
type MovieInfo struct {
	Title    string   `json:"title"`
	Year     string   `json:"year"`
	Runtime  uint32   `json:"runtime"`
	Score    float32  `json:"score"`
	Genres   []string `json:"genres"`
	Director string   `json:"director"`
	Actors   []string `json:"actors"`
	Overview string   `json:"overview"`
}

// movieCache is the on-disk shape of .movie-info.json.
type movieCache struct {
	Version int `json:"version"`
	MovieInfo
	PosterBase64 string `json:"poster_base64,omitempty"`
}

// Config is the on-disk shape of $XDG_CONFIG_HOME/movie-info/config.json
// (~/.config/movie-info/config.json on Linux if XDG_CONFIG_HOME is unset).
// ShowTitle/Probe/Wrap are pointers so "absent" is distinguishable from "false"/"0".
type Config struct {
	APIKey        string   `json:"api_key,omitempty"`
	ShowTitle     *bool    `json:"show_title,omitempty"`
	Probe         *bool    `json:"probe,omitempty"`
	Subtitles     []string `json:"subtitles,omitempty"`
	Wrap          *int     `json:"wrap,omitempty"`
	ImageProtocol string   `json:"image_protocol,omitempty"`
}

// configPath returns $XDG_CONFIG_HOME/movie-info/config.json (or the platform
// equivalent from os.UserConfigDir).
func configPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "movie-info", "config.json"), nil
}

// loadConfig reads the user config file. A missing file is not an error;
// any other read/parse failure returns an error and an empty Config.
func loadConfig() (*Config, error) {
	path, err := configPath()
	if err != nil {
		return &Config{}, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Config{}, nil
	}
	if err != nil {
		return &Config{}, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return &Config{}, err
	}
	return &c, nil
}

const configTemplate = `{
  "api_key": "",
  "show_title": false,
  "probe": false,
  "subtitles": [],
  "wrap": 79,
  "image_protocol": "auto"
}
`

// initConfig creates a default config file for the user to edit. It refuses
// to overwrite an existing one.
func initConfig() error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("config file already exists: %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(configTemplate), 0o644); err != nil {
		return err
	}
	fmt.Println("created", path)
	return nil
}

func main() {
	showTitle := flag.Bool("show-title", false, "show the movie title/year (hidden by default, e.g. for guessing games)")
	wrap := flag.Int("wrap", 79, "wrap text output to N characters (0 = no wrap)")
	probe := flag.Bool("probe", false, "append ffprobe media details (video/subtitle/audio tracks) for a movie directory")
	subtitles := flag.String("subtitles", "", "only report these subtitle languages, e.g. EN,FI (empty = all)")
	short := flag.Bool("short", false, "print a single line: <runtime>\\t<genres>\\t<score>")
	score := flag.Bool("score", false, "print only the score, e.g. 7.3")
	runtime := flag.Bool("runtime", false, "print only the runtime in minutes, e.g. 148")
	genre := flag.Bool("genre", false, "print only the primary (first listed) genre, e.g. Drama")
	debug := flag.Bool("debug", false, "print which image protocol was chosen and why")
	imageProtocol := flag.String("image-protocol", "auto", "image protocol: auto, kitty, sixel, none (override auto-detection, e.g. wezterm/kitty behind tmux misdetect as unsupported)")
	initFlag := flag.Bool("init", false, "create a default config file at $XDG_CONFIG_HOME/movie-info/config.json and exit")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: movie-info [flags] <movie name | imdb id>")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *initFlag {
		if err := initConfig(); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}

	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(1)
	}

	// ponytail: first of -short/-score/-runtime/-genre wins, no mutual-exclusion error.
	oneLine := ""
	switch {
	case *short:
		oneLine = "short"
	case *score:
		oneLine = "score"
	case *runtime:
		oneLine = "runtime"
	case *genre:
		oneLine = "genre"
	}

	if err := run(flag.Args(), *showTitle, *wrap, oneLine, *debug, *imageProtocol, *probe, *subtitles); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// run resolves and renders the movie information for args. Any failure to do
// so — a TMDB error, a bad cache/config file, or an unexpected panic — comes
// back as a non-nil error so main can exit with status 1.
func run(args []string, showTitle bool, wrap int, oneLine string, debug bool, imageProtocol string, probe bool, subtitles string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()

	cfg, cfgErr := loadConfig()
	if cfgErr != nil {
		fmt.Fprintln(os.Stderr, "warning: failed to load config:", cfgErr)
	}

	setFlags := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { setFlags[f.Name] = true })

	effShowTitle := showTitle
	if !setFlags["show-title"] && cfg.ShowTitle != nil {
		effShowTitle = *cfg.ShowTitle
	}
	effProbe := probe
	if !setFlags["probe"] && cfg.Probe != nil {
		effProbe = *cfg.Probe
	}
	effSubtitles := strings.FieldsFunc(subtitles, func(r rune) bool { return r == ',' || r == ' ' })
	if !setFlags["subtitles"] && len(cfg.Subtitles) > 0 {
		effSubtitles = cfg.Subtitles
	}
	effWrap := wrap
	if !setFlags["wrap"] && cfg.Wrap != nil {
		effWrap = *cfg.Wrap
	}
	effImageProtocol := imageProtocol
	if !setFlags["image-protocol"] && cfg.ImageProtocol != "" {
		effImageProtocol = cfg.ImageProtocol
	}
	switch effImageProtocol {
	case "auto", "kitty", "sixel", "none":
	default:
		return fmt.Errorf("image protocol must be one of auto, kitty, sixel, none (got %q)", effImageProtocol)
	}

	dirPath := ""
	if len(args) == 1 {
		if st, statErr := os.Stat(args[0]); statErr == nil && st.IsDir() {
			dirPath = args[0]
		}
	}

	showCached := func(c *movieCache) {
		if oneLine != "" {
			printOneLine(c.MovieInfo, oneLine)
			return
		}
		renderPoster(c.posterBytes(), debug, effImageProtocol)
		printInfo(c.MovieInfo, !effShowTitle, effWrap)
		printProbe(dirPath, effProbe, effSubtitles)
	}

	var stalePoster []byte
	if dirPath != "" {
		if cache, ok := loadCache(dirPath); ok {
			showCached(cache)
			return nil
		} else if cache != nil {
			// Old cache schema forces a re-fetch of movie info, but the poster
			// image itself never changes, so reuse it instead of re-downloading.
			stalePoster = cache.posterBytes()
		}
	}

	apiKey := os.Getenv("TMDB_API_KEY")
	if apiKey == "" {
		apiKey = cfg.APIKey
	}
	if apiKey == "" {
		return fmt.Errorf("TMDB_API_KEY not set: export it, or set \"api_key\" in $XDG_CONFIG_HOME/movie-info/config.json")
	}
	api := tmdb.Init(tmdb.Config{APIKey: apiKey})

	query := strings.Join(args, " ")
	year := ""
	if dirPath != "" {
		if id, nfoErr := imdbIDFromDir(dirPath); nfoErr == nil {
			query = id
		} else {
			query, year = movieNameFromDirName(filepath.Base(dirPath))
		}
	}

	// Wait out the gap since the previous invocation's last API request, then
	// record this one's once all its requests are done.
	markRequested := throttle()
	// Another process launched for the same directory may have written the
	// cache while we slept.
	if dirPath != "" {
		if cache, ok := loadCache(dirPath); ok {
			showCached(cache)
			return nil
		}
	}
	defer markRequested()

	movieID, err := resolveMovieID(api, query, year)
	if err != nil {
		return err
	}

	movie, err := api.GetMovieInfo(movieID, map[string]string{"append_to_response": "credits"})
	if err != nil {
		return err
	}

	posterData := stalePoster
	if len(posterData) == 0 {
		posterData, _ = fetchPosterBytes(movie.PosterPath)
	}
	info := movieInfoFrom(movie)
	if oneLine != "" {
		printOneLine(info, oneLine)
	} else {
		renderPoster(posterData, debug, effImageProtocol)
		printInfo(info, !effShowTitle, effWrap)
		printProbe(dirPath, effProbe, effSubtitles)
	}

	if dirPath != "" {
		if err := saveCache(dirPath, info, posterData); err != nil {
			fmt.Fprintln(os.Stderr, "warning: failed to save cache:", err)
		}
	}

	return nil
}

// loadCache reads dir/.movie-info.json; ok is false if it's missing, unparsable,
// or older than the schema this build writes (forcing a fresh fetch). When the
// file parses but is stale, the cache is still returned (ok=false) so callers
// can salvage fields—like the poster image—that don't change between schema
// versions.
func loadCache(dir string) (*movieCache, bool) {
	data, err := os.ReadFile(filepath.Join(dir, cacheFileName))
	if err != nil {
		return nil, false
	}
	var c movieCache
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, false
	}
	return &c, c.Version >= cacheVersion
}

func saveCache(dir string, info MovieInfo, posterData []byte) error {
	c := movieCache{Version: cacheVersion, MovieInfo: info}
	if len(posterData) > 0 {
		c.PosterBase64 = base64.StdEncoding.EncodeToString(posterData)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, cacheFileName), data, 0o644)
}

func (c *movieCache) posterBytes() []byte {
	if c.PosterBase64 == "" {
		return nil
	}
	data, err := base64.StdEncoding.DecodeString(c.PosterBase64)
	if err != nil {
		return nil
	}
	return data
}

func movieInfoFrom(m *tmdb.Movie) MovieInfo {
	year := ""
	if len(m.ReleaseDate) >= 4 {
		year = m.ReleaseDate[:4]
	}

	genres := make([]string, len(m.Genres))
	for i, g := range m.Genres {
		genres[i] = g.Name
	}

	director := ""
	var actors []string
	if m.Credits != nil {
		for _, c := range m.Credits.Crew {
			if c.Job == "Director" {
				director = c.Name
				break
			}
		}
		for i, c := range m.Credits.Cast {
			if i >= 5 {
				break
			}
			actors = append(actors, c.Name)
		}
	}

	return MovieInfo{
		Title:    m.Title,
		Year:     year,
		Runtime:  m.Runtime,
		Score:    m.VoteAverage,
		Genres:   genres,
		Director: director,
		Actors:   actors,
		Overview: m.Overview,
	}
}

// throttle spaces TMDB API use 600-1200ms apart between invocations (not
// within one). Each invocation is a fresh process, so the time of the last
// request lives on disk. It sleeps, then returns a func that records the
// finish time; call that once the requests are done.
func throttle() func() {
	dir, err := os.UserCacheDir()
	if err != nil {
		return func() {}
	}
	return throttleAt(filepath.Join(dir, "movie-info", "last-request"))
}

// ponytail: no file lock, so parallel invocations can race past each other;
// add flock if runs are ever parallelised.
func throttleAt(path string) func() {
	if data, err := os.ReadFile(path); err == nil {
		if ns, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64); err == nil {
			gap := 600*time.Millisecond + rand.N(600*time.Millisecond)
			time.Sleep(gap - time.Since(time.Unix(0, ns)))
		}
	}
	return func() {
		if os.MkdirAll(filepath.Dir(path), 0o755) == nil {
			_ = os.WriteFile(path, []byte(strconv.FormatInt(time.Now().UnixNano(), 10)), 0o644)
		}
	}
}

// resolveMovieID finds the TMDB id for query. year (may be "") narrows the
// search so e.g. Quarry 2018 and Quarry 2023 resolve to different films.
func resolveMovieID(api *tmdb.TMDb, query string, year string) (int, error) {
	if imdbIDRe.MatchString(query) {
		res, err := api.GetFind(query, "imdb_id", nil)
		if err != nil {
			return 0, err
		}
		if len(res.MovieResults) == 0 {
			return 0, fmt.Errorf("no movie found for imdb id %s", query)
		}
		return res.MovieResults[0].ID, nil
	}

	if year != "" {
		res, err := api.SearchMovie(query, map[string]string{"year": year})
		if err != nil {
			return 0, err
		}
		if len(res.Results) > 0 {
			return res.Results[0].ID, nil
		}
		// ponytail: scene names are sometimes off by one from TMDB's release
		// year, so fall through to an unfiltered search rather than failing.
	}
	res, err := api.SearchMovie(query, nil)
	if err != nil {
		return 0, err
	}
	if len(res.Results) == 0 {
		return 0, fmt.Errorf("no movie found for %q", query)
	}
	return res.Results[0].ID, nil
}

// imdbIDFromDir looks for a *.nfo file in dir and extracts an imdb id (e.g. "tt1375666")
// from its contents, matching plain ids as well as imdb.com URLs.
func imdbIDFromDir(dir string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.nfo"))
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no .nfo file found in %s", dir)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		return "", err
	}
	id := imdbIDInTextRe.FindString(string(data))
	if id == "" {
		return "", fmt.Errorf("no imdb id found in %s", matches[0])
	}
	return id, nil
}

// movieNameFromDirName derives a search title and release year from a
// scene-style directory name, e.g. "Example.Movie.1999.foo.bar.asdf" ->
// ("Example Movie", "1999"), by treating dots/underscores as spaces and cutting
// off at the last 4-digit year or the first scene tag (1080p, remux, etc). The
// last year is used, not the first, so a year that's part of the title itself
// (e.g. "1234.2013.1080p" -> ("1234", "2013")) isn't mistaken for the release
// year. year is "" if the name has none.
func movieNameFromDirName(name string) (title, year string) {
	cleaned := strings.NewReplacer(".", " ", "_", " ").Replace(name)
	cut := len(cleaned)
	if locs := yearRe.FindAllStringIndex(cleaned, -1); len(locs) > 0 {
		last := locs[len(locs)-1]
		year = cleaned[last[0]:last[1]]
		if last[0] < cut {
			cut = last[0]
		}
	}
	if loc := cutoffTagRe.FindStringIndex(cleaned); loc != nil && loc[0] < cut {
		cut = loc[0]
	}
	return strings.TrimSpace(cleaned[:cut]), year
}

// fetchPosterBytes downloads the raw poster image bytes (jpeg) for caching/rendering.
func fetchPosterBytes(posterPath string) ([]byte, error) {
	if posterPath == "" {
		return nil, nil
	}
	resp, err := http.Get(posterBaseURL + posterPath)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// tmuxShowEnv returns tmux's global environment value for name, or "" if unset
// or tmux isn't running. tmux captures this from the client that started the
// server, so it can reveal the outer terminal's TERM_PROGRAM/KITTY_WINDOW_ID
// even though tmux doesn't forward them into the pane's own environment.
func tmuxShowEnv(name string) string {
	out, err := exec.Command("tmux", "show-environment", "-g", name).Output()
	if err != nil {
		return ""
	}
	_, val, ok := strings.Cut(strings.TrimSpace(string(out)), "=")
	if !ok {
		return ""
	}
	return strings.ToLower(val)
}

// renderPoster renders poster image bytes via kitty or sixel graphics; silently does
// nothing if data is empty, undecodable, or the terminal supports neither protocol.
// imageProtocol overrides auto-detection ("auto", "kitty", "sixel", "none") — useful
// when a terminal multiplexer like tmux hides the real terminal's capabilities.
func renderPoster(data []byte, debug bool, imageProtocol string) {
	if len(data) == 0 {
		return
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return
	}

	if debug {
		for _, k := range []string{"TERM", "TERM_PROGRAM", "LC_TERMINAL", "VIM_TERMINAL", "KITTY_WINDOW_ID", "TMUX"} {
			fmt.Fprintf(os.Stderr, "debug: %s=%q\n", k, os.Getenv(k))
		}
	}

	if imageProtocol == "none" {
		if debug {
			fmt.Fprintln(os.Stderr, "debug: -image-protocol=none, skipping image render")
		}
		return
	}

	kittyCapable := imageProtocol == "kitty"
	if imageProtocol == "auto" {
		kittyCapable = rasterm.IsKittyCapable()
		if debug {
			fmt.Fprintf(os.Stderr, "debug: IsKittyCapable()=%v\n", kittyCapable)
		}
		if !kittyCapable && os.Getenv("TMUX") != "" {
			prog := tmuxShowEnv("TERM_PROGRAM")
			kittyWindowID := tmuxShowEnv("KITTY_WINDOW_ID")
			if debug {
				fmt.Fprintf(os.Stderr, "debug: tmux show-environment -g TERM_PROGRAM=%q KITTY_WINDOW_ID=%q\n", prog, kittyWindowID)
			}
			kittyCapable = prog == "wezterm" || prog == "ghostty" || kittyWindowID != ""
		}
	} else if debug {
		fmt.Fprintf(os.Stderr, "debug: kitty forced via -image-protocol=kitty\n")
	}

	switch {
	case kittyCapable:
		if debug {
			fmt.Fprintln(os.Stderr, "debug: using kitty protocol")
		}
		rasterm.KittyWriteImage(os.Stdout, img, rasterm.KittyImgOpts{})
	default:
		sixelOK := imageProtocol == "sixel"
		var sixelErr error
		if imageProtocol == "auto" {
			sixelOK, sixelErr = rasterm.IsSixelCapable()
		}
		if debug {
			if imageProtocol == "sixel" {
				fmt.Fprintln(os.Stderr, "debug: sixel forced via -image-protocol=sixel")
			} else {
				fmt.Fprintf(os.Stderr, "debug: IsSixelCapable()=%v err=%v\n", sixelOK, sixelErr)
			}
		}
		if sixelOK {
			if debug {
				fmt.Fprintln(os.Stderr, "debug: using sixel protocol")
			}
			pImg := image.NewPaletted(img.Bounds(), palette.Plan9)
			draw.FloydSteinberg.Draw(pImg, img.Bounds(), img, image.Point{})
			rasterm.SixelWriteImage(os.Stdout, pImg)
		} else {
			if debug {
				fmt.Fprintln(os.Stderr, "debug: no supported terminal graphics protocol detected")
			}
			fmt.Println("[no terminal graphics]")
		}
	}
	fmt.Println()
}

// printOneLine prints the single-line output selected by -short/-score/-runtime/-genre.
func printOneLine(info MovieInfo, field string) {
	switch field {
	case "score":
		fmt.Printf("%.1f\n", info.Score)
	case "runtime":
		fmt.Println(info.Runtime)
	case "genre":
		// TMDB lists genres in its own order; the first is taken as primary.
		primary := ""
		if len(info.Genres) > 0 {
			primary = info.Genres[0]
		}
		fmt.Println(primary)
	default:
		fmt.Printf("%d min\t%s\t%.1f/10\n", info.Runtime, strings.Join(info.Genres, ", "), info.Score)
	}
}

func printInfo(info MovieInfo, hideTitle bool, wrap int) {
	if !hideTitle {
		printField("", fmt.Sprintf("%s (%s)", info.Title, info.Year), wrap)
	}
	printField("Runtime:  ", fmt.Sprintf("%d min", info.Runtime), wrap)
	printField("Score:    ", fmt.Sprintf("%.1f/10", info.Score), wrap)
	printField("Genres:   ", strings.Join(info.Genres, ", "), wrap)
	printField("Director: ", info.Director, wrap)
	printField("Actors:   ", strings.Join(info.Actors, ", "), wrap)
	printField("Overview: ", info.Overview, wrap)
}

// printProbe appends the ffprobe media block for a movie directory, separated
// from the movie info above it by a blank line. Disabled, given a non-directory
// argument, or probe failure (no ffprobe, no video file) prints nothing —
// the TMDB output stands on its own.
func printProbe(dirPath string, enabled bool, langs []string) {
	if !enabled || dirPath == "" {
		return
	}
	text, err := probeText(dirPath, langs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: probe failed:", err)
		return
	}
	fmt.Printf("\n%s\n", text)
}

const (
	ansiWhite = "\x1b[97m"
	ansiReset = "\x1b[0m"
)

// printField prints "label+text", word-wrapped to wrap characters (0 = no wrap),
// indenting continuation lines under the text column. label is printed in white.
func printField(label, text string, wrap int) {
	coloredLabel := label
	if label != "" {
		coloredLabel = ansiWhite + label + ansiReset
	}
	if wrap <= 0 {
		fmt.Printf("%s%s\n", coloredLabel, text)
		return
	}
	width := max(wrap-len(label), 10)
	indent := strings.Repeat(" ", len(label))
	for i, line := range wrapText(text, width) {
		if i == 0 {
			fmt.Printf("%s%s\n", coloredLabel, line)
		} else {
			fmt.Printf("%s%s\n", indent, line)
		}
	}
}

func wrapText(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{""}
	}
	lines := []string{words[0]}
	for _, w := range words[1:] {
		last := lines[len(lines)-1]
		if len(last)+1+len(w) > width {
			lines = append(lines, w)
		} else {
			lines[len(lines)-1] = last + " " + w
		}
	}
	return lines
}
