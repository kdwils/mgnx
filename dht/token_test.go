package dht

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTokenManager_Generate(t *testing.T) {
	t.Run("returns 4-byte token", func(t *testing.T) {
		m := NewTokenManager(0)
		token := m.Generate(net.ParseIP("1.2.3.4"))
		assert.Len(t, token, 4)
	})

	t.Run("same ip produces same token before rotation", func(t *testing.T) {
		m := NewTokenManager(0)
		ip := net.ParseIP("1.2.3.4")
		assert.Equal(t, m.Generate(ip), m.Generate(ip))
	})

	t.Run("different ips produce different tokens", func(t *testing.T) {
		m := NewTokenManager(0)
		a := m.Generate(net.ParseIP("1.2.3.4"))
		b := m.Generate(net.ParseIP("5.6.7.8"))
		assert.NotEqual(t, a, b)
	})
}

func TestTokenManager_Validate(t *testing.T) {
	t.Run("token generated with current secret is valid", func(t *testing.T) {
		m := NewTokenManager(0)
		ip := net.ParseIP("1.2.3.4")
		token := m.Generate(ip)
		assert.True(t, m.Validate(ip, token))
	})

	t.Run("wrong token is invalid", func(t *testing.T) {
		m := NewTokenManager(0)
		ip := net.ParseIP("1.2.3.4")
		assert.False(t, m.Validate(ip, "bad!"))
	})

	t.Run("token for different ip is invalid", func(t *testing.T) {
		m := NewTokenManager(0)
		token := m.Generate(net.ParseIP("1.2.3.4"))
		assert.False(t, m.Validate(net.ParseIP("5.6.7.8"), token))
	})
}

func TestTokenManager_Rotate(t *testing.T) {
	t.Run("token from before rotation is still valid via previous secret", func(t *testing.T) {
		m := NewTokenManager(0)
		ip := net.ParseIP("1.2.3.4")
		oldToken := m.Generate(ip)

		m.rotate()

		assert.True(t, m.Validate(ip, oldToken))
	})

	t.Run("token from two rotations ago is no longer valid", func(t *testing.T) {
		m := NewTokenManager(0)
		ip := net.ParseIP("1.2.3.4")
		oldToken := m.Generate(ip)

		m.rotate()
		m.rotate()

		assert.False(t, m.Validate(ip, oldToken))
	})

	t.Run("new token is valid after rotation", func(t *testing.T) {
		m := NewTokenManager(0)
		ip := net.ParseIP("1.2.3.4")

		m.rotate()

		newToken := m.Generate(ip)
		assert.True(t, m.Validate(ip, newToken))
	})
}
