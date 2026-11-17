
default:
	@printf "make get_deps:\t download required dependencies\nmake build:\t compile project\nmake run:\t It doesn't run, target the binary explicitly"

get_deps:
	go get github.com/scylladb/gocqlx
	go get github.com/spf13/cobra@latest
	go get log/slog
	go get github.com/charmbracelet/bubbletea
	go get github.com/charmbracelet/lipgloss

build:
	go build ./cmd/anti-interrupt

run:
	@echo "Make makes it difficult to pass arguments. Just run ./anti-interrupt"
