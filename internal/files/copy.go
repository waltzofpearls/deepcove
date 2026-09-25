// Package files holds small file helpers shared by the build commands.
package files

import (
	"io"
	"os"
)

// Copy writes the contents of the file at from to a new file at to, replacing one already there.
func Copy(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
