// Command scrape builds the camera gear database from Wikidata and Wikimedia Commons.
//
//	go run ./scrape -out .
//
// Wikidata's data is CC0. Images come from Commons, each under its own free licence; the author
// and licence of every downloaded image are recorded in images/CREDITS.csv.
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const userAgent = "camera-gear-database/0.1 (https://github.com/kapsikkum/camera-gear-database)"

// Wikidata models camera and lens models as classes, so they are subclasses of their type rather
// than instances of it. Optional fields are multi-valued, so rows repeat per item.
const query = `
SELECT ?item ?itemLabel ?kind ?digital ?mountLabel ?date ?image ?focal ?aperture ?article WHERE {
  ?item wdt:P176 wd:%s .
  { ?item wdt:P279* wd:Q196353 . BIND("body" AS ?kind) }
  UNION
  { ?item wdt:P279* wd:Q192234 . BIND("lens" AS ?kind) }
  BIND(EXISTS { ?item wdt:P279* wd:Q196342 } AS ?digital)
  OPTIONAL { ?item wdt:P2935 ?mount }
  OPTIONAL { ?item wdt:P5204 ?date }
  OPTIONAL { ?item wdt:P18 ?image }
  OPTIONAL { ?item wdt:P2151 ?focal }
  OPTIONAL { ?item wdt:P7863 ?aperture }
  OPTIONAL { ?article schema:about ?item ; schema:isPartOf <https://en.wikipedia.org/> }
  SERVICE wikibase:label { bd:serviceParam wikibase:language "en" }
}`

type item struct {
	QID, Name, Kind string
	Digital         bool
	Mounts          []string
	Introduced      string
	CommonsFile     string
	Focal, Aperture []string
	Filter, Weight  string
	Wikipedia       string
	Source          string // wikidata or wikipedia
	Image           string // path in the repo, once downloaded
}

type brand struct{ name, qid string }

var brands = map[string]brand{"canon": {"Canon", "Q62621"}}

func main() {
	out := flag.String("out", ".", "repository root to write into")
	which := flag.String("brand", "canon", "brand to scrape")
	maxWidth := flag.Int("width", 900, "width to download images at")
	skipImages := flag.Bool("skip-images", false, "only refresh the CSV data")
	flag.Parse()

	b, ok := brands[strings.ToLower(*which)]
	if !ok {
		log.Fatalf("unknown brand %q", *which)
	}
	items, err := fetchItems(b)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("%s: %d models from Wikidata", b.name, len(items))
	if b.qid == brands["canon"].qid {
		for _, src := range []struct {
			what string
			fn   func() ([]*item, error)
		}{{"FD lens list", fdItems}, {"SLR body lists", canonBodies}} {
			rows, err := src.fn()
			if err != nil {
				log.Printf("wikipedia %s: %v", src.what, err)
				continue
			}
			before := len(items)
			items = merge(items, rows)
			log.Printf("Wikipedia %s: %d rows, %d new models", src.what, len(rows), len(items)-before)
		}
	}
	items = fold(items)
	slices.SortFunc(items, func(a, b *item) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })

	if !*skipImages {
		if err := fetchImages(items, *out, strings.ToLower(*which), *maxWidth); err != nil {
			log.Fatal(err)
		}
	}
	if err := writeCSVs(items, *out, strings.ToLower(*which)); err != nil {
		log.Fatal(err)
	}
}

func get(u string, v any) error {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: %s", u, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(v)
}

// fetchItems runs the SPARQL query and folds its repeated rows into one record per model.
func fetchItems(b brand) ([]*item, error) {
	var res struct {
		Results struct {
			Bindings []map[string]struct{ Value string } `json:"bindings"`
		} `json:"results"`
	}
	u := "https://query.wikidata.org/sparql?format=json&query=" + url.QueryEscape(fmt.Sprintf(query, b.qid))
	if err := get(u, &res); err != nil {
		return nil, fmt.Errorf("wikidata: %w", err)
	}
	byQID := map[string]*item{}
	var items []*item
	for _, row := range res.Results.Bindings {
		val := func(k string) string { return row[k].Value }
		qid := val("item")[strings.LastIndex(val("item"), "/")+1:]
		it := byQID[qid]
		if it == nil {
			name := strings.TrimSpace(val("itemLabel"))
			if val("kind") == "body" {
				name = strings.TrimSuffix(name, " camera") // "Canon EF camera" is the Canon EF
			}
			it = &item{QID: qid, Name: name, Kind: val("kind"), Digital: val("digital") == "true", Source: "wikidata"}
			byQID[qid] = it
			items = append(items, it)
		}
		// P2935 covers every connector, so keep the lens mounts and drop hot shoes and HDMI sockets.
		if m := val("mountLabel"); strings.Contains(strings.ToLower(m), "lens mount") && !slices.Contains(it.Mounts, m) {
			it.Mounts = append(it.Mounts, m)
		}
		if d := val("date"); d != "" && (it.Introduced == "" || d < it.Introduced) {
			it.Introduced = d
		}
		if f := val("image"); f != "" && it.CommonsFile == "" {
			// P18 comes back as https://commons.wikimedia.org/wiki/Special:FilePath/Canon%20AE-1....jpg
			name, err := url.PathUnescape(f[strings.LastIndex(f, "/")+1:])
			if err == nil {
				it.CommonsFile = name
			}
		}
		if f := val("focal"); f != "" && !slices.Contains(it.Focal, f) {
			it.Focal = append(it.Focal, f)
		}
		if a := val("aperture"); a != "" && !slices.Contains(it.Aperture, a) {
			it.Aperture = append(it.Aperture, a)
		}
		if w := val("article"); w != "" {
			it.Wikipedia = w
		}
	}
	// Drop entries that are families rather than models ("Canon A series") and anything unnamed.
	items = slices.DeleteFunc(items, func(i *item) bool {
		return i.Name == "" || strings.HasPrefix(i.Name, "Q") || seriesName.MatchString(i.Name)
	})
	slices.SortFunc(items, func(a, b *item) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return items, nil
}

var (
	// "Canon EF 24-105mm lens" is Wikidata's article about every 24-105 Canon made, not a model.
	genericName = regexp.MustCompile(`(?i)^(.*)\s+lens$`)
	focalPrefix = regexp.MustCompile(`(?i)^(.*?\d[\d.]*(?:\s*-\s*[\d.]+)?\s*mm)`)
)

// fold drops the vague entries that name a focal length rather than a model, once a real model
// with that focal length is in the database. What the vague entry knew is kept.
func fold(items []*item) []*item {
	dashes := strings.NewReplacer("–", "-", "—", "-")
	group := func(name string) string {
		m := focalPrefix.FindStringSubmatch(dashes.Replace(name))
		if m == nil {
			return ""
		}
		return strings.ToLower(strings.Join(strings.Fields(m[1]), ""))
	}
	specific := map[string][]*item{}
	for _, it := range items {
		it.Name = dashes.Replace(it.Name)
		if g := group(it.Name); g != "" && !genericName.MatchString(it.Name) {
			specific[g] = append(specific[g], it)
		}
	}
	return slices.DeleteFunc(items, func(it *item) bool {
		real := specific[group(it.Name)]
		if !genericName.MatchString(it.Name) || len(real) == 0 {
			return false
		}
		for _, r := range real {
			if r.Wikipedia == "" {
				r.Wikipedia = it.Wikipedia
			}
			if r.CommonsFile == "" {
				r.CommonsFile = it.CommonsFile // a photo of the family beats no photo
			}
		}
		return true
	})
}

var seriesName = regexp.MustCompile(`(?i)\b(series|family|lens mount|mount)$`)

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	return strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// fetchImages downloads each model's Commons image at a sensible width and records its licence.
func fetchImages(items []*item, out, brandDir string, width int) error {
	type imageInfo struct {
		ThumbURL       string `json:"thumburl"`
		DescriptionURL string `json:"descriptionurl"`
		// Commons returns some of these as numbers, so take them as they come.
		ExtMetadata map[string]struct {
			Value any `json:"value"`
		} `json:"extmetadata"`
	}
	dir := filepath.Join(out, "images", brandDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	credits := [][]string{{"image", "model", "commons_file", "author", "licence", "licence_url", "source"}}

	var pending []*item
	for _, it := range items {
		if it.CommonsFile != "" {
			pending = append(pending, it)
		}
	}
	for start := 0; start < len(pending); start += 25 {
		batch := pending[start:min(start+25, len(pending))]
		titles := make([]string, len(batch))
		for i, it := range batch {
			titles[i] = "File:" + it.CommonsFile
		}
		q := url.Values{
			"action": {"query"}, "format": {"json"}, "prop": {"imageinfo"},
			"iiprop": {"url|extmetadata"}, "iiurlwidth": {strconv.Itoa(width)},
			"titles": {strings.Join(titles, "|")},
		}
		var res struct {
			Query struct {
				Pages map[string]struct {
					Title     string      `json:"title"`
					ImageInfo []imageInfo `json:"imageinfo"`
				} `json:"pages"`
			} `json:"query"`
		}
		if err := get("https://commons.wikimedia.org/w/api.php?"+q.Encode(), &res); err != nil {
			return fmt.Errorf("commons: %w", err)
		}
		byTitle := map[string]imageInfo{}
		for _, p := range res.Query.Pages {
			if len(p.ImageInfo) > 0 {
				byTitle[p.Title] = p.ImageInfo[0]
			}
		}
		for _, it := range batch {
			info, ok := byTitle["File:"+it.CommonsFile]
			if !ok || info.ThumbURL == "" {
				log.Printf("no image for %s", it.Name)
				continue
			}
			name := slug(it.Name) + ".jpg"
			path := filepath.Join(dir, name)
			if _, err := os.Stat(path); err != nil {
				if err := download(info.ThumbURL, path); err != nil {
					log.Printf("%s: %v", it.Name, err)
					continue
				}
				time.Sleep(150 * time.Millisecond) // be a good Commons citizen
			}
			it.Image = filepath.ToSlash(filepath.Join("images", brandDir, name))
			meta := func(k string) string {
				v, ok := info.ExtMetadata[k]
				if !ok || v.Value == nil {
					return ""
				}
				return strings.TrimSpace(stripHTML(fmt.Sprint(v.Value)))
			}
			credits = append(credits, []string{it.Image, it.Name, it.CommonsFile, meta("Artist"), meta("LicenseShortName"), meta("LicenseUrl"), info.DescriptionURL})
		}
		log.Printf("images %d/%d", min(start+25, len(pending)), len(pending))
	}
	keep := map[string]bool{}
	for _, it := range items {
		keep[filepath.Base(it.Image)] = true
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() && !keep[e.Name()] {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	slices.SortFunc(credits[1:], func(a, b []string) int { return strings.Compare(a[0], b[0]) })
	return writeCSV(filepath.Join(out, "images", "CREDITS.csv"), credits)
}

func download(u, path string) error {
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: %s", u, resp.Status)
	}
	f, err := os.Create(path + ".part")
	if err != nil {
		return err
	}
	_, err = io.Copy(f, io.LimitReader(resp.Body, 16<<20))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path + ".part")
		return err
	}
	return os.Rename(path+".part", path)
}

var tagRe = regexp.MustCompile(`<[^>]*>`)

func stripHTML(s string) string {
	s = tagRe.ReplaceAllString(s, "")
	return strings.Join(strings.Fields(strings.NewReplacer("&amp;", "&", "&quot;", `"`, "&#039;", "'", "&nbsp;", " ").Replace(s)), " ")
}

func writeCSVs(items []*item, out, brandDir string) error {
	bodies := [][]string{{"slug", "name", "type", "mount", "introduced", "image", "wikidata", "commons_file", "wikipedia", "source"}}
	lenses := [][]string{{"slug", "name", "mount", "focal_length_mm", "max_aperture", "introduced", "filter_mm", "weight_g", "image", "wikidata", "commons_file", "wikipedia", "source"}}
	for _, it := range items {
		mounts := strings.Join(it.Mounts, "; ")
		year := it.Introduced
		if len(year) >= 4 {
			year = year[:4]
		}
		switch it.Kind {
		case "body":
			kind := "SLR"
			if it.Digital {
				kind = "DSLR"
			}
			bodies = append(bodies, []string{slug(it.Name), it.Name, kind, mounts, year, it.Image, it.QID, it.CommonsFile, it.Wikipedia, it.Source})
		case "lens":
			lenses = append(lenses, []string{slug(it.Name), it.Name, mounts, joinNumbers(it.Focal), fastestAperture(it.Aperture), year, it.Filter, it.Weight, it.Image, it.QID, it.CommonsFile, it.Wikipedia, it.Source})
		}
	}
	dir := filepath.Join(out, "data", brandDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeCSV(filepath.Join(dir, "bodies.csv"), bodies); err != nil {
		return err
	}
	log.Printf("%d bodies, %d lenses", len(bodies)-1, len(lenses)-1)
	return writeCSV(filepath.Join(dir, "lenses.csv"), lenses)
}

// joinNumbers formats Wikidata's numbers as "28-70" or "1.4".
func joinNumbers(vals []string) string {
	var out []string
	for _, v := range vals {
		if f, err := strconv.ParseFloat(strings.TrimPrefix(v, "+"), 64); err == nil {
			out = append(out, strconv.FormatFloat(f, 'f', -1, 64))
		}
	}
	slices.SortFunc(out, func(a, b string) int {
		x, _ := strconv.ParseFloat(a, 64)
		y, _ := strconv.ParseFloat(b, 64)
		return int(x*1000 - y*1000)
	})
	return strings.Join(out, "-")
}

// fastestAperture reports the widest of a lens's f-numbers, the one its name is known by.
func fastestAperture(vals []string) string {
	best := ""
	for _, v := range vals {
		f, err := strconv.ParseFloat(strings.TrimPrefix(v, "+"), 64)
		if err != nil {
			continue
		}
		if b, err := strconv.ParseFloat(best, 64); best == "" || (err == nil && f < b) {
			best = strconv.FormatFloat(f, 'f', -1, 64)
		}
	}
	return best
}

func writeCSV(path string, rows [][]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	w.UseCRLF = false
	if err := w.WriteAll(rows); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
