// Package imageurl — проверка ссылок на изображения в каталоге (menu, restaurant).
// Правило контракта (imageUrl): абсолютный https-URL, не длиннее 2048 символов.
package imageurl

import (
	"errors"
	"net/url"
	"strings"
)

// MaxLen — предел длины из контракта (maxLength: 2048).
const MaxLen = 2048

// Validate проверяет необязательную ссылку: пустая строка допустима («фото нет»).
// Запрещены не-https схемы, userinfo, пробелы и управляющие символы.
func Validate(s string) error {
	if s == "" {
		return nil
	}
	if len(s) > MaxLen {
		return errors.New("imageUrl is longer than 2048 characters")
	}
	if !strings.HasPrefix(s, "https://") {
		return errors.New("imageUrl must start with https://")
	}
	for _, r := range s {
		if r <= ' ' || r == 0x7f {
			return errors.New("imageUrl must not contain spaces or control characters")
		}
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return errors.New("imageUrl is not a valid URL")
	}
	if u.User != nil {
		return errors.New("imageUrl must not contain credentials")
	}
	return nil
}
