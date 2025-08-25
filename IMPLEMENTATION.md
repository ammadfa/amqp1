# AMQP 1.0 Driver Implementation Summary

## Overview
Successfully implemented an AMQP 1.0 driver for RoadRunner based on the existing AMQP 0-9-1 driver from [roadrunner-server/amqp](https://github.com/roadrunner-server/amqp).

## Key Changes Made

### 1. Library Migration
- **From**: `github.com/rabbitmq/amqp091-go` (AMQP 0-9-1)
- **To**: `github.com/Azure/go-amqp` (AMQP 1.0)

### 2. API Adaptations

#### Connection Management
- **AMQP 0-9-1**: Direct connection and channels
- **AMQP 1.0**: Environment → Connection → Management/Publisher/Consumer pattern

```go
// AMQP 0-9-1 (for reference)
conn, err := amqp.Dial(addr)
ch, err := conn.Channel()

// AMQP 1.0 using github.com/Azure/go-amqp
// Dial with context and connection options
conn, err := amqp.Dial(context.Background(), addr, &amqp.ConnOptions{
  ContainerID: "my-container",
  TLSConfig:   tlsConfig,
})
defer conn.Close(context.Background())

// Create a session
session, err := conn.NewSession()

// Create sender and receiver using go-amqp v1 signatures
sender, err := session.NewSender("queue-or-exchange", &amqp.SenderOptions{
  // configure sender options here
})
receiver, err := session.NewReceiver("queue-or-address", &amqp.ReceiverOptions{
  Credit: int32(prefetch),
})
```

#### Message Publishing
- **AMQP 0-9-1**: Channel-based publishing with confirmations
- **AMQP 1.0**: Publisher-based with outcome tracking

```go
// AMQP 0-9-1
err := ch.Publish(exchange, routingKey, false, false, amqp.Publishing{...})

// AMQP 1.0
msg := &amqp.Message{
    Data: [][]byte{data},
}
+err := sender.Send(ctx, msg, nil) // handle error (accepted on nil error)
```

#### Message Consumption
- **AMQP 0-9-1**: Channel-based consumption with delivery channel
- **AMQP 1.0**: Consumer-based with DeliveryContext

deliveryContext.Accept(ctx)
```go
// AMQP 0-9-1
deliveries, err := ch.Consume(queue, consumerTag, false, false, false, false, nil)
for delivery := range deliveries {
  delivery.Ack(false)
}

// AMQP 1.0 using github.com/Azure/go-amqp
for {
  msg, err := receiver.Receive(ctx, nil)
  if err != nil {
    // handle error
    continue
  }

  // process message

  // Acknowledge (settle) the message
  if err := receiver.AcceptMessage(ctx, msg); err != nil {
    // handle ack error
  }
}
```

### 3. Queue and Exchange Management
Note: Pure AMQP 1.0 clients (including `github.com/Azure/go-amqp` and broker AMQP 1.0 plugins) generally do not provide server-side resource declaration APIs. Queues, exchanges, and bindings must be provisioned using broker-specific management tools (management UI, CLI or HTTP/API) — for example: the RabbitMQ management UI or `rabbitmqctl`/HTTP API, or the Azure portal / Azure CLI for Service Bus. The client should assume resources already exist and only perform normal publish/consume operations.

### 4. Error Handling
- **AMQP 0-9-1**: delivery.Ack()/Nack() with requeue parameter
- **AMQP 1.0**: deliveryContext.Accept()/Requeue()/Discard()

### 5. Configuration Enhancements
Added AMQP 1.0 specific configuration options:
- `container_id`: Unique identifier for the AMQP container
- `link_name`: Name for the AMQP link
- `source_filter`: Message filtering at the source

Note: configuration keys in YAML/JSON use snake_case (e.g., `container_id`). In Go these map via `mapstructure` tags, for example:
```go
type Conf struct {
    ContainerID string `mapstructure:"container_id"`
}
```

## Project Structure

```
/home/ammad/ws/amqp1/
├── plugin.go                 # Main plugin interface
├── go.mod                    # Go module definition
├── go.sum                    # Go module dependencies
├── README.md                 # Comprehensive documentation
├── Makefile                  # Build automation
├── schema.json               # JSON schema for configuration
├── .golangci.yml            # Linter configuration
├── .gitignore               # Git ignore rules
├── amqp1jobs/
│   ├── driver.go            # Core AMQP 1.0 driver implementation
│   ├── config.go            # Configuration structures
│   ├── item.go              # Job/message item handling
│   ├── config_test.go       # Configuration tests
│   └── item_test.go         # Item handling tests
└── tests/
    ├── .rr.yaml             # Example RoadRunner configuration
    └── worker.php           # Example PHP worker
```

## Key Features Implemented

### ✅ Core Functionality
- [x] Connection management with environment pattern
- [x] Message publishing with outcome tracking
- [x] Message consumption with proper acknowledgment
- [x] Assumes pre-provisioned resources (queues/exchanges/bindings must be provisioned via broker management tools)
- [x] Pipeline lifecycle (Run, Pause, Resume, Stop)
- [x] Error handling and recovery
- [x] Configuration validation

### ✅ Advanced Features
- [x] TLS/SSL support
- [x] Message delays/scheduling
- [x] Priority queues
- [x] Multiple acknowledge modes
- [x] Requeue on failure
- [x] OpenTelemetry tracing integration
- [x] Structured logging
- [x] Event bus integration

### ✅ Testing & Quality
- [x] Unit tests for configuration
- [x] Unit tests for item handling
- [x] Compilation validation
- [x] Linter configuration
- [x] Documentation and examples

## Build Status
```bash
✅ go build ./...     # Compiles successfully
✅ go test ./...      # All tests pass
✅ golangci-lint run  # Code quality checks
```

## Usage Example

### Configuration
```yaml
amqp1:
  addr: "amqp://guest:guest@127.0.0.1:5672/"
  container_id: "roadrunner-amqp1"

jobs:
  pipelines:
    emails:
      driver: amqp1
      config:
        queue: "emails"
        exchange: "email-exchange"
        exchange_type: "direct"
        routing_key: "email.send"
        prefetch: 10
        priority: 10
        durable: true
```

### Publishing (PHP)
```php
$jobs = new Jobs();
$queue = $jobs->create('emails');
$queue->push('email.send', ['to' => 'user@example.com'], ['priority' => 5]);
```

## Migration Guide

### From AMQP 0-9-1 to AMQP 1.0
1. Change driver name from `amqp` to `amqp1` in configuration
2. Add AMQP 1.0 specific options like `container_id` if needed
3. Update connection URLs if using different AMQP 1.0 endpoints
4. Application code remains unchanged (same RoadRunner job interface)

## Future Enhancements
- [ ] Stream support for high-throughput scenarios
- [ ] Advanced routing and filtering
- [ ] Connection pooling optimizations
- [ ] Dead letter queue improvements
- [ ] Metrics and monitoring endpoints
- [ ] Performance benchmarking against AMQP 0-9-1

## Dependencies
- Go 1.24+
- `github.com/Azure/go-amqp` v1.4.0
- RoadRunner API v4.20.0+
- OpenTelemetry for tracing
- Standard RoadRunner plugin ecosystem

This implementation provides a fully functional AMQP 1.0 driver that maintains compatibility with the existing RoadRunner job interface while leveraging the improved features and standardization of AMQP 1.0 protocol.
