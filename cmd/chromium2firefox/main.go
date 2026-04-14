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
		chromiumHistory  = flag.String("chromium-history", "", "path to the Chromium History SQLite database")
		chromiumFavicons = flag.String("chromium-favicons", "", "path to the Chromium Favicons SQLite database")
		chromiumWebData  = flag.String("chromium-web-data", "", "path to the Chromium Web Data SQLite database")
		firefoxProfile   = flag.String("firefox-profile", "", "path to the Firefox profile directory")
	)
	flag.Parse()

	if *chromiumHistory == "" || *firefoxProfile == "" {
		flag.Usage()
		os.Exit(2)
	}

	if err := converter.ConvertHistory(context.Background(), *chromiumHistory, *chromiumFavicons, *chromiumWebData, *firefoxProfile); err != nil {
		log.Fatalf("convert history: %v", err)
	}

	fmt.Println("history conversion completed")
}
