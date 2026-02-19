# mdview

A portable, zero-dependency CLI tool that renders Markdown files in a browser window with live reload.

## Usage

```bash
mdview file.md              # Open a single file
mdview file1.md file2.md    # Concatenate and view multiple files
cat file.md | mdview        # Read from stdin
```

## Features

- **Live reload** — File watcher + SSE pushes reload events to the browser
- **GitHub-flavored Markdown** — Tables, task lists, strikethrough, autolinks
- **Syntax highlighting** — Fenced code blocks with language detection
- **Dark/light mode** — Respects `prefers-color-scheme`, with a toggle button
- **Clean typography** — GitHub-like CSS embedded in binary
- **Portable** — Single binary, cross-compile for macOS/Linux/Windows

## How It Works

1. Reads the Markdown file(s)
2. Converts to HTML using [goldmark](https://github.com/yuin/goldmark) with GFM extensions
3. Starts a local HTTP server on a random port
4. Opens the default browser
5. Watches the source file for changes and auto-reloads via SSE
6. Exits when the browser tab closes or on Ctrl+C

## Building

```bash
go build -o mdview .
```

## Example

Here's a table:

| Feature | Status |
|---------|--------|
| Tables | ✅ |
| Task lists | ✅ |
| Code highlighting | ✅ |
| Dark mode | ✅ |
| Live reload | ✅ |

And a task list:

- [x] Parse Markdown
- [x] Render HTML
- [x] Live reload
- [ ] World domination

```go
package main

import "fmt"

func main() {
    fmt.Println("Hello from mdview!")
}
```

> This is a blockquote to test styling.

---

*Built with Go and goldmark.*
