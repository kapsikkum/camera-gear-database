package main

// Wikidata knows little about Canon's FD-era gear, which is the film part. Wikipedia's
// "Canon FD lens mount" article lists every FD camera and lens in two tables, so read those too.

import (
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

type table struct {
	Head []string
	Rows [][]string
}

var (
	tableRe = regexp.MustCompile(`(?s)<table[^>]*>(.*?)</table>`)
	rowRe   = regexp.MustCompile(`(?s)<tr[^>]*>(.*?)</tr>`)
	cellRe  = regexp.MustCompile(`(?s)<(t[dh])([^>]*)>(.*?)</(?:td|th)>`)
	spanRe  = regexp.MustCompile(`(?i)(rowspan|colspan)\s*=\s*"?(\d+)`)
	refRe   = regexp.MustCompile(`\[\d+\]`)
)

// parseTables turns rendered wiki tables into a grid, expanding cells that span rows or columns.
func parseTables(page string) []table {
	var out []table
	for _, t := range tableRe.FindAllStringSubmatch(page, -1) {
		var grid [][]string
		carry := map[int]struct {
			text string
			left int
		}{}
		for _, r := range rowRe.FindAllStringSubmatch(t[1], -1) {
			var row []string
			col := 0
			place := func(text string, cols int) {
				for range cols {
					for {
						if c, ok := carry[col]; ok && c.left > 0 {
							row = append(row, c.text)
							c.left--
							if c.left == 0 {
								delete(carry, col)
							} else {
								carry[col] = c
							}
							col++
							continue
						}
						break
					}
					row = append(row, text)
					col++
				}
			}
			for _, c := range cellRe.FindAllStringSubmatch(r[1], -1) {
				rowspan, colspan := 1, 1
				for _, s := range spanRe.FindAllStringSubmatch(c[2], -1) {
					n, _ := strconv.Atoi(s[2])
					if strings.EqualFold(s[1], "rowspan") {
						rowspan = min(n, 50)
					} else {
						colspan = min(n, 20)
					}
				}
				text := cellText(c[3])
				start := col
				place(text, colspan)
				if rowspan > 1 {
					for i := start; i < start+colspan; i++ {
						carry[i] = struct {
							text string
							left int
						}{text, rowspan - 1}
					}
				}
			}
			// trailing cells carried down from earlier rows
			for {
				c, ok := carry[col]
				if !ok || c.left == 0 {
					break
				}
				row = append(row, c.text)
				c.left--
				if c.left == 0 {
					delete(carry, col)
				} else {
					carry[col] = c
				}
				col++
			}
			if len(row) > 0 {
				grid = append(grid, row)
			}
		}
		if len(grid) > 1 {
			out = append(out, table{Head: grid[0], Rows: grid[1:]})
		}
	}
	return out
}

var tagOrComment = regexp.MustCompile(`(?s)<!--.*?-->|<[^>]*>`)

func cellText(s string) string {
	s = strings.ReplaceAll(s, "<br />", " ")
	s = tagOrComment.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = refRe.ReplaceAllString(s, "")
	s = strings.NewReplacer(" ", " ", "–", "-", "—", "-", "×", "x").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

// sections lists an article's section titles and indexes.
func sections(page string) ([]struct{ Index, Line string }, error) {
	var res struct {
		Parse struct {
			Sections []struct {
				Index string `json:"index"`
				Line  string `json:"line"`
			} `json:"sections"`
		} `json:"parse"`
	}
	u := "https://en.wikipedia.org/w/api.php?action=parse&format=json&formatversion=2&prop=sections&page=" + url.QueryEscape(page)
	if err := get(u, &res); err != nil {
		return nil, err
	}
	out := make([]struct{ Index, Line string }, 0, len(res.Parse.Sections))
	for _, s := range res.Parse.Sections {
		out = append(out, struct{ Index, Line string }{s.Index, cellText(s.Line)})
	}
	return out, nil
}

func sectionHTML(page, index string) (string, error) {
	var res struct {
		Parse struct {
			Text string `json:"text"`
		} `json:"parse"`
	}
	u := "https://en.wikipedia.org/w/api.php?action=parse&format=json&formatversion=2&prop=text&section=" + index + "&page=" + url.QueryEscape(page)
	err := get(u, &res)
	return res.Parse.Text, err
}

var listItemRe = regexp.MustCompile(`(?s)<li[^>]*>(.*?)</li>`)

// canonBodies reads Canon's SLR bodies from the bulleted lists in "List of Canon products",
// which covers the film-era models Wikidata is missing.
func canonBodies() ([]*item, error) {
	secs, err := sections("List of Canon products")
	if err != nil {
		return nil, err
	}
	mounts := map[string]string{
		"canonflex slr": "Canon R lens mount", "fl-mount slr": "Canon FL lens mount", "ee-mount slr": "Canon EE lens mount",
		"fd-mount slr": "Canon FD lens mount", "f series": "Canon FD lens mount", "a series": "Canon FD lens mount",
		"t series": "Canon FD lens mount", "film slr eos cameras": "Canon EF lens mount",
	}
	var out []*item
	for _, sec := range secs {
		mount, ok := mounts[strings.ToLower(sec.Line)]
		if !ok {
			continue
		}
		page, err := sectionHTML("List of Canon products", sec.Index)
		if err != nil {
			return nil, err
		}
		for _, li := range listItemRe.FindAllStringSubmatch(page, -1) {
			text := cellText(li[1])
			name, _, _ := strings.Cut(text, "(")
			name = strings.TrimSpace(name)
			if !strings.HasPrefix(name, "Canon") || len(name) > 40 {
				continue
			}
			out = append(out, &item{Name: name, Kind: "body", Mounts: []string{mount}, Introduced: yearRe.FindString(text), Source: "wikipedia"})
		}
	}
	return out, nil
}

// wikiSection fetches one section of an article as rendered HTML.
func wikiSection(page, section string) (string, error) {
	var res struct {
		Parse struct {
			Text     string `json:"text"`
			Sections []struct {
				Index string `json:"index"`
				Line  string `json:"line"`
			} `json:"sections"`
		} `json:"parse"`
	}
	base := "https://en.wikipedia.org/w/api.php?action=parse&format=json&formatversion=2&page=" + url.QueryEscape(page)
	if err := get(base+"&prop=sections", &res); err != nil {
		return "", err
	}
	index := ""
	for _, s := range res.Parse.Sections {
		if strings.EqualFold(strings.TrimSpace(s.Line), section) {
			index = s.Index
		}
	}
	if index == "" {
		return "", fmt.Errorf("%s: no section %q", page, section)
	}
	res.Parse.Text = ""
	if err := get(base+"&prop=text&section="+index, &res); err != nil {
		return "", err
	}
	return res.Parse.Text, nil
}

func column(head []string, names ...string) int {
	for i, h := range head {
		for _, n := range names {
			if strings.HasPrefix(strings.ToLower(h), strings.ToLower(n)) {
				return i
			}
		}
	}
	return -1
}

func cell(row []string, i int) string {
	if i < 0 || i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}

var (
	yearRe     = regexp.MustCompile(`(19|20)\d{2}`)
	numberRe   = regexp.MustCompile(`\d+(\.\d+)?`)
	apertureRe = regexp.MustCompile(`f/\s*(\d+(\.\d+)?)`)
)

// fdItems reads Canon's FD cameras and lenses from Wikipedia.
func fdItems() ([]*item, error) {
	var out []*item
	for _, src := range []struct{ section, kind, mount string }{
		{"FD cameras", "body", "Canon FD lens mount"},
		{"FD lenses", "lens", "Canon FD lens mount"},
	} {
		page, err := wikiSection("Canon FD lens mount", src.section)
		if err != nil {
			return nil, err
		}
		for _, t := range parseTables(page) {
			name := column(t.Head, "name", "model", "camera")
			if name < 0 {
				continue
			}
			aperture, year := column(t.Head, "aperture"), column(t.Head, "year", "introduced")
			filter, weight := column(t.Head, "filter"), column(t.Head, "wgt", "weight")
			focal := column(t.Head, "focal")
			for _, row := range t.Rows {
				n := cell(row, name)
				// Section rows inside the table repeat one label across every column.
				if n == "" || len(row) < 3 || strings.EqualFold(n, cell(row, 0)) && distinctCells(row) < 3 {
					continue
				}
				it := &item{Name: "Canon " + n, Kind: src.kind, Mounts: []string{src.mount}, Source: "wikipedia"}
				it.Introduced = yearRe.FindString(cell(row, year))
				if m := apertureRe.FindStringSubmatch(cell(row, aperture)); m != nil {
					it.Aperture = []string{m[1]}
				} else if m := apertureRe.FindStringSubmatch(n); m != nil {
					it.Aperture = []string{m[1]}
				}
				it.Focal = focalFromName(n)
				if len(it.Focal) == 0 {
					if f := numberRe.FindString(cell(row, focal)); f != "" {
						it.Focal = []string{f}
					}
				}
				it.Filter = numberRe.FindString(cell(row, filter))
				it.Weight = numberRe.FindString(cell(row, weight))
				out = append(out, it)
			}
		}
	}
	return out, nil
}

var focalRe = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*(?:-\s*(\d+(?:\.\d+)?)\s*)?mm`)

// focalFromName reads "FD 100-200mm f/5.6" as 100 to 200 mm.
func focalFromName(name string) []string {
	m := focalRe.FindStringSubmatch(name)
	if m == nil {
		return nil
	}
	if m[2] != "" {
		return []string{m[1], m[2]}
	}
	return []string{m[1]}
}

func distinctCells(row []string) int {
	seen := map[string]bool{}
	for _, c := range row {
		seen[c] = true
	}
	return len(seen)
}

var normalise = regexp.MustCompile(`[^a-z0-9.]+`)

// key matches the same model named slightly differently in each source.
func key(name string) string {
	n := strings.ToLower(name)
	for _, drop := range []string{"canon", "new fd", "f/", "s.s.c.", "s.c.", "ssc", "sc", "lens", "mm"} {
		n = strings.ReplaceAll(n, drop, " ")
	}
	return normalise.ReplaceAllString(n, "")
}

// merge adds Wikipedia's models to the Wikidata ones, filling gaps rather than replacing.
func merge(wikidata, wiki []*item) []*item {
	byKey := map[string]*item{}
	for _, it := range wikidata {
		byKey[key(it.Name)] = it
	}
	for _, w := range wiki {
		if it, ok := byKey[key(w.Name)]; ok {
			if it.Introduced == "" {
				it.Introduced = w.Introduced
			}
			if len(it.Aperture) == 0 {
				it.Aperture = w.Aperture
			}
			if len(it.Focal) == 0 {
				it.Focal = w.Focal
			}
			if it.Filter == "" {
				it.Filter, it.Weight = w.Filter, w.Weight
			}
			if len(it.Mounts) == 0 {
				it.Mounts = w.Mounts
			}
			continue
		}
		byKey[key(w.Name)] = w
		wikidata = append(wikidata, w)
	}
	return wikidata
}
