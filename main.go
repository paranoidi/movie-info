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
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BourgeoisBear/rasterm"
	tmdb "github.com/ryanbradynd05/go-tmdb"
)

const posterBaseURL = "https://image.tmdb.org/t/p/w500"
const cacheFileName = ".movie-info.json"
const cacheVersion = 1

var imdbIDRe = regexp.MustCompile(`^tt\d+$`)
var imdbIDInTextRe = regexp.MustCompile(`tt\d+`)
var yearRe = regexp.MustCompile(`\b(19|20)\d{2}\b`)

// MovieInfo is the subset of movie data we display and cache.
type MovieInfo struct {
	Title    string   `json:"title"`
	Year     string   `json:"year"`
	Runtime  uint32   `json:"runtime"`
	Score    float32  `json:"score"`
	Genres   []string `json:"genres"`
	Director string   `json:"director"`
	Actors   []string `json:"actors"`
}

// movieCache is the on-disk shape of .movie-info.json.
type movieCache struct {
	Version int `json:"version"`
	MovieInfo
	PosterBase64 string `json:"poster_base64,omitempty"`
}

// Config is the on-disk shape of $XDG_CONFIG_HOME/movie-info/config.json
// (~/.config/movie-info/config.json on Linux if XDG_CONFIG_HOME is unset).
// ShowTitle/Wrap are pointers so "absent" is distinguishable from "false"/"0".
type Config struct {
	APIKey    string `json:"api_key,omitempty"`
	ShowTitle *bool  `json:"show_title,omitempty"`
	Wrap      *int   `json:"wrap,omitempty"`
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
  "wrap": 79
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

	if err := run(flag.Args(), *showTitle, *wrap); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// run resolves and renders the movie information for args. Any failure to do
// so — a TMDB error, a bad cache/config file, or an unexpected panic — comes
// back as a non-nil error so main can exit with status 1.
func run(args []string, showTitle bool, wrap int) (err error) {
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
	effWrap := wrap
	if !setFlags["wrap"] && cfg.Wrap != nil {
		effWrap = *cfg.Wrap
	}

	dirPath := ""
	if len(args) == 1 {
		if st, statErr := os.Stat(args[0]); statErr == nil && st.IsDir() {
			dirPath = args[0]
		}
	}

	if dirPath != "" {
		if cache, ok := loadCache(dirPath); ok {
			renderPoster(cache.posterBytes())
			printInfo(cache.MovieInfo, !effShowTitle, effWrap)
			return nil
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
	if dirPath != "" {
		if id, nfoErr := imdbIDFromDir(dirPath); nfoErr == nil {
			query = id
		} else {
			query = movieNameFromDirName(filepath.Base(dirPath))
		}
	}

	movieID, err := resolveMovieID(api, query)
	if err != nil {
		return err
	}

	movie, err := api.GetMovieInfo(movieID, map[string]string{"append_to_response": "credits"})
	if err != nil {
		return err
	}

	posterData, _ := fetchPosterBytes(movie.PosterPath)
	renderPoster(posterData)
	info := movieInfoFrom(movie)
	printInfo(info, !effShowTitle, effWrap)

	if dirPath != "" {
		if err := saveCache(dirPath, info, posterData); err != nil {
			fmt.Fprintln(os.Stderr, "warning: failed to save cache:", err)
		}
	}

	return nil
}

// loadCache reads dir/.movie-info.json; ok is false if it's missing, unparsable,
// or older than the schema this build writes (forcing a fresh fetch).
func loadCache(dir string) (*movieCache, bool) {
	data, err := os.ReadFile(filepath.Join(dir, cacheFileName))
	if err != nil {
		return nil, false
	}
	var c movieCache
	if err := json.Unmarshal(data, &c); err != nil || c.Version < cacheVersion {
		return nil, false
	}
	return &c, true
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
	}
}

func resolveMovieID(api *tmdb.TMDb, query string) (int, error) {
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

// movieNameFromDirName derives a search title from a scene-style directory name,
// e.g. "The.Matrix.1999.foo.bar.asdf" -> "The Matrix", by treating dots/underscores
// as spaces and cutting off at the first 4-digit year.
func movieNameFromDirName(name string) string {
	cleaned := strings.NewReplacer(".", " ", "_", " ").Replace(name)
	if loc := yearRe.FindStringIndex(cleaned); loc != nil {
		cleaned = cleaned[:loc[0]]
	}
	return strings.TrimSpace(cleaned)
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

// renderPoster renders poster image bytes via kitty or sixel graphics; silently does
// nothing if data is empty, undecodable, or the terminal supports neither protocol.
func renderPoster(data []byte) {
	if len(data) == 0 {
		return
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return
	}

	switch {
	case rasterm.IsKittyCapable():
		rasterm.KittyWriteImage(os.Stdout, img, rasterm.KittyImgOpts{})
	default:
		if ok, _ := rasterm.IsSixelCapable(); ok {
			pImg := image.NewPaletted(img.Bounds(), palette.Plan9)
			draw.FloydSteinberg.Draw(pImg, img.Bounds(), img, image.Point{})
			rasterm.SixelWriteImage(os.Stdout, pImg)
		}
	}
	fmt.Println()
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
