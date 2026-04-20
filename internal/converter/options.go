package converter

import (
	"fmt"
	"strings"
)

type Options struct {
	History    bool
	Favicons   bool
	Cookies    bool
	Search     bool
	Extensions bool
	Merge      bool
}

func DefaultOptions() Options {
	return Options{
		History:    true,
		Favicons:   true,
		Cookies:    true,
		Search:     true,
		Extensions: true,
		Merge:      true,
	}
}

func ParseOnly(value string) (Options, error) {
	if strings.TrimSpace(value) == "" {
		return DefaultOptions(), nil
	}

	options := Options{Merge: true}
	for _, part := range strings.Split(value, ",") {
		switch strings.TrimSpace(strings.ToLower(part)) {
		case "history":
			options.History = true
		case "favicons", "favicon":
			options.Favicons = true
		case "cookies", "cookie":
			options.Cookies = true
		case "search", "search-engines", "engines":
			options.Search = true
		case "extensions", "extension":
			options.Extensions = true
		case "":
			continue
		default:
			return Options{}, fmt.Errorf("unsupported -only value %q", strings.TrimSpace(part))
		}
	}

	if !options.History && !options.Favicons && !options.Cookies && !options.Search && !options.Extensions {
		return Options{}, fmt.Errorf("no import targets selected")
	}
	return options, nil
}
