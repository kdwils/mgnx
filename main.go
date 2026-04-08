package main

import (
	"log"

	"github.com/kdwils/mgnx/cmd"
)

func main() {
	err := cmd.Execute()
	if err != nil {
		log.Printf("Execute returned error: %v", err)
	}
	log.Println("main: exiting")
}
