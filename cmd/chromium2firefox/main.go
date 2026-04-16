package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"chromium2firefox/internal/converter"
)

func main() {
	var (
		chromiumProfile = flag.String("chromium-profile", "", "path to the Chromium profile directory")
		firefoxProfile  = flag.String("firefox-profile", "", "path to the Firefox profile directory")
		only            = flag.String("only", "", "only import selected data: history,favicons,cookies,search")
	)
	flag.Parse()

	if *chromiumProfile == "" || *firefoxProfile == "" {
		flag.Usage()
		os.Exit(2)
	}

	options, err := converter.ParseOnly(*only)
	if err != nil {
		log.Fatalf("parse flags: %v", err)
	}

	if err := converter.ConvertProfile(context.Background(), *chromiumProfile, *firefoxProfile, options); err != nil {
		log.Fatalf("convert history: %v", err)
	}

	fmt.Println("profile conversion completed")
}
