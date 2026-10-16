// Command devserver runs the pages+static surface standalone for manual
// curl verification (t9 acceptance) WITHOUT touching cmd/ (owned by t6).
// Usage: devserver [addr]   (default 127.0.0.1:19121)
package main

import (
	"log"
	"net/http"
	"os"

	"zhipucode2api/internal/pages"
)

func main() {
	addr := "127.0.0.1:19121"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}
	log.Printf("pages devserver listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, pages.Handler()))
}
