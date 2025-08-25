package amqp1jobs

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConfigInitDefault(t *testing.T) {
	tests := []struct {
		name     string
		config   config
		expected config
	}{
		{
			name:   "empty config",
			config: config{},
			expected: config{
				ExchangeType:  "direct",
				Exchange:      "",
				RedialTimeout: 60,
				Prefetch:      10,
				Priority:      10,
				Addr:          "amqp://guest:guest@127.0.0.1:5672/",
			},
		},
		{
			name: "partial config",
			config: config{
				Exchange: "custom-exchange",
				Priority: 5,
			},
			expected: config{
				ExchangeType:  "direct",
				Exchange:      "custom-exchange",
				RedialTimeout: 60,
				Prefetch:      10,
				Priority:      5,
				Addr:          "amqp://guest:guest@127.0.0.1:5672/",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.InitDefault()
			assert.NoError(t, err)

			assert.Equal(t, tt.expected.ExchangeType, tt.config.ExchangeType)
			assert.Equal(t, tt.expected.Exchange, tt.config.Exchange)
			assert.Equal(t, tt.expected.RedialTimeout, tt.config.RedialTimeout)
			assert.Equal(t, tt.expected.Prefetch, tt.config.Prefetch)
			assert.Equal(t, tt.expected.Priority, tt.config.Priority)
			assert.Equal(t, tt.expected.Addr, tt.config.Addr)
			assert.NotEmpty(t, tt.config.ContainerID)
		})
	}
}

func TestConfigEnableTLS(t *testing.T) {
	tests := []struct {
		name     string
		config   config
		expected bool
	}{
		{
			name:     "no TLS config",
			config:   config{},
			expected: false,
		},
		{
			name: "TLS with cert and key",
			config: config{
				TLS: &TLS{
					Cert: "cert.pem",
					Key:  "key.pem",
				},
			},
			expected: true,
		},
		{
			name: "TLS with cert, key and root CA",
			config: config{
				TLS: &TLS{
					RootCA: "ca.pem",
					Cert:   "cert.pem",
					Key:    "key.pem",
				},
			},
			expected: true,
		},
		{
			name: "TLS with only root CA",
			config: config{
				TLS: &TLS{
					RootCA: "ca.pem",
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.config.enableTLS()
			assert.Equal(t, tt.expected, result)
		})
	}
}
