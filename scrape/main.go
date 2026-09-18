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
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
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
// than instances of it. Optional fields are multi-valued, so rows repeat per item. One query per
// class keeps each one inside the query service's minute: asking for both at once, or asking per
// model whether it is digital, times out.
const modelQuery = `
SELECT ?item ?itemLabel ?makerLabel ?mountLabel ?date ?image ?focal ?aperture ?article WHERE {
  ?item wdt:P279* wd:%s ; wdt:P176 ?maker .
  OPTIONAL { ?item wdt:P2935 ?mount }
  OPTIONAL { ?item wdt:P5204 ?date }
  OPTIONAL { ?item wdt:P18 ?image }
  OPTIONAL { ?item wdt:P2151 ?focal }
  OPTIONAL { ?item wdt:P7863 ?aperture }
  OPTIONAL { ?article schema:about ?item ; schema:isPartOf <https://en.wikipedia.org/> }
  SERVICE wikibase:label { bd:serviceParam wikibase:language "en" }
}`

// Which of them are digital, as one set rather than a subclass walk per model.
const digitalQuery = `SELECT ?item WHERE { ?item wdt:P279* wd:Q196342 }`

type item struct {
	QID, Name, Kind string
	Brand           string
	Format          string // 35mm, 120, APS, sheet
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

func main() {
	out := flag.String("out", ".", "repository root to write into")
	only := flag.String("brand", "", "only this brand, e.g. nikon (default: every brand)")
	maxWidth := flag.Int("width", 700, "width to download images at")
	skipImages := flag.Bool("skip-images", false, "only refresh the CSV data")
	flag.Parse()

	items, err := fetchItems()
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("%d models from Wikidata", len(items))
	if *only == "" || slug(*only) == "canon" {
		// Wikidata knows little about Canon's FD era, which is most of its film gear.
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
	items = slices.DeleteFunc(items, filmless)
	if *only != "" {
		items = slices.DeleteFunc(items, func(it *item) bool { return slug(it.Brand) != slug(*only) })
	}
	for _, it := range items {
		it.Format = filmFormat(it)
	}
	slices.SortFunc(items, func(a, b *item) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })

	if !*skipImages {
		if err := fetchImages(items, *out, *maxWidth); err != nil {
			log.Fatal(err)
		}
	}
	if err := writeCSVs(items, *out); err != nil {
		log.Fatal(err)
	}
}

// makerName turns Wikidata's company label into the brand a photographer would say.
var (
	makerSuffix = regexp.MustCompile(`(?i)[,\s]+(inc|corp|corporation|co|co\.,? ?ltd|ltd|limited|company|group|holdings|electronics|optics|gmbh|ag|kg|k\.k|s\.a|a/s|ab|oy)\.?$`)
	makerAlias  = map[string]string{
		"asahi optical": "Pentax", "nippon kogaku": "Nikon", "victor hasselblad": "Hasselblad",
		"zenza bronica": "Bronica", "chinon industries": "Chinon", "ricoh imaging": "Pentax",
		"carl zeiss jena": "Carl Zeiss", "veb pentacon": "Pentacon",
		// Soviet factories: their SLRs are known by the name on the front, not the plant.
		"krasnogorskiy zavod": "Zenit", "beloma": "Zenit", "kyiv arsenal": "Kiev",
	}
)

func makerName(label string) string {
	name := strings.TrimSpace(label)
	for {
		shorter := strings.TrimSpace(makerSuffix.ReplaceAllString(name, ""))
		if shorter == name || shorter == "" {
			break
		}
		name = shorter
	}
	if alias, ok := makerAlias[strings.ToLower(name)]; ok {
		return alias
	}
	return name
}

var (
	// Film size, from whatever the name and mount give away. Most SLRs are 35mm, so that is the
	// fallback; the medium and large format ones say so, or come from a maker who only made those.
	// ponytail: a guess from the model number ("645N", "6x7", "67II"); a Rollei 6008 or Fuji
	// GX680 reads as 35mm until someone corrects it in their own copy.
	rollFilm  = regexp.MustCompile(`(?i)(\b6\s*[x\xd7]\s*[4-9]|\b6(?:45|[6-9])(?:[a-z]+|\b)|medium format|120 film|\bsix\b)`)
	sheetFilm = regexp.MustCompile(`(?i)(\b[45]\s*[x\xd7]\s*[57]\b|\b8\s*[x\xd7]\s*10\b|large format|sinar|linhof|graflex)`)
	apsFilm   = regexp.MustCompile(`(?i)(\bAPS\b|IX240|pronea|EOS IX)`)
	packFilm  = regexp.MustCompile(`(?i)(polaroid|instax|SX-70|instant film)`)
	tinyFilm  = regexp.MustCompile(`(?i)\b110\b`) // "Pentax Auto 110"; 110mm keeps its mm
	rollMaker = map[string]bool{"hasselblad": true, "mamiya": true, "bronica": true, "plaubel": true, "norita": true}
)

func filmFormat(it *item) string {
	hay := it.Name + " " + strings.Join(it.Mounts, " ")
	switch {
	case apsFilm.MatchString(hay):
		return "APS"
	case tinyFilm.MatchString(hay):
		return "110"
	case packFilm.MatchString(hay + " " + it.Brand):
		return "instant"
	case sheetFilm.MatchString(hay):
		return "sheet"
	case rollFilm.MatchString(hay) || rollMaker[strings.ToLower(it.Brand)]:
		return "120"
	}
	return "35mm"
}

// digitalOnly names the mounts that never had film behind them.
var digitalOnly = regexp.MustCompile(`(?i)\b(EF-S|EF-M|RF-S|Canon RF|RF-mount|Canon EOS M|Nikon Z|Nikkor Z|Z-mount|Nikon 1|Sony E-?mount|E-mount|FE-mount|FE|Sony E |Micro Four Thirds|Four Thirds|M\.Zuiko|Zuiko Digital|Body Cap Lens|Lumix|Leica D |Leica DG|Fujifilm X|Fujinon X[FC]|XF|Fujifilm G|Fujinon GF|Leica L-?mount|Leica T-?mount|Leica SL|Samsung NX|NX|Pentax Q|DX Nikkor|Nikkor DX|Di III|DN|DT)`) // no closing \b: "EF-S18-55mm" runs the two together

var (
	// Wikidata files phone cameras and cine lenses under the same classes, and has articles about
	// whole families ("Olympus OM system", "Exa cameras") alongside the models.
	junkName  = regexp.MustCompile(`(?i)(\bgalaxy\b|\biphone\b|rear (main |wide |telephoto )?camera|\b(cameras|lenses|system)$|\bT\d\.\d\b)`)
	junkBrand = map[string]bool{"gopro": true, "cooke": true, "panavision": true, "technovision": true}
)

// filmless reports what does not belong in a film gear database: digital bodies, lenses for a mount
// that only ever had a digital body behind it, and entries that are not a model at all.
func filmless(it *item) bool {
	if junkName.MatchString(it.Name) || junkBrand[strings.ToLower(it.Brand)] {
		return true
	}
	if it.Kind == "body" {
		return it.Digital
	}
	return digitalOnly.MatchString(it.Name) || slices.ContainsFunc(it.Mounts, digitalOnly.MatchString)
}

// get reads JSON, waiting out the rate limits and outages these public endpoints have.
func get(u string, v any) error {
	var err error
	for wait := time.Minute; ; wait *= 2 {
		err = getOnce(u, v)
		var busy busyError
		if !errors.As(err, &busy) || wait > 8*time.Minute {
			return err
		}
		log.Printf("%s; retrying in %s", err, wait)
		time.Sleep(wait)
	}
}

type busyError struct{ status, url string }

func (e busyError) Error() string { return e.url + ": " + e.status }

func getOnce(u string, v any) error {
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
	if slices.Contains([]int{429, 500, 502, 503, 504}, resp.StatusCode) {
		return busyError{resp.Status, u}
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: %s", u, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(v)
}

type binding = map[string]struct{ Value string }

func sparql(query string) ([]binding, error) {
	var res struct {
		Results struct {
			Bindings []binding `json:"bindings"`
		} `json:"results"`
	}
	u := "https://query.wikidata.org/sparql?format=json&query=" + url.QueryEscape(query)
	if err := get(u, &res); err != nil {
		return nil, fmt.Errorf("wikidata: %w", err)
	}
	return res.Results.Bindings, nil
}

func lastPath(u string) string { return u[strings.LastIndex(u, "/")+1:] }

// fetchItems asks Wikidata for camera bodies and lenses and folds its repeated rows into one
// record per model.
func fetchItems() ([]*item, error) {
	byQID := map[string]*item{}
	var items []*item
	for _, class := range []struct{ qid, kind string }{{"Q196353", "body"}, {"Q192234", "lens"}} {
		rows, err := sparql(fmt.Sprintf(modelQuery, class.qid))
		if err != nil {
			return nil, err
		}
		log.Printf("wikidata: %d rows of %s", len(rows), class.kind)
		for _, row := range rows {
			val := func(k string) string { return row[k].Value }
			qid := lastPath(val("item"))
			it := byQID[qid]
			if it == nil {
				name := strings.TrimSpace(val("itemLabel"))
				if class.kind == "body" {
					name = strings.TrimSuffix(name, " camera") // "Canon EF camera" is the Canon EF
				}
				it = &item{QID: qid, Name: name, Kind: class.kind, Brand: makerName(val("makerLabel")), Source: "wikidata"}
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
				if name, err := url.PathUnescape(lastPath(f)); err == nil {
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
	}
	digital, err := sparql(digitalQuery)
	if err != nil {
		return nil, err
	}
	for _, row := range digital {
		if it, ok := byQID[lastPath(row["item"].Value)]; ok {
			it.Digital = true
		}
	}

	// Drop entries that are families rather than models ("Canon A series") and anything unnamed.
	items = slices.DeleteFunc(items, func(i *item) bool {
		return i.Name == "" || strings.HasPrefix(i.Name, "Q") || i.Brand == "" || seriesName.MatchString(i.Name)
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
func fetchImages(items []*item, out string, width int) error {
	type imageInfo struct {
		ThumbURL       string `json:"thumburl"`
		DescriptionURL string `json:"descriptionurl"`
		// Commons returns some of these as numbers, so take them as they come.
		ExtMetadata map[string]struct {
			Value any `json:"value"`
		} `json:"extmetadata"`
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
			rel := filepath.Join("images", slug(it.Brand), slug(it.Name)+".jpg")
			path := filepath.Join(out, rel)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if _, err := os.Stat(path); err != nil {
				if err := download(info.ThumbURL, path); err != nil {
					log.Printf("%s: %v", it.Name, err)
					continue
				}
				time.Sleep(150 * time.Millisecond) // be a good Commons citizen
			}
			it.Image = filepath.ToSlash(rel)
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
		keep[filepath.ToSlash(it.Image)] = true
	}
	root := filepath.Join(out, "images")
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".jpg") {
			return nil
		}
		if rel, err := filepath.Rel(out, p); err == nil && !keep[filepath.ToSlash(rel)] {
			os.Remove(p)
		}
		return nil
	})
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

func writeCSVs(items []*item, out string) error {
	bodyHead := []string{"slug", "name", "brand", "type", "mount", "film_format", "introduced", "image", "wikidata", "commons_file", "wikipedia", "source"}
	lensHead := []string{"slug", "name", "brand", "mount", "film_format", "focal_length_mm", "max_aperture", "introduced", "filter_mm", "weight_g", "image", "wikidata", "commons_file", "wikipedia", "source"}
	bodies, lenses := map[string][][]string{}, map[string][][]string{}
	for _, it := range items {
		dir := slug(it.Brand)
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
			bodies[dir] = append(bodies[dir], []string{slug(it.Name), it.Name, it.Brand, kind, mounts, it.Format, year, it.Image, it.QID, it.CommonsFile, it.Wikipedia, it.Source})
		case "lens":
			lenses[dir] = append(lenses[dir], []string{slug(it.Name), it.Name, it.Brand, mounts, it.Format, joinNumbers(it.Focal), fastestAperture(it.Aperture), year, it.Filter, it.Weight, it.Image, it.QID, it.CommonsFile, it.Wikipedia, it.Source})
		}
	}
	// Brands that lost every model keep an empty file rather than a stale one.
	old, _ := filepath.Glob(filepath.Join(out, "data", "*", "*.csv"))
	for _, p := range old {
		os.Remove(p)
	}
	nb, nl := 0, 0
	for _, part := range []struct {
		rows map[string][][]string
		head []string
		file string
		n    *int
	}{{bodies, bodyHead, "bodies.csv", &nb}, {lenses, lensHead, "lenses.csv", &nl}} {
		for dir, rows := range part.rows {
			if err := writeCSV(filepath.Join(out, "data", dir, part.file), append([][]string{part.head}, rows...)); err != nil {
				return err
			}
			*part.n += len(rows)
		}
	}
	log.Printf("%d brands, %d bodies, %d lenses", len(bodies)+len(lenses), nb, nl)
	return nil
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
