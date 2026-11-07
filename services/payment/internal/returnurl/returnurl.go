// Package returnurl проверяет адрес возврата после оплаты по списку разрешённых origin'ов
// (защита от открытого редиректа и фишинга: ТД-QA-1, долг №91).
package returnurl

import (
	"errors"
	"net/url"
	"strings"
)

// Origin нормализует origin вида scheme://host[:port]: схема и хост в нижнем регистре,
// порт по умолчанию (80/443) отбрасывается. Путь, запрос, фрагмент и userinfo недопустимы
// (кроме одиночного «/»). Подстановочные знаки запрещены.
func Origin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "*") {
		return "", errors.New("wildcards are not allowed")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("not a valid URL")
	}
	if u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || u.Opaque != "" {
		return "", errors.New("must be an origin: scheme://host[:port] without path, query or userinfo")
	}
	return normalize(u)
}

// normalize возвращает scheme://host[:port] для http(s)-URL с непустым хостом.
func normalize(u *url.URL) (string, error) {
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("scheme must be http or https")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", errors.New("host is empty")
	}
	if strings.Contains(host, ":") { // IPv6
		host = "[" + host + "]"
	}
	port := u.Port()
	if port == "" || (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		return scheme + "://" + host, nil
	}
	return scheme + "://" + host + ":" + port, nil
}

// Allowed — множество разрешённых origin'ов.
type Allowed map[string]struct{}

// NewAllowed строит множество из origin'ов; ошибка — первый невалидный.
func NewAllowed(origins ...string) (Allowed, error) {
	a := Allowed{}
	for _, o := range origins {
		n, err := Origin(o)
		if err != nil {
			return nil, errors.New(o + ": " + err.Error())
		}
		a[n] = struct{}{}
	}
	return a, nil
}

// Check проверяет полный returnUrl: http(s), абсолютный, без userinfo (https://good@evil),
// origin входит в множество.
func (a Allowed) Check(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" {
		return errors.New("returnUrl must be an absolute http(s) URL")
	}
	if u.User != nil {
		return errors.New("returnUrl must not contain credentials")
	}
	o, err := normalize(u)
	if err != nil {
		return errors.New("returnUrl must be an absolute http(s) URL")
	}
	if _, ok := a[o]; !ok {
		return errors.New("returnUrl origin is not allowed")
	}
	return nil
}
