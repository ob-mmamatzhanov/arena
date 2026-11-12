// Package cors — CORS для HTTP-ручек с той же проверкой Origin, что у
// WebSocket (nhooyr.io/websocket): разрешены собственный хост сервера и
// хосты, подходящие под шаблоны из -origins.
package cors

import (
	"net/http"
	"net/url"
	"path"
	"strings"
)

// Allow выставляет Access-Control-Allow-Origin, если Origin запроса разрешён,
// и Vary: Origin в любом случае. Возвращает true, если Origin разрешён или
// его нет (запрос не из браузера / same-origin без заголовка).
func Allow(w http.ResponseWriter, r *http.Request, patterns []string) bool {
	w.Header().Add("Vary", "Origin")
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	if !Allowed(o, r.Host, patterns) {
		return false
	}
	w.Header().Set("Access-Control-Allow-Origin", o)
	return true
}

// Allowed повторяет проверку Origin в nhooyr.io/websocket: хост из Origin
// совпадает с хостом запроса или подходит под один из шаблонов (path.Match
// без учёта регистра, например "localhost:*").
func Allowed(origin, host string, patterns []string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, host) {
		return true
	}
	h := strings.ToLower(u.Host)
	for _, p := range patterns {
		if ok, err := path.Match(strings.ToLower(p), h); err == nil && ok {
			return true
		}
	}
	return false
}
