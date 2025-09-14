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
        queue: "test-queue"                   # Must exist in Azure Service Bus
        prefetch: 10
        priority: 1
        durable: false
        exclusive: false
  # Broker ack/requeue behavior should be configured on the broker or handled by the application
```

**Azure Service Bus Requirements:**
- Queue must be pre-created in Azure portal or via Azure CLI
- Uses Shared Access Key authentication
- TLS is automatically enabled with `amqps://` protocol
- Routing occurs directly to queue (no exchanges)

Note: the configuration key is `container_id` (snake_case) in YAML/JSON. In Go code this maps to the `ContainerID` struct field via `mapstructure:"container_id"`.

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
        routing_key: "test-queue"
        exchange: "test-queue"                # Use default exchange
        exchange_type: "direct"               # informational; configure server-side
        prefetch: 10
        priority: 10
        durable: true
        exclusive: false
  # Broker ack/requeue behavior should be configured on the broker or handled by the application
```

**RabbitMQ Requirements:**
- Enable AMQP 1.0 plugin: `rabbitmq-plugins enable rabbitmq_amqp1_0`
- Queues and exchanges must be created ahead of time (AMQP 1.0 client does not declare them)
- Supports exchange-based routing; ensure bindings are configured server-side
- Delayed-message support: to use pipeline-level message delays (x-delay) with RabbitMQ you must enable the `rabbitmq_delayed_message_exchange` plugin on the broker. Enable it with:

  ```bash
  rabbitmq-plugins enable rabbitmq_delayed_message_exchange
  ```

  Note: After enabling the plugin you may need to create the delayed exchanges (type `x-delayed-message`) and bindings server-side. The driver will set the `x-delay` application property for delayed messages when configured.

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

  # Azure Service Bus specific headers are managed server-side (use Azure portal/CLI)

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
  # Broker ack/requeue behavior should be configured on the broker or handled by the application

  # RabbitMQ-specific exchange/queue properties are informational and configured on the broker
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
// Create AMQP 1.0 connection using Azure go-amqp v1
ctx := context.TODO()
conn, err := amqp.Dial(ctx, addr, &amqp.ConnOptions{
  ContainerID: conf.ContainerID,
  TLSConfig:   tlsConfig,
})
if err != nil { /* handle */ }

// Session (applies to all brokers)
session, err := conn.NewSession(ctx, nil)
if err != nil { /* handle */ }
defer session.Close()
```

#### Broker Detection and Adaptation
```go
// Automatic broker detection based on connection properties
if d.isAzureServiceBus() {
  // Azure Service Bus: direct queue address
  receiver, err := session.NewReceiver(context.Background(), queueName, &amqp.ReceiverOptions{Credit: int32(prefetch)})
  sender,   err := session.NewSender(context.Background(), queueName, nil)
} else {
  // RabbitMQ (AMQP 1.0 plugin): prefer AMQP v2 address targets or per-message Properties.To for routing.
  // Fixed routing (static exchange + routing-key): create a sender with a v2 target address,
  // e.g. "/exchanges/your-exchange/your-routing-key" and then send messages without setting per-message To.
  //   sender, err := session.NewSender(context.Background(), "/exchanges/your-exchange/your-routing-key", nil)
  // Variable routing (dynamic): create a sender with an empty target and set Properties.To on each message,
  // using a v2 address such as "/exchanges/<exchange>/<routing-key>" for the destination.
  //   sender, err := session.NewSender(context.Background(), "", nil) // use Properties.To for routing per message
  receiver, err := session.NewReceiver(context.Background(), queueName, &amqp.ReceiverOptions{Credit: int32(prefetch)})
  sender,   err := session.NewSender(context.Background(), "", nil) // default exchange; use Properties.To for routing when needed
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
  // RabbitMQ: prefer using AMQP v2 address targeting or per-message Properties.To for routing.
  // Examples:
  // 1) Fixed routing (sender created with a v2 target):
  //    // sender was created with target "/exchanges/your-exchange/your-routing-key"
  //    err := sender.Send(ctx, amqpMsg, nil)
  //
  // 2) Variable routing (sender has empty target; set Properties.To per message):
  //    toAddr := fmt.Sprintf("/exchanges/%s/%s", exchange, routingKey)
  //    amqpMsg.Properties = &amqp.MessageProperties{To: &toAddr}
  //    err := sender.Send(ctx, amqpMsg, nil)
  //
  // Note: Subject-based routing is deprecated for RabbitMQ's AMQP 1.0 plugin; prefer Properties.To (AMQP v2 addressing) for varying routes.
  err := sender.Send(ctx, amqpMsg, nil)
}
```

#### Message Consumption
```go
// Unified consumption pattern
receiver, err := session.NewReceiver(ctx, queueName, &amqp.ReceiverOptions{
  Credit: int32(prefetch),
  // AMQP 1.0 uses credit-based flow control; manual settlement flags are not used here
})

for {
  msg, err := receiver.Receive(ctx, nil)
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
- **Message Processing**: Requeue/reject behavior should be implemented by the consumer application or configured on the broker; the driver will call the appropriate AMQP settlement methods.
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

### AMQP1 Driver Sequence Diagram

![amqp1 driver sequence diagram](./diagram-coderabbit.svg)

The diagram shows the high-level lifecycle for publishing and consuming jobs via the `amqp1` driver. For fixed routes prefer creating a sender with a v2 target address; for variable routing set `Properties.To` on each message. Subject-based routing is deprecated — prefer AMQP address (v2) / `Properties.To` as shown above.

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

## Building Custom RoadRunner Binary

This repository includes configuration to build a custom RoadRunner binary with the AMQP1 plugin integrated.

### Prerequisites

1. **Docker** installed on your system
2. **GitHub Personal Access Token** (optional, but recommended to avoid rate limits)

### Build Options

#### Option 1: Using Makefile (Recommended)

The repository includes a Makefile with convenient build targets:

1. **Set up your GitHub token** (optional but recommended):
   ```bash
   export RT_TOKEN="your_github_personal_access_token"
   ```

2. **Build custom RoadRunner binary**:
   ```bash
   make build-roadrunner
   ```

3. **Extract binary for local use**:
   ```bash
   make extract-binary
   ```

4. **Run the extracted binary**:
   ```bash
   make run-roadrunner
   ```

5. **Verify the build**:
   ```bash
   make test-build
   ```

#### Option 2: Using Docker Commands Directly

If you prefer to use Docker commands directly:

1. **Set up your GitHub token** (optional but recommended):
   ```bash
   export RT_TOKEN="your_github_personal_access_token"
   ```

2. **Build the Docker image**:
   ```bash
   docker build \
     --build-arg RT_TOKEN="${RT_TOKEN}" \
     --build-arg APP_VERSION="2024.3.0" \
     --build-arg BUILD_TIME="$(date +%FT%T%z)" \
     -t roadrunner-amqp1:latest .
   ```

3. **Run the container**:
   ```bash
   docker run -p 8080:8080 roadrunner-amqp1:latest
   ```

#### Option 3: Extract Binary Manually

If you want to extract the built binary manually:

1. **Build the image** (using Option 1 or 2 above)

2. **Create a temporary container and copy the binary**:
   ```bash
   # Create a temporary container
   docker create --name temp-rr roadrunner-amqp1:latest

   # Copy the binary from the container
   docker cp temp-rr:/usr/bin/rr ./rr

   # Remove the temporary container
   docker rm temp-rr

   # Make the binary executable
   chmod +x ./rr
   ```

3. **Run the binary locally**:
   ```bash
   ./rr serve
   ```

### Build Configuration

The build uses `velox.toml` configuration which includes:

- **Custom AMQP1 plugin** from this repository
- **Core RoadRunner plugins** (HTTP, gRPC, Jobs, KV, etc.)
- **Job queue drivers** (SQS, Beanstalk, NATS, Kafka, Google Pub/Sub)
- **Storage drivers** (Redis, Memcached, BoltDB, Memory)
- **Observability** (Metrics, OpenTelemetry, Prometheus)

### Troubleshooting Build Issues

#### GitHub Rate Limits

If you encounter rate limiting issues:

1. Create a GitHub Personal Access Token:
   - Go to GitHub Settings → Developer settings → Personal access tokens
   - Generate a new token with `repo` scope
   - Set the `RT_TOKEN` environment variable

#### Build Errors

1. **Check Docker logs**:
   ```bash
   docker build --no-cache -t roadrunner-amqp1:latest .
   ```

2. **Verify velox.toml syntax**:
   The configuration should be valid TOML format

3. **Check plugin compatibility**:
   Ensure all plugins use compatible versions (v4.x.x for current RoadRunner)

### Development Workflow

To modify the build:

1. **Edit `velox.toml`** to add/remove plugins or change versions
2. **Rebuild the Docker image** with your changes
3. **Verify the new binary** with your configuration

## Contributing

1. Follow the existing code style and patterns
2. Update documentation for configuration changes
3. Ensure compatibility with RoadRunner job interface
4. Validate against both Azure Service Bus and RabbitMQ

## License

This project follows the same license as the original RoadRunner AMQP driver.

## Acknowledgments

Based on the original AMQP 0-9-1 driver implementation from [roadrunner-server/amqp](https://github.com/roadrunner-server/amqp), completely rewritten for AMQP 1.0 protocol using the pure [Azure go-amqp](https://github.com/Azure/go-amqp) library. This provides unified support for Azure Service Bus and RabbitMQ through the standardized AMQP 1.0 protocol.

**Special Thanks:**
- Azure Service Bus team for the excellent pure Go AMQP 1.0 implementation
- RabbitMQ team for AMQP 1.0 plugin support
- RoadRunner community for the extensible job queue architecture
