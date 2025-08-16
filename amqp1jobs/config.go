package amqp1jobs

import (
	"crypto/tls"
	"fmt"
	"os"

	"github.com/google/uuid"
	"github.com/roadrunner-server/errors"
)

// pipeline amqp1 info
const (
	exchangeKey   string = "exchange"
	exchangeType  string = "exchange_type"
	queue         string = "queue"
	routingKey    string = "routing_key"
	prefetch      string = "prefetch"
	exclusive     string = "exclusive"
	durable       string = "durable"
	deleteOnStop  string = "delete_queue_on_stop"
	priority      string = "priority"
	multipleAck   string = "multiple_ack"
	requeueOnFail string = "requeue_on_fail"

	// new in 2.12
	redialTimeout      string = "redial_timeout"
	exchangeDurable    string = "exchange_durable"
	exchangeAutoDelete string = "exchange_auto_delete"
	queueAutoDelete    string = "queue_auto_delete"

	// new in 2.12.2
	queueHeaders string = "queue_headers"
)

// config is used to parse pipeline configuration
type config struct {
	// global - AMQP 1.0 connection URL
	Addr string `mapstructure:"addr"`

	// global TLS option
	TLS *TLS `mapstructure:"tls"`

	// SASL authentication for Azure Service Bus and other AMQP 1.0 brokers
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
	SASLType string `mapstructure:"sasl_type"`

	// local
	Prefetch     int    `mapstructure:"prefetch"`
	Queue        string `mapstructure:"queue"`
	Priority     int64  `mapstructure:"priority"`
	Exchange     string `mapstructure:"exchange"`
	ExchangeType string `mapstructure:"exchange_type"`

	RoutingKey        string `mapstructure:"routing_key"`
	Exclusive         bool   `mapstructure:"exclusive"`
	Durable           bool   `mapstructure:"durable"`
	DeleteQueueOnStop bool   `mapstructure:"delete_queue_on_stop"`
	MultipleAck       bool   `mapstructure:"multiple_ack"`
	RequeueOnFail     bool   `mapstructure:"requeue_on_fail"`

	// new in 2.12.1
	ExchangeDurable    bool `mapstructure:"exchange_durable"`
	ExchangeAutoDelete bool `mapstructure:"exchange_auto_delete"`
	QueueAutoDelete    bool `mapstructure:"queue_auto_delete"`
	RedialTimeout      int  `mapstructure:"redial_timeout"`

	// new in 2.12.2
	QueueHeaders map[string]any `mapstructure:"queue_headers"`
	// new in 2023.1.0
	ConsumerID string `mapstructure:"consumer_id"`

	// AMQP 1.0 specific options
	ContainerID  string `mapstructure:"container_id"`
	LinkName     string `mapstructure:"link_name"`
	SourceFilter string `mapstructure:"source_filter"`
}

// TLS configuration
type TLS struct {
	RootCA             string          `mapstructure:"root_ca"`
	Key                string          `mapstructure:"key"`
	Cert               string          `mapstructure:"cert"`
	InsecureSkipVerify bool            `mapstructure:"insecure_skip_verify"`
	// auth type internal
	auth tls.ClientAuthType
}

func (c *config) InitDefault() error {
	const op = errors.Op("amqp1_init_default")
	// all options should be in sync with the pipeline defaults in the ConsumerFromPipeline method
	if c.ExchangeType == "" {
		c.ExchangeType = "direct"
	}

	// Leave exchange empty for default exchange - don't set a default value

	if c.RedialTimeout == 0 {
		c.RedialTimeout = 60
	}

	if c.Prefetch == 0 {
		c.Prefetch = 10
	}

	if c.Priority == 0 {
		c.Priority = 10
	}

	if c.Addr == "" {
		c.Addr = "amqp://guest:guest@127.0.0.1:5672/"
	}

	if c.ConsumerID == "" {
		c.ConsumerID = fmt.Sprintf("roadrunner-%s", uuid.NewString())
	}

	if c.ContainerID == "" {
		c.ContainerID = fmt.Sprintf("roadrunner-amqp1-%s", uuid.NewString())
	}

	if c.LinkName == "" {
		c.LinkName = fmt.Sprintf("amqp1-link-%s", uuid.NewString())
	}

	if c.enableTLS() {
		if _, err := os.Stat(c.TLS.Key); err != nil {
			if os.IsNotExist(err) {
				return errors.E(op, errors.Errorf("key file '%s' does not exists", c.TLS.Key))
			}

			return errors.E(op, err)
		}

		if _, err := os.Stat(c.TLS.Cert); err != nil {
			if os.IsNotExist(err) {
				return errors.E(op, errors.Errorf("cert file '%s' does not exists", c.TLS.Cert))
			}

			return errors.E(op, err)
		}

		// RootCA is optional, but if provided - check it
		if c.TLS.RootCA != "" {
			if _, err := os.Stat(c.TLS.RootCA); err != nil {
				if os.IsNotExist(err) {
					return errors.E(op, errors.Errorf("root ca path provided, but key file '%s' does not exists", c.TLS.RootCA))
				}
				return errors.E(op, err)
			}

			// auth type used only for the CA
			c.TLS.auth = tls.NoClientCert
		}
	}

	return nil
}

func (c *config) enableTLS() bool {
	if c.TLS != nil {
		return (c.TLS.RootCA != "" && c.TLS.Key != "" && c.TLS.Cert != "") || (c.TLS.Key != "" && c.TLS.Cert != "")
	}
	return false
}
