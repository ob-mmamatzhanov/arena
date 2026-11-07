package room

import "time"

// SetJoinTimeout подменяет таймаут ожидания join на время теста.
func SetJoinTimeout(d time.Duration) (restore func()) {
	old := joinTimeout
	joinTimeout = d
	return func() { joinTimeout = old }
}
