# AMQP 1.0 Driver for RoadRunner

This is an AMQP 1.0 driver implementation for RoadRunner, based on the existing AMQP 0-9-1 driver but adapted to use the `github.com/rabbitmq/rabbitmq-amqp-go-client` library for AMQP 1.0 protocol support.

## Overview

This driver provides AMQP 1.0 connectivity for RoadRunner's job queue system, offering:

- Full AMQP 1.0 protocol support
- Publisher and consumer capabilities
- Queue and exchange management
- TLS/SSL encryption support
- Connection resilience and retry mechanisms
- Distributed tracing integration
- Event-driven architecture

## Key Differences from AMQP 0-9-1 Driver

### Protocol Changes
- **AMQP 1.0**: Uses standardized messaging protocol with better interoperability
- **Connection Model**: AMQP 1.0 uses containers and links instead of channels
- **Message Format**: AMQP 1.0 has a more structured message format with application properties
- **Flow Control**: Different credit-based flow control mechanism

### Library Migration
- **From**: `github.com/rabbitmq/amqp091-go`
- **To**: `github.com/rabbitmq/rabbitmq-amqp-go-client`

### Configuration Enhancements
Added AMQP 1.0 specific configuration options:
- `container_id`: Unique identifier for the AMQP container
- `link_name`: Name for the AMQP link
- `source_filter`: Message filtering at the source

## Configuration

### Basic Configuration

```yaml
amqp1:
  addr: "amqp://guest:guest@127.0.0.1:5672/"
  container_id: "roadrunner-container"
  
jobs:
  consume: ["tube"]
  
  pipelines:
    tube:
      driver: amqp1
      queue: "test-queue"
      exchange: "test-exchange"
      exchange_type: "direct"
      routing_key: "test"
      prefetch: 10
      priority: 10
      durable: true
```

### TLS Configuration

```yaml
amqp1:
  addr: "amqps://guest:guest@127.0.0.1:5671/"
  tls:
    cert: "/path/to/cert.pem"
    key: "/path/to/key.pem"
    root_ca: "/path/to/ca.pem"
    client_auth_type: "require_and_verify_client_cert"
```

### Advanced Pipeline Configuration

```yaml
jobs:
  pipelines:
    advanced-tube:
      driver: amqp1
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
      delete_queue_on_stop: false
      
      # AMQP 1.0 specific
      container_id: "custom-container"
      link_name: "custom-link"
      source_filter: "application-type = 'order'"
      
      # Exchange settings
      exchange_durable: true
      exchange_auto_delete: false
      
      # Queue settings
      queue_auto_delete: false
      queue_headers:
        x-max-length: 1000
        x-message-ttl: 3600000
```

## Implementation Details

### Driver Architecture

The driver consists of several key components:

1. **Plugin** (`plugin.go`): Main plugin interface and registration
2. **Driver** (`amqp1jobs/driver.go`): Core driver implementation with AMQP 1.0 support
3. **Config** (`amqp1jobs/config.go`): Configuration structure and validation
4. **Item** (`amqp1jobs/item.go`): Message/job item handling and serialization

### AMQP 1.0 Integration

#### Connection Management
```go
// Create AMQP 1.0 connection with container ID
conn, err := amqp.NewConnection(addr, &amqp.ConnectionOptions{
    ContainerID: conf.ContainerID,
    TLSConfig:   tlsConfig,
})

// Initialize management, publisher, and consumer
d.management, _ = d.connection.Management()
d.publisher, _ = d.connection.Publisher()
d.consumer, _ = d.connection.Consumer()
```

#### Message Publishing
```go
amqpMsg := &amqp.Message{
    Data:                  [][]byte{msg.Body()},
    ApplicationProperties: convertToAMQP1Headers(msg.headers),
}

err := d.publisher.Publish(amqpMsg, &amqp.PublishOptions{
    Exchange:   d.exchangeName,
    RoutingKey: rk,
})
```

#### Message Consumption
```go
queueSpec := amqp.QueueSpecification{
    Name:        d.queue,
    Durable:     d.durable,
    AutoDelete:  d.queueAutoDelete,
    Exclusive:   d.exclusive,
    Arguments:   convertHeaders(d.queueHeaders),
}

stream, err := d.consumer.ConsumeFromQueue(queueSpec, &amqp.ConsumerOptions{
    Credit: int32(d.prefetch),
})
```

### Message Flow

1. **Publishing**: Jobs are converted to AMQP 1.0 messages with application properties
2. **Routing**: Messages are routed through exchanges using routing keys
3. **Consumption**: Messages are consumed from queues with credit-based flow control
4. **Processing**: Messages are processed and acknowledged/rejected based on outcome

### Error Handling

The driver implements comprehensive error handling:

- Connection failures trigger automatic reconnection
- Message processing errors respect `requeue_on_fail` setting
- TLS configuration errors are validated at startup
- Resource cleanup on shutdown

### Observability

- **Tracing**: OpenTelemetry integration for distributed tracing
- **Logging**: Structured logging with zap
- **Events**: Event bus for monitoring driver state changes
- **Metrics**: Pipeline state reporting (active, delayed, ready jobs)

## Usage Examples

### Publishing Jobs

```php
<?php
use Spiral\RoadRunner\Jobs\Jobs;
use Spiral\RoadRunner\Jobs\Queue;

$jobs = new Jobs();
$queue = $jobs->create('tube');

$queue->push('job.name', ['data' => 'value'], [
    'priority' => 5,
    'delay' => 30,
    'headers' => ['x-custom' => 'header-value']
]);
```

### Consuming Jobs

```php
<?php
use Spiral\RoadRunner\Jobs\Consumer;

$consumer = new Consumer();

while ($task = $consumer->waitTask()) {
    try {
        // Process the task
        $payload = $task->getPayload();
        $headers = $task->getHeaders();
        
        // Your business logic here
        processJob($payload);
        
        $task->ack();
    } catch (\Exception $e) {
        $task->nack();
    }
}
```

## Migration from AMQP 0-9-1

### Configuration Changes

1. Change driver name from `amqp` to `amqp1`
2. Add AMQP 1.0 specific options like `container_id`
3. Update connection URLs if needed for AMQP 1.0 endpoints

### Code Changes

Most application code remains unchanged as the driver maintains the same RoadRunner job interface. Only configuration needs to be updated.

### Performance Considerations

- AMQP 1.0 may have different performance characteristics
- Credit-based flow control may require tuning `prefetch` values
- Connection patterns may differ from AMQP 0-9-1

## Testing

The driver includes comprehensive tests covering:

- Connection establishment and management
- Message publishing and consumption
- Error handling and recovery
- TLS configuration
- Pipeline lifecycle management

Run tests with:
```bash
go test ./...
```

## Contributing

1. Follow the existing code style and patterns
2. Add tests for new functionality
3. Update documentation for configuration changes
4. Ensure compatibility with RoadRunner job interface

## License

This project follows the same license as the original RoadRunner AMQP driver.

## Acknowledgments

Based on the original AMQP 0-9-1 driver implementation from [roadrunner-server/amqp](https://github.com/roadrunner-server/amqp), adapted for AMQP 1.0 protocol using the RabbitMQ AMQP 1.0 Go client.
