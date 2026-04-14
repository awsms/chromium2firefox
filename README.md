# chromium2firefox

`chromium2firefox` is a Go utility for migrating browser data from Chromium-based browsers into Firefox profiles.
plz close both browsers before running the importer.

for now, it can export Chromium's `History` database into Firefox `places.sqlite`, merging:
- URLs
- visits
- titles
- typed/hidden flags
- origin rows

## Usage

```bash
go run ./cmd/chromium2firefox \
  -chromium-history /path/to/History \
  -chromium-favicons /path/to/Favicons \
  -firefox-profile /path/to/firefox/profile
```

## TODO

- [ ] export custom search keywords (stuff in `Web Data` => `search.json.mozlz4`)
- [ ] attempt to export extensions settings (hopium)
- [ ] attempt to export cookies/sites data

## Not planned

- bookmarks since it's already doable from Firefox