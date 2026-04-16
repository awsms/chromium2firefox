# chromium2firefox

`chromium2firefox` is a Go utility for migrating browser data from Chromium-based browsers into Firefox profiles. only tested on Linux.

plz close both browsers before trying to run the tool.

for now, it can export Chromium data into Firefox by merging:
- URLs
- visits
- titles
- typed/hidden flags
- origin rows
- favicons
- cookies
- search engines from `Web Data` into `search.json.mozlz4`

## Usage

```bash
go run ./cmd/chromium2firefox \
  -chromium-profile /path/to/chromium/profile \
  -firefox-profile /path/to/firefox/profile
```

Chromium profile is expected to contain `History`. `Favicons`, `Cookies`, and `Web Data` are imported too when those files exist and are non-empty in the same profile directory.

To import only one category, use `-only`:

```bash
go run ./cmd/chromium2firefox \
  -chromium-profile /path/to/chromium/profile \
  -firefox-profile /path/to/firefox/profile \
  -only search
```

Supported `-only` values:
- `history`
- `favicons`
- `cookies`
- `search`

You can combine them with commas, for example `-only history,favicons`.

search engine import is conservative for the first pass:
- it reads Chromium engines from the `keywords` table
- it only imports active engines with `{searchTerms}` in the search URL
- it skips POST-based engines for now
- it avoids clobbering Firefox engines with the same persisted name or id
- it shells out to the `mozlz4` command, so [`mozlz4`](https://github.com/jusw85/mozlz4) must be installed in `PATH`

## TODO

- [ ] attempt to export extensions settings (hopium)
- [ ] attempt to export leveldb-backed site storage

## Not planned

- bookmarks since it's already doable from Firefox
