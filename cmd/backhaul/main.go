package main

import (
	"flag"
	"fmt"
	"os"

	"open-backhaul/internal/app"
)

func main() {
	configPath := flag.String("c", "", "path to config toml")
	showVersion := flag.Bool("v", false, "print version")
	flag.Parse()

	if *showVersion {
		fmt.Println("backhaul_recovered", app.Version)
		return
	}
	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "usage: backhaul_recovered -c <config.toml>")
		os.Exit(2)
	}
	if err := app.Run(*configPath); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
