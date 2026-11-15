//go:build ignore

// Packages one plugin library as <archive>.zip (library at the zip root) and writes
// "<sha256>  <archive>" to the checksum file, the format CPA's plugin store verifies.
package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"io"
	"log"
	"os"
	"path/filepath"
)

func main() {
	lib := flag.String("library", "", "shared library path")
	archive := flag.String("archive", "", "zip to write")
	checksum := flag.String("checksum", "", "checksum file to write")
	flag.Parse()
	if *lib == "" || *archive == "" || *checksum == "" {
		log.Fatal("-library, -archive and -checksum are required")
	}
	out, err := os.Create(*archive)
	if err != nil {
		log.Fatal(err)
	}
	zw := zip.NewWriter(out)
	in, err := os.Open(*lib)
	if err != nil {
		log.Fatal(err)
	}
	st, _ := in.Stat()
	hdr, _ := zip.FileInfoHeader(st)
	hdr.Name = filepath.Base(*lib)
	hdr.Method = zip.Deflate
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := io.Copy(w, in); err != nil {
		log.Fatal(err)
	}
	in.Close()
	if err := zw.Close(); err != nil {
		log.Fatal(err)
	}
	out.Close()
	data, err := os.ReadFile(*archive)
	if err != nil {
		log.Fatal(err)
	}
	sum := sha256.Sum256(data)
	line := hex.EncodeToString(sum[:]) + "  " + filepath.Base(*archive) + "\n"
	if err := os.WriteFile(*checksum, []byte(line), 0o644); err != nil {
		log.Fatal(err)
	}
}
