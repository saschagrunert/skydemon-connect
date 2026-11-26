// Command genpng writes the app icon, the tray icon in its "sending"
// colour, as a PNG for the exe's resources: genpng <file>.
package main

import (
	"fmt"
	"image/png"
	"os"

	"github.com/saschagrunert/skydemon-connect/internal/icon"
)

const (
	// size is the largest icon Windows shows, e.g. in Explorer's large view.
	size = 256
	// args is the program's name and the file.
	args      = 2
	exitUsage = 2
)

func main() {
	if len(os.Args) != args {
		_, _ = fmt.Fprintln(os.Stderr, "usage: genpng <file.png>")

		os.Exit(exitUsage)
	}

	err := write(os.Args[1])
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "genpng:", err)

		os.Exit(1)
	}
}

func write(path string) error {
	img := icon.Render(size, icon.Green)

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}

	err = png.Encode(f, img)
	if err != nil {
		_ = f.Close()

		return fmt.Errorf("encode: %w", err)
	}

	err = f.Close()
	if err != nil {
		return fmt.Errorf("close: %w", err)
	}

	return nil
}
