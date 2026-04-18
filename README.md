# chromium2firefox(2chromium2floorp2brave2zen2ungoogled-chromium)

`chromium2firefox` is a Go utility for migrating browser data between Chromium-based browsers and Firefox-based browsers. only tested on Linux.

plz close both browsers before trying to run the tool.

for now, it can merge data in both directions:
- Chromium-based browser profile => Firefox-based browser profile
- Firefox-based browser profile => Chromium-based browser profile

that means it can do regular Chromium => zen-browser, Floorp => ungoogled-chromium, etc. haven't tested standard Chrome & Firefox though.

currently it can merge:
- History (visited sites & count)
- favicons
- cookies
- custom search engines

## Usage

```bash
go run ./cmd/chromium2firefox \
  --chromium-profile /path/to/chromium/profile \
  --firefox-profile /path/to/firefox/profile
```

By default this imports Chromium => Firefox.

To import Firefox => Chromium, add `--reverse`:

```bash
go run ./cmd/chromium2firefox \
  --reverse \
  --firefox-profile /path/to/firefox/profile \
  --chromium-profile /path/to/chromium/profile
```

Chromium profiles are expected to contain `History`. `Favicons`, `Cookies`, and `Web Data` are imported too when those files exist and are non-empty in the same profile directory.

Firefox profiles are expected to contain `places.sqlite`. `favicons.sqlite`, `cookies.sqlite`, and `search.json.mozlz4` are imported too when those files exist and are non-empty in the same profile directory.

To import only one category, use `--only`:

```bash
go run ./cmd/chromium2firefox \
  -chromium-profile /path/to/chromium/profile \
  -firefox-profile /path/to/firefox/profile \
  --only search
```

Supported `--only` values:
- `history`
- `favicons`
- `cookies`
- `search`

You can combine them with commas, for example `--only history,favicons`.

for the search engine import, it shells out to the `mozlz4` command, so [`mozlz4`](https://github.com/jusw85/mozlz4) must be installed in `PATH` if you want to import them.

## TODO

- [ ] attempt to export extensions settings (hopium)
- [ ] attempt to export leveldb-backed site storage
- [ ] support Firefox-based browser profile => Firefox-based browser profile merges too, eg. Waterfox => Floorp
- [ ] support Chromium-based browser profile => Chromium-based browser profile merges too, eg. ungoogled-chromium => Brave

## Not planned

- bookmarks since it's already doable from Firefox
