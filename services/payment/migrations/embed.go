// Package migrations — SQL-миграции payment-service, встроенные в бинарь.
package migrations

import "embed"

// FS содержит файлы NNN_name.sql. Применённые миграции неизменяемы (контроль checksum).
//
//go:embed *.sql
var FS embed.FS
