// Package migrations — SQL-миграции auth-service, встроенные в бинарь.
package migrations

import "embed"

// FS содержит файлы NNN_name.sql. Применённые миграции неизменяемы (контроль checksum):
// 001 и 002 перенесены из Java-версии без правок, их версии усыновляет pgxutil.AdoptLegacy.
//
//go:embed *.sql
var FS embed.FS
