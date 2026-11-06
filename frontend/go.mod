// Заглушка: отделяет frontend/ от Go-модуля репозитория, чтобы `go vet ./...` не заходил в frontend/node_modules
// (там встречаются .go-файлы сторонних npm-пакетов). Go-кода во frontend/ нет.
module example.invalid/frontend-ignore

go 1.24
