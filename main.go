package main

import (
	"log"

	"github.com/yxxbc/haruhikage-app/internal/app"
)

func main() {
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
