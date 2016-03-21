package main

import (
	"os"
	"os/signal"
	"testing"

	"github.com/PuerkitoBio/juggler/internal/redistest"
)

func main() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)

	t := &testing.T{}
	fn, _ := redistest.StartCluster(t, os.Stdout)
	<-c
	fn()
}
