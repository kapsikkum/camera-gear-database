# Camera gear database

An open database of camera bodies and lenses, with a picture of each, built for film photography apps.
Canon first; other brands can follow the same shape. Film gear only: digital bodies and the
crop-sensor lenses that will not mount on a 35mm camera (EF-S, EF-M, RF-S) are left out.

Everything here is generated from [Wikidata](https://www.wikidata.org/) and English Wikipedia by the
scraper in `scrape/`, so it can be rebuilt and kept current.

## What's in it

| File | Rows |
| --- | --- |
| `data/canon/bodies.csv` | Canon film SLR bodies |
| `data/canon/lenses.csv` | Canon lenses (FD, FL, R, EF, RF) |
| `images/canon/*.jpg` | One photo per model, up to 900px wide |
| `images/CREDITS.csv` | Author and licence of every image |

**bodies.csv** — `slug, name, type, mount, introduced, image, wikidata, commons_file, wikipedia, source`
where `type` is `SLR` (film) or `DSLR`.

**lenses.csv** — `slug, name, mount, focal_length_mm, max_aperture, introduced, filter_mm, weight_g,
image, wikidata, commons_file, wikipedia, source`. Zooms give their range as `100-200`; `max_aperture`
is the widest f-number, so `1.4` means f/1.4.

`slug` is a stable id made from the name. `source` says whether a row came from Wikidata or Wikipedia.

## Where the data comes from

- **Wikidata** for models, mounts, dates and the linked Commons photo. Cameras and lenses are modelled
  as classes there (*subclass of* single-lens reflex camera or camera lens), not instances.
- **Wikipedia** for Canon's FD era, which Wikidata barely covers: the lens table in
  [Canon FD lens mount](https://en.wikipedia.org/wiki/Canon_FD_lens_mount) (with filter size and weight)
  and the body lists in [List of Canon products](https://en.wikipedia.org/wiki/List_of_Canon_products).
- **Wikimedia Commons** for the photos, fetched at a sensible size through its API.

Wikipedia rows fill gaps: where a model already exists from Wikidata, only its empty fields are filled.

## Licences

- **Data** (`data/`): [CC0 1.0](LICENSE). Wikidata is CC0; the facts taken from Wikipedia (names, years,
  filter sizes) aren't copyrightable.
- **Images** (`images/`): each keeps its own licence from Commons, mostly CC BY-SA or CC BY, a few public
  domain. `images/CREDITS.csv` records the author, licence and source page for every file. If you use an
  image, credit its author and keep its licence. This repository is not the copyright holder.

Manufacturer sites such as the Canon Camera Museum are linked to, never scraped.

## Rebuilding

```bash
go run ./scrape -out . -brand canon
```

```bash
go run ./scrape -out . -brand canon -skip-images
```

Images already on disk are kept, so a rebuild only fetches what's new. A monthly GitHub Action reruns the
scraper and commits any changes.

The CSVs are generated: edits made by hand are overwritten on the next run. To correct something, fix it
in Wikidata or Wikipedia (everyone benefits), or change the scraper.

## Adding a brand

`brands` in `scrape/main.go` maps a name to its Wikidata id, e.g. Canon is `Q62621`. Add an entry and run
the scraper with `-brand`. Nikon, Pentax and Minolta should work as they are; the Wikipedia fallbacks are
Canon-specific.
