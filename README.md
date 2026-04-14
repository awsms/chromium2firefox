# chromium2firefox

`chromium2firefox` is a Go utility for migrating browser data from Chromium-based browsers into Firefox profiles.
plz close both browsers before running the importer.

for now, it can export Chromium data into Firefox by merging:
- URLs
- visits
- titles
- typed/hidden flags
- origin rows
- favicons
- search engines from `Web Data` into `search.json.mozlz4`

## Usage

```bash
go run ./cmd/chromium2firefox \
  -chromium-history /path/to/History \
  -chromium-favicons /path/to/Favicons \
  -chromium-web-data /path/to/Web\ Data \
  -firefox-profile /path/to/firefox/profile
```

if `-chromium-favicons` or `-chromium-web-data` are omitted, the tool will try sibling `Favicons` and `Web Data` files next to the `History` file when they exist and are non-empty.

search engine import is conservative for the first pass:
- it reads Chromium engines from the `keywords` table
- it only imports active engines with `{searchTerms}` in the search URL
- it skips POST-based engines for now
- it avoids clobbering Firefox engines with the same persisted name or id
- it shells out to the `mozlz4` command, so [`mozlz4`](https://github.com/jusw85/mozlz4) must be installed in `PATH`

## TODO

- [ ] attempt to export extensions settings (hopium)
- [ ] attempt to export cookies/sites data

## Not planned

- bookmarks since it's already doable from Firefox
