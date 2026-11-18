
build:
	go build ./cmd/anti-interrupt

what_the_make_doin:
	@printf "make get_deps:\t download required dependencies\nmake build:\t compile project\nmake run:\t It doesn't run, target the binary explicitly"

cqlsh:
	docker exec -it cass1 cqlsh

connect:
	docker exec -it cass1 cqlsh -k anti_interrupt

get_deps:
	go get github.com/scylladb/gocqlx/v3@latest
	go get github.com/spf13/cobra@latest
	go get log/slog
	go get github.com/charmbracelet/bubbletea
	go get github.com/charmbracelet/lipgloss

run:
	@echo "Make makes it difficult to pass arguments. Just run ./anti-interrupt"
