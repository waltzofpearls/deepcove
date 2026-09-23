// Command website builds deepcove.dev into website/dist. Each page in website/pages is rendered inside
// website/templates/base.html, next to the stylesheet, the logos and the install scripts. It runs from
// the repository root.
package main

import (
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"
)

// logosFromApp come from the app, so the site never carries a second copy that drifts.
var logosFromApp = []string{"deepcove-logo-light.svg", "deepcove-logo-dark.svg", "favicon.svg"}

var installScripts = []string{"install.sh", "install.ps1"}

func main() {
	if err := build(".", filepath.Join("website", "dist")); err != nil {
		fmt.Fprintln(os.Stderr, "website:", err)
		os.Exit(1)
	}
}

func build(repoRoot, dist string) error {
	site := filepath.Join(repoRoot, "website")
	if err := os.RemoveAll(dist); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dist, "assets"), 0o755); err != nil {
		return err
	}
	if err := renderPages(site, dist); err != nil {
		return err
	}

	copies := map[string]string{
		filepath.Join(site, "_headers"):            filepath.Join(dist, "_headers"),
		filepath.Join(site, "assets", "style.css"): filepath.Join(dist, "assets", "style.css"),
	}
	for _, name := range logosFromApp {
		copies[filepath.Join(repoRoot, "ui", "static", name)] = filepath.Join(dist, "assets", name)
	}
	for _, name := range installScripts {
		copies[filepath.Join(repoRoot, name)] = filepath.Join(dist, name)
	}
	for from, to := range copies {
		if err := copyFile(from, to); err != nil {
			return err
		}
	}
	return nil
}

// renderPages writes every page inside the base template. A page defines the title, description and
// content blocks the base template asks for.
func renderPages(site, dist string) error {
	base, err := template.ParseFiles(filepath.Join(site, "templates", "base.html"))
	if err != nil {
		return err
	}
	pages, err := filepath.Glob(filepath.Join(site, "pages", "*.html"))
	if err != nil {
		return err
	}
	for _, page := range pages {
		tmpl, err := base.Clone()
		if err != nil {
			return err
		}
		if _, err := tmpl.ParseFiles(page); err != nil {
			return err
		}
		out, err := os.Create(filepath.Join(dist, filepath.Base(page)))
		if err != nil {
			return err
		}
		err = tmpl.ExecuteTemplate(out, "base.html", nil)
		if closeErr := out.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(page), err)
		}
	}
	return nil
}

func copyFile(from, to string) error {
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
