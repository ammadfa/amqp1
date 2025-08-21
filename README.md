# AMQP 1.0 Driver for RoadRunner

This is an AMQP 1.0 driver implementation for RoadRunner that provides unified support for both **Azure Service Bus** and **RabbitMQ** using the pure `github.com/Azure/go-amqp` library.

## Overview

This driver provides AMQP 1.0 connectivity for RoadRunner's job queue system with dual broker support:

### Supported Brokers
- **Azure Service Bus**: Native AMQP 1.0 cloud messaging service
- **RabbitMQ**: With AMQP 1.0 plugin enabled

### Features
- Pure AMQP 1.0 protocol implementation using Azure go-amqp
- Automatic broker detection (Azure Service Bus vs RabbitMQ)
- Publisher and consumer capabilities
- TLS/SSL encryption support (automatic for Azure Service Bus)
- Connection resilience and retry mechanisms
- Distributed tracing integration
- Event-driven architecture

## Key Features

### Pure AMQP 1.0 Implementation
- **Library**: `github.com/Azure/go-amqp` v1.4.0 - Pure AMQP 1.0 client
- **Protocol**: Standardized AMQP 1.0 with better interoperability
- **Connection Model**: Container-based connections with sessions and links
- **Message Format**: Structured AMQP 1.0 message format with application properties

### Broker-Agnostic Design
- **Automatic Detection**: Identifies Azure Service Bus vs RabbitMQ automatically
- **Unified Configuration**: Same configuration format works with both brokers
- **Protocol Optimization**: Adapts message routing based on broker capabilities

### Migration Benefits
- **From**: `github.com/rabbitmq/rabbitmq-amqp-go-client` (RabbitMQ-specific)
- **To**: `github.com/Azure/go-amqp` (Pure AMQP 1.0, works with any AMQP 1.0 broker)
- **Compatibility**: Works with Azure Service Bus and RabbitMQ using the same codebase

## Configuration

### Azure Service Bus Configuration

```yaml
# Azure Service Bus with TLS (production)
amqp1:
  addr: "amqps://RootManageSharedAccessKey:YOUR_ACCESS_KEY@YOUR_NAMESPACE.servicebus.windows.net:5671/"
  container_id: "roadrunner-jobs-azure"

jobs:
  consume: ["azure-queue"]

  pipelines:
    azure-queue:
      driver: amqp1
      config:
        queue: "your-queue-name"              # Must exist in Azure Service Bus
        routing_key: "your-queue-name"        # For compatibility
        exchange: ""                          # Not used in Azure Service Bus
        exchange_type: "direct"               # Keep for compatibility
        prefetch: 10
        priority: 1
        durable: false
        exclusive: false
        multiple_ack: false
        requeue_on_fail: false
```

**Azure Service Bus Requirements:**
- Queue must be pre-created in Azure portal or via Azure CLI
- Uses Shared Access Key authentication
- TLS is automatically enabled with `amqps://` protocol
- Routing occurs directly to queue (no exchanges)

### RabbitMQ Configuration

```yaml
# RabbitMQ with AMQP 1.0 plugin
amqp1:
  addr: "amqp://username:password@rabbitmq:5672/"
  container_id: "roadrunner-jobs-rabbitmq"

jobs:
  consume: ["rabbit-queue"]

  pipelines:
    rabbit-queue:
      driver: amqp1
      config:
        queue: "test-queue"
        routing_key: "test"
        exchange: ""                          # Use default exchange
        exchange_type: "direct"
        prefetch: 10
        priority: 10
        durable: true
        exclusive: false
        multiple_ack: false
        requeue_on_fail: true
```

**RabbitMQ Requirements:**
- Enable AMQP 1.0 plugin: `rabbitmq-plugins enable rabbitmq_amqp1_0`
- Queues and exchanges must be created ahead of time (AMQP 1.0 client does not declare them)
- Supports exchange-based routing; ensure bindings are configured server-side
### TLS Configuration

amqp1:
  addr: "amqps://guest:guest@127.0.0.1:5671/"
  tls:
    cert: "/path/to/cert.pem"
    key: "/path/to/key.pem"
    root_ca: "/path/to/ca.pem"
    insecure_skip_verify: false

### Advanced Pipeline Configuration

```yaml
# Advanced configuration with broker-specific optimizations
jobs:
  pipelines:
    advanced-azure:
      driver: amqp1
      config:
        queue: "priority-orders"
        routing_key: "priority-orders"
        prefetch: 50
        priority: 5

        # Azure Service Bus specific headers
        queue_headers:
          max-delivery-count: 10
          default-message-ttl: 3600000

    advanced-rabbit:
      driver: amqp1
      config:
        queue: "advanced-queue"
        exchange: "advanced-exchange"
        exchange_type: "topic"
        routing_key: "events.#"
        prefetch: 50
        priority: 5
        durable: true
        exclusive: false
        multiple_ack: false
        requeue_on_fail: true

        # RabbitMQ specific settings
        exchange_durable: true
        exchange_auto_delete: false
        queue_auto_delete: false
        queue_headers:
          x-max-length: 1000
          x-message-ttl: 3600000
```

## Implementation Details

### Driver Architecture

The driver consists of several key components:

1. **Plugin** (`plugin.go`): Main plugin interface and registration
2. **Driver** (`amqp1jobs/driver.go`): Core driver implementation with pure AMQP 1.0 support
3. **Config** (`amqp1jobs/config.go`): Configuration structure and validation
4. **Item** (`amqp1jobs/item.go`): Message/job item handling and serialization

### Pure AMQP 1.0 Implementation

#### Connection Management
```go
// Create AMQP 1.0 connection using pure Azure go-amqp library
conn, err := amqp.NewConnection(addr, &amqp.ConnectionOptions{
    ContainerID: conf.ContainerID,
    TLSConfig:   tlsConfig,
})

// Unified session for both Azure Service Bus and RabbitMQ
session, err := conn.NewSession()
defer session.Close()
```

#### Broker Detection and Adaptation
```go
// Automatic broker detection based on connection properties
if d.isAzureServiceBus() {
    // Azure Service Bus: Direct queue communication
    receiver, err := session.NewReceiver(amqp.LinkAddress(queueName))
    sender, err := session.NewSender(amqp.LinkAddress(queueName))
} else {
    // RabbitMQ: Traditional exchange/routing
    receiver, err := session.NewReceiver(amqp.LinkAddress(queueName))
    sender, err := session.NewSender(amqp.LinkAddress(""))
}
```

#### Message Publishing
```go
// Unified message publishing for both brokers
amqpMsg := &amqp.Message{
    Data:                  [][]byte{msg.Body()},
    ApplicationProperties: convertToAMQP1Headers(msg.headers),
}

if d.isAzureServiceBus() {
    // Direct to queue
    err := sender.Send(ctx, amqpMsg, nil)
} else {
    // Through exchange with routing key
    amqpMsg.Properties = &amqp.MessageProperties{
        Subject: &routingKey,  // RabbitMQ routing key
    }
    err := sender.Send(ctx, amqpMsg, nil)
}
```

#### Message Consumption
```go
// Unified consumption pattern
receiver, err := session.NewReceiver(amqp.LinkTargetAddress(queueName), &amqp.ReceiverOptions{
    Credit: int32(prefetch),
    Manual: true,  // Manual acknowledgment
})

for {
    msg, err := receiver.Receive(ctx)
    if err != nil {
        continue
    }

    // Process message
    jobItem := convertFromAMQP1Message(msg)

    // Acknowledge based on processing result
    if processSuccess {
        receiver.AcceptMessage(ctx, msg)
    } else if requeue {
        receiver.RejectMessage(ctx, msg, nil)
    } else {
        receiver.ReleaseMessage(ctx, msg)
    }
}
```

### Message Flow

1. **Publishing**: Jobs are converted to AMQP 1.0 messages with unified format
2. **Broker Detection**: Automatic detection determines routing strategy
3. **Routing**:
   - Azure Service Bus: Direct queue delivery
   - RabbitMQ: Exchange-based routing with keys
4. **Consumption**: Credit-based flow control for both brokers
5. **Processing**: Unified acknowledgment handling

### Error Handling

The driver implements comprehensive error handling:

- **Connection Resilience**: Automatic reconnection with exponential backoff
- **Message Processing**: Configurable requeue/reject behavior via `requeue_on_fail`
- **TLS Validation**: Certificate validation for secure connections
- **Resource Cleanup**: Graceful shutdown of sessions and connections
- **Broker Compatibility**: Fallback mechanisms for broker-specific features

### Observability

- **Tracing**: OpenTelemetry integration for distributed tracing
- **Logging**: Structured logging with zap logger
- **Events**: Event bus integration for monitoring driver state changes
- **Metrics**: Pipeline state reporting (active, delayed, ready jobs)
- **Health Checks**: Connection status and queue availability monitoring

## Usage Examples

### Publishing Jobs to Azure Service Bus

```php
<?php
use Spiral\RoadRunner\Jobs\Jobs;
use Spiral\RoadRunner\Jobs\Queue;

$jobs = new Jobs();
$queue = $jobs->create('azure-queue');  // Pipeline name from config

$queue->push('ProcessOrder', ['orderId' => 12345], [
    'priority' => 5,
    'delay' => 30,
    'headers' => ['tenant-id' => 'company-a']
]);
```

### Publishing Jobs to RabbitMQ

```php
<?php
use Spiral\RoadRunner\Jobs\Jobs;
use Spiral\RoadRunner\Jobs\Queue;

$jobs = new Jobs();
$queue = $jobs->create('rabbit-queue');  // Pipeline name from config

$queue->push('SendEmail', ['recipient' => 'user@example.com'], [
    'priority' => 1,
    'headers' => ['routing-type' => 'urgent']
]);
```

### Consuming Jobs (Universal)

```php
<?php
use Spiral\RoadRunner\Jobs\Consumer;

$consumer = new Consumer();

while ($task = $consumer->waitTask()) {
    try {
        // Process the task (same for both brokers)
        $payload = $task->getPayload();
        $headers = $task->getHeaders();
        $queue = $task->getQueue();  // Pipeline identifier

        // Your business logic here
        match($task->getName()) {
            'ProcessOrder' => handleOrder($payload),
            'SendEmail' => sendEmail($payload),
            default => throw new \Exception('Unknown job type')
        };

        $task->ack();
    } catch (\Exception $e) {
        error_log("Job failed: " . $e->getMessage());
        $task->nack();  // Requeue based on pipeline config
    }
}
```

## Migration Guide

### From RabbitMQ AMQP 0-9-1 Client

**Before** (RabbitMQ-specific):
```yaml
jobs:
  pipelines:
    rabbit-legacy:
      driver: amqp
      addr: "amqp://guest:guest@rabbitmq:5672/"
      # AMQP 0-9-1 specific options
```

**After** (Pure AMQP 1.0):
```yaml
amqp1:
  addr: "amqp://guest:guest@rabbitmq:5672/"
  container_id: "roadrunner-amqp1"

jobs:
  pipelines:
    rabbit-modern:
      driver: amqp1
      config:
        queue: "same-queue-name"
        # AMQP 1.0 benefits: better performance, standardized protocol
```

### From Azure Service Bus SDKs

**Before** (Azure SDK):
```yaml
# Custom Azure Service Bus implementation
azure:
  connection_string: "Endpoint=sb://..."
```

**After** (Pure AMQP 1.0):
```yaml
amqp1:
  addr: "amqps://RootManageSharedAccessKey:key@namespace.servicebus.windows.net:5671/"
  container_id: "roadrunner-azure"

jobs:
  pipelines:
    azure-queue:
      driver: amqp1
      config:
        queue: "existing-queue"
### Migration Benefits

**Performance Improvements:**
- Pure AMQP 1.0 protocol implementation
- Reduced memory footprint without heavy client libraries
- Better connection management and session reuse
- Optimized message serialization/deserialization

**Compatibility Advantages:**
- Single codebase for multiple brokers (Azure Service Bus, RabbitMQ)
- Standardized AMQP 1.0 protocol ensures consistent behavior
- No vendor-specific client library dependencies
- Future-proof against broker-specific API changes

**Configuration Simplification:**
- Unified configuration format for all AMQP 1.0 brokers
- Automatic broker detection and adaptation
- Consistent job handling across different message brokers
- Simplified deployment and maintenance

### Breaking Changes

**Configuration Structure:**
```yaml
# Old structure
jobs:
  pipelines:
    my-queue:
      driver: amqp1
      queue: "test-queue"
      exchange: "test-exchange"

# New structure
jobs:
  pipelines:
    my-queue:
      driver: amqp1
      config:              # Nested under 'config'
        queue: "test-queue"
        exchange: "test-exchange"
```

**Driver Registration:**
- Driver name remains `amqp1`
- Global `amqp1` configuration section now required
- Pipeline configuration moved under `config` key

**PHP Code:**
- No changes required in PHP job publishing/consuming code
- Pipeline names in `$jobs->create('pipeline-name')` remain unchanged
- Message format and headers are fully compatible

Most application code remains unchanged as the driver maintains the same RoadRunner job interface. Only configuration needs to be updated.

### Performance Considerations

**AMQP 1.0 Advantages:**
- More efficient binary protocol compared to AMQP 0-9-1
- Better session management and connection multiplexing
- Credit-based flow control for optimal throughput
- Reduced memory overhead with pure Go implementation

**Tuning Recommendations:**
- Adjust `prefetch` values based on message processing speed
- Use `container_id` for connection identification and debugging
- Monitor connection pooling for high-throughput scenarios
- Configure TLS for production deployments

**Broker-Specific Optimizations:**
- **Azure Service Bus**: Use session-enabled queues for ordered processing
- **RabbitMQ**: Enable AMQP 1.0 plugin and tune exchange configurations

## Testing

The driver includes comprehensive tests covering:

- Connection establishment and management for both Azure Service Bus and RabbitMQ
- Message publishing and consumption with broker detection
- Error handling and automatic reconnection
- TLS configuration and certificate validation
- Pipeline lifecycle management and graceful shutdown

Run tests with:
```bash
cd amqp1
make test
```

Integration tests require:
- Azure Service Bus namespace (for Azure tests)
- RabbitMQ with AMQP 1.0 plugin (for RabbitMQ tests)

## Contributing

1. Follow the existing code style and patterns
2. Add tests for new functionality covering both broker types
3. Update documentation for configuration changes
4. Ensure compatibility with RoadRunner job interface
5. Test against both Azure Service Bus and RabbitMQ

## License

This project follows the same license as the original RoadRunner AMQP driver.

## Acknowledgments

Based on the original AMQP 0-9-1 driver implementation from [roadrunner-server/amqp](https://github.com/roadrunner-server/amqp), completely rewritten for AMQP 1.0 protocol using the pure [Azure go-amqp](https://github.com/Azure/go-amqp) library. This provides unified support for Azure Service Bus and RabbitMQ through the standardized AMQP 1.0 protocol.

**Special Thanks:**
- Azure Service Bus team for the excellent pure Go AMQP 1.0 implementation
- RabbitMQ team for AMQP 1.0 plugin support
- RoadRunner community for the extensible job queue architecture
