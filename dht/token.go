package dht

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"net"
	"sync"
	"time"
)

// TokenManager issues and validates announce_peer tokens using a
// double-rotating secret scheme per BEP-05.
//
// A token is SHA1(ip || secret)[:4]. Two secrets are kept (current and
// previous) so tokens issued just before a rotation remain valid for one
// additional window.
type TokenManager struct {
	mu             sync.RWMutex
	currentSecret  [8]byte
	previousSecret [8]byte
	rotation       time.Duration
}

// NewTokenManager creates a TokenManager with two freshly-generated secrets.
// rotation is how often the secret cycles (BEP-05 default: 5 minutes).
func NewTokenManager(rotation time.Duration) *TokenManager {
	return &TokenManager{
		currentSecret:  newSecret(),
		previousSecret: newSecret(),
		rotation:       rotation,
	}
}

// Start launches the background rotation goroutine. It runs until ctx is cancelled.
func (m *TokenManager) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(m.rotation)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m.rotate()
			case <-ctx.Done():
				return
			}
		}
	}()
}

// Generate returns a 4-byte token for ip using the current secret.
func (m *TokenManager) Generate(ip net.IP) string {
	m.mu.RLock()
	secret := m.currentSecret
	m.mu.RUnlock()
	return tokenFor(ip, secret)
}

// Validate reports whether token is valid for ip against either the current
// or previous secret. Uses constant-time comparison to prevent timing attacks.
func (m *TokenManager) Validate(ip net.IP, token string) bool {
	m.mu.RLock()
	current := m.currentSecret
	previous := m.previousSecret
	m.mu.RUnlock()
	currentToken := tokenFor(ip, current)
	prevToken := tokenFor(ip, previous)
	return subtle.ConstantTimeCompare([]byte(token), []byte(currentToken)) == 1 ||
		subtle.ConstantTimeCompare([]byte(token), []byte(prevToken)) == 1
}

// rotate advances the secret window: previous = current, current = new random.
func (m *TokenManager) rotate() {
	next := newSecret()
	m.mu.Lock()
	m.previousSecret = m.currentSecret
	m.currentSecret = next
	m.mu.Unlock()
}

// tokenFor computes SHA1(ip || secret)[:4] as a raw byte string.
func tokenFor(ip net.IP, secret [8]byte) string {
	v4 := ip.To4()
	if v4 == nil {
		v4 = []byte(ip)
	}
	h := sha1.New()
	h.Write(v4)
	h.Write(secret[:])
	return string(h.Sum(nil)[:4])
}

// newSecret returns 8 cryptographically random bytes.
func newSecret() [8]byte {
	var s [8]byte
	rand.Read(s[:])
	return s
}
