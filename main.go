package main

import (
	"log"

	"github.com/yxxbc/haruhikage/internal/app"
)

func main() {
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
