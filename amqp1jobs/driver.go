package amqp1jobs

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	stderr "errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Azure/go-amqp"
	"github.com/roadrunner-server/api/v4/plugins/v4/jobs"
	"github.com/roadrunner-server/errors"
	"github.com/roadrunner-server/events"
	jprop "go.opentelemetry.io/contrib/propagators/jaeger"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

const (
	xRoutingKey        = "x-routing-key"
	pluginName  string = "amqp1"
	tracerName  string = "jobs"
)

var _ jobs.Driver = (*Driver)(nil)

type Configurer interface {
	// UnmarshalKey takes a single key and unmarshal it into a Struct.
	UnmarshalKey(name string, out any) error
	// Has checks if a config section exists.
	Has(name string) bool
}

type Driver struct {
	mu       sync.RWMutex
	log      *zap.Logger
	pq       jobs.Queue
	pipeline atomic.Pointer[jobs.Pipeline]
	tracer   *sdktrace.TracerProvider
	prop     propagation.TextMapPropagator

	// events
	eventBus *events.Bus
	id       string

	// Pure AMQP 1.0 connection and management using Azure go-amqp
	conn     *amqp.Conn
	session  *amqp.Session
	sender   *amqp.Sender
	receiver *amqp.Receiver

	// Connection detection
	isAzureServiceBus bool
	isRabbitMQ        bool

	connStr     string
	containerID string
	linkName    string

	retryTimeout      time.Duration
	prefetch          int
	priority          int64
	exchangeName      string
	queue             string
	exclusive         bool
	exchangeType      string
	routingKey        string
	multipleAck       bool
	requeueOnFail     bool
	durable           bool
	deleteQueueOnStop bool

	// new in 2.12
	exchangeDurable    bool
	exchangeAutoDelete bool
	queueAutoDelete    bool

	// new in 2.12.2
	queueHeaders map[string]any

	// AMQP 1.0 specific
	sourceFilter string

	listeners uint32
	delayed   *int64
	stopCh    chan struct{}
	stopped   uint64
}

// FromConfig initializes AMQP1 pipeline
func FromConfig(tracer *sdktrace.TracerProvider, configKey string, log *zap.Logger, cfg Configurer, pipeline jobs.Pipeline, pq jobs.Queue) (*Driver, error) {
	const op = errors.Op("new_amqp1_consumer")

	if tracer == nil {
		tracer = sdktrace.NewTracerProvider()
	}

	prop := propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}, jprop.Jaeger{})
	otel.SetTextMapPropagator(prop)

	// we need to get two parts of the amqp1 information here.
	// first part - address to connect, it is located in the global section under the amqp1 pluginName
	// second part - queues and other pipeline information
	// if no such key - error
	if !cfg.Has(configKey) {
		return nil, errors.E(op, errors.Errorf("no configuration by provided key: %s", configKey))
	}

	// if no global section
	if !cfg.Has(pluginName) {
		return nil, errors.E(op, errors.Str("no global amqp1 configuration, global configuration should contain amqp1 addrs"))
	}

	// PARSE CONFIGURATION START -------
	log.Debug("starting amqp1 configuration parsing",
		zap.String("configKey", configKey),
		zap.String("pluginName", pluginName))

	var conf config
	err := cfg.UnmarshalKey(configKey, &conf)
	if err != nil {
		return nil, errors.E(op, err)
	}

	log.Debug("after pipeline-specific config load",
		zap.String("exchange", conf.Exchange),
		zap.String("routingKey", conf.RoutingKey))

	err = cfg.UnmarshalKey(pluginName, &conf)
	if err != nil {
		return nil, errors.E(op, err)
	}

	log.Debug("after global config load",
		zap.String("exchange", conf.Exchange),
		zap.String("routingKey", conf.RoutingKey))

	err = conf.InitDefault()
	if err != nil {
		return nil, err
	}
	// PARSE CONFIGURATION END -------

	// DEBUG: Log the parsed configuration values
	log.Debug("amqp1 configuration parsed",
		zap.String("exchange", conf.Exchange),
		zap.String("routingKey", conf.RoutingKey),
		zap.String("queue", conf.Queue),
		zap.String("exchangeType", conf.ExchangeType),
		zap.String("addr", conf.Addr),
		zap.String("username", conf.Username),
		zap.String("password", "***"),  // Don't log password in plain text
		zap.String("containerID", conf.ContainerID))

	eventBus, id := events.NewEventBus()

	jb := &Driver{
		tracer: tracer,
		prop:   prop,
		log:    log,
		pq:     pq,
		stopCh: make(chan struct{}, 1),

		// events
		eventBus: eventBus,
		id:       id,

		priority:    conf.Priority,
		delayed:     ptrTo(int64(0)),
		containerID: conf.ContainerID,
		linkName:    conf.LinkName,

		routingKey:        conf.RoutingKey,
		queue:             conf.Queue,
		durable:           conf.Durable,
		exchangeType:      conf.ExchangeType,
		deleteQueueOnStop: conf.DeleteQueueOnStop,
		exchangeName:      conf.Exchange,
		prefetch:          conf.Prefetch,
		exclusive:         conf.Exclusive,
		multipleAck:       conf.MultipleAck,
		requeueOnFail:     conf.RequeueOnFail,

		// 2.12
		retryTimeout:       time.Duration(conf.RedialTimeout) * time.Second,
		exchangeAutoDelete: conf.ExchangeAutoDelete,
		exchangeDurable:    conf.ExchangeDurable,
		queueAutoDelete:    conf.QueueAutoDelete,
		// 2.12.2
		queueHeaders: conf.QueueHeaders,
		// AMQP 1.0 specific
		sourceFilter: conf.SourceFilter,
	}

	// Detect connection type and initialize accordingly
	jb.isAzureServiceBus = strings.Contains(conf.Addr, "servicebus.windows.net")
	jb.isRabbitMQ = !jb.isAzureServiceBus // Assume RabbitMQ if not Azure Service Bus

	// Connect using pure AMQP 1.0 protocol
	jb.conn, err = connectAMQP(conf.Addr, &conf)
	if err != nil {
		return nil, errors.E(op, err)
	}

	// save address
	jb.connStr = conf.Addr
	err = jb.init()
	if err != nil {
		return nil, errors.E(op, err)
	}

	jb.pipeline.Store(&pipeline)

	return jb, nil
}

// FromPipeline initializes consumer from pipeline
func FromPipeline(tracer *sdktrace.TracerProvider, pipeline jobs.Pipeline, log *zap.Logger, cfg Configurer, pq jobs.Queue) (*Driver, error) {
	const op = errors.Op("new_amqp1_consumer_from_pipeline")
	if tracer == nil {
		tracer = sdktrace.NewTracerProvider()
	}

	prop := propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}, jprop.Jaeger{})
	otel.SetTextMapPropagator(prop)

	// only global section
	if !cfg.Has(pluginName) {
		return nil, errors.E(op, errors.Str("no global amqp1 configuration, global configuration should contain amqp1 addrs"))
	}

	// PARSE CONFIGURATION -------
	log.Debug("starting FromPipeline configuration parsing",
		zap.String("pluginName", pluginName))

	var conf config
	err := cfg.UnmarshalKey(pluginName, &conf)
	if err != nil {
		return nil, errors.E(op, err)
	}

	log.Debug("after global config load in FromPipeline",
		zap.String("exchange", conf.Exchange))

	err = conf.InitDefault()
	if err != nil {
		return nil, err
	}

	log.Debug("after InitDefault in FromPipeline",
		zap.String("exchange", conf.Exchange))
	// PARSE CONFIGURATION -------

	// allow addr override from pipeline
	if addr := pipeline.String("addr", ""); addr != "" {
		conf.Addr = addr
	}

	// parse prefetch
	prf, err := strconv.Atoi(pipeline.String(prefetch, "10"))
	if err != nil {
		log.Error("prefetch parse, driver will use default (10) prefetch", zap.String("prefetch", pipeline.String(prefetch, "10")))
	}

	log.Debug("pipeline values",
		zap.String("exchangeKey_value", pipeline.String(exchangeKey, "")),
		zap.String("routingKey_value", pipeline.String(routingKey, "")),
		zap.String("queue_value", pipeline.String(queue, "")))

	eventBus, id := events.NewEventBus()

	jb := &Driver{
		prop:    prop,
		tracer:  tracer,
		log:     log,
		pq:      pq,
		stopCh:  make(chan struct{}, 1),
		delayed: ptrTo(int64(0)),

		// events
		eventBus: eventBus,
		id:       id,

		routingKey:        pipeline.String(routingKey, ""),
		queue:             pipeline.String(queue, ""),
		exchangeType:      pipeline.String(exchangeType, "direct"),
		exchangeName:      pipeline.String(exchangeKey, ""),
		prefetch:          prf,
		priority:          int64(pipeline.Int(priority, 10)),
		durable:           pipeline.Bool(durable, false),
		deleteQueueOnStop: pipeline.Bool(deleteOnStop, false),
		exclusive:         pipeline.Bool(exclusive, false),
		multipleAck:       pipeline.Bool(multipleAck, false),
		requeueOnFail:     pipeline.Bool(requeueOnFail, false),

		// new in 2.12
		retryTimeout:       time.Duration(pipeline.Int(redialTimeout, 60)) * time.Second,
		exchangeAutoDelete: pipeline.Bool(exchangeAutoDelete, false),
		exchangeDurable:    pipeline.Bool(exchangeDurable, false),
		queueAutoDelete:    pipeline.Bool(queueAutoDelete, false),

		// 2.12.2
		queueHeaders: nil,

		// containers and links
		containerID: conf.ContainerID,
		linkName:    conf.LinkName,

		// AMQP 1.0 specific
		sourceFilter: pipeline.String("source_filter", ""),
	}

	v := pipeline.String(queueHeaders, "")
	if v != "" {
		var tp map[string]any
		err = json.Unmarshal([]byte(v), &tp)
		if err != nil {
			log.Warn("failed to unmarshal headers", zap.String("value", v))
			return nil, err
		}

		jb.queueHeaders = tp
	}

	// Detect connection type and initialize accordingly
	jb.isAzureServiceBus = strings.Contains(conf.Addr, "servicebus.windows.net")
	jb.isRabbitMQ = !jb.isAzureServiceBus // Assume RabbitMQ if not Azure Service Bus

	// Connect using pure AMQP 1.0 protocol
	jb.conn, err = connectAMQP(conf.Addr, &conf)
	if err != nil {
		return nil, errors.E(op, err)
	}

	// save address
	jb.connStr = conf.Addr

	err = jb.init()
	if err != nil {
		return nil, errors.E(op, err)
	}

	// register the pipeline
	jb.pipeline.Store(&pipeline)

	return jb, nil
}

func (d *Driver) Push(ctx context.Context, job jobs.Message) error {
	const op = errors.Op("amqp1_driver_push")

	ctx, span := trace.SpanFromContext(ctx).TracerProvider().Tracer(tracerName).Start(ctx, "amqp1_push")
	defer span.End()

	// For RabbitMQ with named exchanges, routing key is often required; for Azure Service Bus, it's queue-centric and may be empty
	if !d.isAzureServiceBus && d.routingKey == "" && d.exchangeType != "fanout" && d.exchangeName != "" {
		return errors.Str("empty routing key, consider adding the routing key name to the AMQP1 configuration")
	}

	// load atomic value
	pipe := *d.pipeline.Load()
	if pipe.Name() != job.GroupID() {
		return errors.E(op, errors.Errorf("no such pipeline: %s, actual: %s", job.GroupID(), pipe.Name()))
	}

	err := d.handleItem(ctx, fromJob(job))
	if err != nil {
		return errors.E(op, err)
	}

	return nil
}

func (d *Driver) Run(ctx context.Context, p jobs.Pipeline) error {
	start := time.Now().UTC()
	const op = errors.Op("amqp1_driver_run")

	_, span := trace.SpanFromContext(ctx).TracerProvider().Tracer(tracerName).Start(ctx, "amqp1_run")
	defer span.End()

	pipe := *d.pipeline.Load()
	if pipe.Name() != p.Name() {
		return errors.E(op, errors.Errorf("no such pipeline registered: %s", pipe.Name()))
	}

	if d.queue == "" {
		return errors.Str("empty queue name, consider adding the queue name to the AMQP1 configuration")
	}

	// protect connection (redial)
	d.mu.Lock()
	defer d.mu.Unlock()

	// For Azure Service Bus and RabbitMQ, queues should exist before consuming
	// We don't need to declare them as both brokers expect pre-existing queues

	// Create receiver for AMQP 1.0 consumption
	var err error
	// apply link credit (prefetch) if available
	var rcvOpts *amqp.ReceiverOptions
	if d.prefetch > 0 {
		rcvOpts = &amqp.ReceiverOptions{Credit: int32(d.prefetch)}
	}
	d.receiver, err = d.session.NewReceiver(context.Background(), d.queue, rcvOpts)
	if err != nil {
		return errors.E(op, fmt.Errorf("failed to create AMQP receiver: %w", err))
	}

	// start reading messages from the receiver
	d.listener()

	atomic.StoreUint32(&d.listeners, 1)
	d.log.Debug("pipeline was started", zap.String("driver", pipe.Driver()), zap.String("pipeline", pipe.Name()), zap.Time("start", start), zap.Int64("elapsed", time.Since(start).Milliseconds()))
	return nil
}

func (d *Driver) State(ctx context.Context) (*jobs.State, error) {
	const op = errors.Op("amqp1_driver_state")
	_, span := trace.SpanFromContext(ctx).TracerProvider().Tracer(tracerName).Start(ctx, "amqp1_state")
	defer span.End()

	pipe := *d.pipeline.Load()

	// if there is no queue, check the connection instead
	if d.queue == "" {
		// d.conn should be protected (redial)
		d.mu.RLock()
		defer d.mu.RUnlock()

		if d.conn != nil {
			return &jobs.State{
				Priority: uint64(pipe.Priority()), //nolint:gosec
				Pipeline: pipe.Name(),
				Driver:   pipe.Driver(),
				Delayed:  atomic.LoadInt64(d.delayed),
				Ready:    ready(atomic.LoadUint32(&d.listeners)),
			}, nil
		}

		return nil, errors.Str("connection is closed, can't get the state")
	}

	// For pure AMQP 1.0, we don't have management APIs to query queue info
	// Both Azure Service Bus and RabbitMQ would require separate management APIs
	return &jobs.State{
		Priority: uint64(pipe.Priority()), //nolint:gosec
		Pipeline: pipe.Name(),
		Driver:   pipe.Driver(),
		Queue:    d.queue,
		Active:   0, // Would need management API to get actual count
		Delayed:  atomic.LoadInt64(d.delayed),
		Ready:    ready(atomic.LoadUint32(&d.listeners)),
	}, nil
}

func (d *Driver) Pause(ctx context.Context, p string) error {
	start := time.Now().UTC()
	pipe := *d.pipeline.Load()

	_, span := trace.SpanFromContext(ctx).TracerProvider().Tracer(tracerName).Start(ctx, "amqp1_pause")
	defer span.End()

	if pipe.Name() != p {
		return errors.Errorf("no such pipeline: %s", pipe.Name())
	}

	if d.queue == "" {
		return errors.Str("empty queue name, consider adding the queue name to the AMQP1 configuration")
	}

	// no active listeners
	if atomic.LoadUint32(&d.listeners) == 0 {
		return errors.Str("no active listeners, nothing to pause")
	}

	atomic.AddUint32(&d.listeners, ^uint32(0))

	// protect connection (redial)
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.receiver != nil {
		err := d.receiver.Close(context.Background())
		if err != nil {
			d.log.Error("close receiver", zap.Error(err))
			return err
		}
	}

	d.log.Debug("pipeline was paused",
		zap.String("driver", pipe.Driver()),
		zap.String("pipeline", pipe.Name()),
		zap.Time("start", start),
		zap.Int64("elapsed", time.Since(start).Milliseconds()),
	)

	return nil
}

func (d *Driver) Resume(ctx context.Context, p string) error {
	start := time.Now().UTC()

	_, span := trace.SpanFromContext(ctx).TracerProvider().Tracer(tracerName).Start(ctx, "amqp1_resume")
	defer span.End()

	pipe := *d.pipeline.Load()
	if pipe.Name() != p {
		return errors.Errorf("no such pipeline: %s", pipe.Name())
	}

	if d.queue == "" {
		return errors.Str("empty queue name, consider adding the queue name to the AMQP1 configuration")
	}

	// protect connection (redial)
	d.mu.Lock()
	defer d.mu.Unlock()

	// no active listeners
	if atomic.LoadUint32(&d.listeners) == 1 {
		return errors.Str("amqp1 listener is already in the active state")
	}

	// For Azure Service Bus and RabbitMQ, queues should exist before consuming
	// We don't need to declare them as both brokers expect pre-existing queues

	var err error
	var rcvOpts2 *amqp.ReceiverOptions
	if d.prefetch > 0 {
		rcvOpts2 = &amqp.ReceiverOptions{Credit: int32(d.prefetch)}
	}
	d.receiver, err = d.session.NewReceiver(context.Background(), d.queue, rcvOpts2)
	if err != nil {
		return fmt.Errorf("failed to create AMQP receiver: %w", err)
	}

	// run listener
	d.listener()

	// increase the number of listeners
	atomic.AddUint32(&d.listeners, 1)
	d.log.Debug("pipeline was resumed",
		zap.String("driver", pipe.Driver()),
		zap.String("pipeline", pipe.Name()),
		zap.Time("start", start),
		zap.Int64("elapsed", time.Since(start).Milliseconds()),
	)

	return nil
}

func (d *Driver) Stop(ctx context.Context) error {
	start := time.Now().UTC()

	_, span := trace.SpanFromContext(ctx).TracerProvider().Tracer(tracerName).Start(ctx, "amqp1_stop")
	defer span.End()

	d.eventBus.Unsubscribe(d.id)

	atomic.StoreUint64(&d.stopped, 1)
	d.stopCh <- struct{}{}

	pipe := *d.pipeline.Load()

	// remove all pending JOBS associated with the pipeline
	_ = d.pq.Remove(pipe.Name())

	// Close Azure AMQP connections
	if d.receiver != nil {
		_ = d.receiver.Close(context.Background())
	}
	if d.sender != nil {
		_ = d.sender.Close(context.Background())
	}
	if d.session != nil {
		_ = d.session.Close(context.Background())
	}
	if d.conn != nil {
		_ = d.conn.Close()
	}

	d.log.Debug("pipeline was stopped", zap.String("driver", pipe.Driver()), zap.String("pipeline", pipe.Name()), zap.Time("start", start), zap.Int64("elapsed", time.Since(start).Milliseconds()))
	return nil
}

// handleItem publishes message to AMQP 1.0 using pure Azure go-amqp
func (d *Driver) handleItem(ctx context.Context, msg *Item) error {
    const op = errors.Op("amqp1_driver_handle_item")

    if d.sender == nil {
        return errors.E(op, errors.Str("sender not initialized"))
    }

    d.prop.Inject(ctx, propagation.HeaderCarrier(msg.headers))

    // Build structured payload with header object and body
    headerData, err := pack(msg.ident, msg)
    if err != nil {
        return errors.E(op, err)
    }

    payload := map[string]any{
        "header": headerData,       // embed as object, not string
        "body":   string(msg.Body()),
    }

    payloadJSON, err := json.Marshal(payload)
    if err != nil {
        return errors.E(op, err)
    }

    // Avoid logging full payloads; log sizes or ids instead
    d.log.Debug("AMQP1 payload prepared",
        zap.String("id", msg.ident),
        zap.Int("headerKeys", len(headerData)),
        zap.Int("payloadBytes", len(payloadJSON)),
    )

    // …continue with sending payloadJSON over d.sender…
    if err := d.sender.Send(payloadJSON); err != nil {
        return errors.E(op, err)
    }

    return nil
}
	// Create Azure AMQP message
	amqpMsg := &amqp.Message{
		Data: [][]byte{payloadJSON},
	}

	// Convert headers to Azure AMQP format
	if len(msg.headers) > 0 {
		amqpMsg.ApplicationProperties = make(map[string]interface{})
		for key, values := range msg.headers {
			if len(values) > 0 {
				amqpMsg.ApplicationProperties[key] = values[0]
			}
		}
	}

	// Handle routing key for RabbitMQ
	if d.isRabbitMQ && d.routingKey != "" && d.exchangeName != "" {
		if amqpMsg.ApplicationProperties == nil {
			amqpMsg.ApplicationProperties = make(map[string]interface{})
		}
		amqpMsg.ApplicationProperties["x-routing-key"] = d.routingKey
		amqpMsg.Properties = &amqp.MessageProperties{
			Subject: &d.routingKey, // Subject is used for routing in AMQP 1.0
		}
	}

	// Handle timeouts/delays
	if msg.Options.DelayDuration() > 0 {
		atomic.AddInt64(d.delayed, 1)

		if d.isAzureServiceBus {
			// Azure Service Bus uses scheduled messages for delays
			scheduleTime := time.Now().Add(msg.Options.DelayDuration())
			amqpMsg.Annotations = make(amqp.Annotations)
			amqpMsg.Annotations["x-opt-scheduled-enqueue-time"] = scheduleTime

			d.log.Debug("Azure message scheduled",
				zap.Duration("delay", msg.Options.DelayDuration()),
				zap.Time("scheduleTime", scheduleTime))
		} else {
			// RabbitMQ: use x-delay header (requires rabbitmq-delayed-message-exchange plugin)
			delayMs := int64(msg.Options.DelayDuration().Seconds() * 1000)
			if amqpMsg.ApplicationProperties == nil {
				amqpMsg.ApplicationProperties = make(map[string]interface{})
			}
			amqpMsg.ApplicationProperties["x-delay"] = delayMs

			d.log.Debug("RabbitMQ message delayed",
				zap.Duration("delay", msg.Options.DelayDuration()),
				zap.Int64("delayMs", delayMs))
		}
	}

	// Send the message
	err = d.sender.Send(ctx, amqpMsg, nil)
	if err != nil {
		if msg.Options.DelayDuration() > 0 {
			atomic.AddInt64(d.delayed, ^int64(0))
		}
		return errors.E(op, fmt.Errorf("failed to send message: %w", err))
	}

	d.log.Debug("Message sent successfully",
		zap.String("queue", d.queue),
		zap.Bool("isAzureServiceBus", d.isAzureServiceBus))

	return nil
}

// connectAMQP creates a pure AMQP 1.0 connection using Azure go-amqp library
// Compatible with both Azure Service Bus and RabbitMQ
func connectAMQP(addr string, conf *config, log *zap.Logger) (*amqp.Conn, error) {
    if log == nil {
        log = zap.NewNop()
    }
    log.Debug("amqp: connecting", zap.String("addr", addr), zap.String("container_id", conf.ContainerID))

    // Parse the connection string to extract credentials and host
    parsedURL, err := url.Parse(addr)
    if err != nil {
        return nil, fmt.Errorf("failed to parse connection URL: %w", err)
    }

    // Extract credentials from URL
    var username, password string
    if parsedURL.User != nil {
        username = parsedURL.User.Username()
        password, _ = parsedURL.User.Password()
        // URL decode the password
        if decodedPassword, err := url.QueryUnescape(password); err == nil {
            password = decodedPassword
        }
        log.Debug("amqp: credentials extracted (username only)", zap.String("username", username))
    }

    // Override with explicit config values if provided
    if conf.Username != "" {
        username = conf.Username
    }
    if conf.Password != "" {
        password = conf.Password
    }

    // Create clean host address without credentials
    hostAddr := fmt.Sprintf("%s://%s", parsedURL.Scheme, parsedURL.Host)
    log.Debug("amqp: clean host address", zap.String("host", hostAddr))

    // Detect broker type
    isAzureServiceBus := strings.Contains(addr, "servicebus.windows.net")
    isRabbitMQ := !isAzureServiceBus
    log.Debug("amqp: broker detection",
        zap.Bool("azure_service_bus", isAzureServiceBus),
        zap.Bool("rabbitmq", isRabbitMQ),
    )

    // Configure AMQP connection options
    connOptions := &amqp.ConnOptions{}

    // Set container ID if provided
    if conf.ContainerID != "" {
        connOptions.ContainerID = conf.ContainerID
        log.Debug("amqp: container id set", zap.String("container_id", conf.ContainerID))
    }

    // Configure SASL authentication
    if username != "" && password != "" {
        log.Debug("amqp: using SASL PLAIN")
        connOptions.SASLType = amqp.SASLTypePlain(username, password)
    }

    // Configure TLS if using amqps
    if strings.HasPrefix(parsedURL.Scheme, "amqps") {
        log.Debug("amqp: configuring TLS")
        tlsConfig := &tls.Config{
            ServerName: parsedURL.Hostname(),
            MinVersion: tls.VersionTLS12,
        }

        if conf.TLS != nil {
            if conf.TLS.InsecureSkipVerify {
                tlsConfig.InsecureSkipVerify = true
                log.Warn("amqp: TLS verification disabled via config")
            }

            if conf.TLS.RootCA != "" {
                caCert, err := os.ReadFile(conf.TLS.RootCA)
                if err != nil {
                    return nil, fmt.Errorf("failed to read CA certificate: %w", err)
                }
                rootCAPool := x509.NewCertPool()
                if !rootCAPool.AppendCertsFromPEM(caCert) {
                    return nil, fmt.Errorf("failed to parse CA certificate")
                }
                tlsConfig.RootCAs = rootCAPool
                log.Debug("amqp: custom RootCA loaded")
            }

            if conf.TLS.Cert != "" && conf.TLS.Key != "" {
                cert, err := tls.LoadX509KeyPair(conf.TLS.Cert, conf.TLS.Key)
                if err != nil {
                    return nil, fmt.Errorf("failed to load client certificate: %w", err)
                }
                tlsConfig.Certificates = []tls.Certificate{cert}
                log.Debug("amqp: client certificate loaded")
            }
        }

        connOptions.TLSConfig = tlsConfig
    }

    // Set hostname for proper TLS verification
    connOptions.HostName = parsedURL.Hostname()
    log.Debug("amqp: dialing")

    // Create the connection
    conn, err := amqp.Dial(context.Background(), hostAddr, connOptions)
    if err != nil {
        return nil, fmt.Errorf("failed to connect to AMQP broker: %w", err)
    }

    log.Debug("amqp: connection established")
    return conn, nil
}

func (d *Driver) init() error {
	var err error

	d.log.Debug("AMQP1 Driver Debug",
		zap.String("exchangeName", d.exchangeName),
		zap.String("routingKey", d.routingKey),
		zap.Bool("isAzureServiceBus", d.isAzureServiceBus),
		zap.Bool("isRabbitMQ", d.isRabbitMQ))

	// Create session
	d.session, err = d.conn.NewSession(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("failed to create AMQP session: %w", err)
	}

	// For Azure Service Bus, queues must be pre-created in the portal
	// For RabbitMQ, we can declare queues via AMQP, but Azure go-amqp doesn't support management operations
	// So we'll rely on pre-existing queues for both brokers

	// Create sender for publishing messages
	// Azure Service Bus uses queue names directly, RabbitMQ can use exchanges or queues
	var targetAddress string
	if d.isAzureServiceBus {
		// Azure Service Bus: send directly to queue
		targetAddress = d.queue
		d.log.Debug("Azure Service Bus: using queue address", zap.String("queue", d.queue))
	} else if d.exchangeName == "" {
		// RabbitMQ: default exchange, use queue name
		targetAddress = d.queue
		d.log.Debug("RabbitMQ: using default exchange with queue", zap.String("queue", d.queue))
	} else {
		// RabbitMQ: named exchange
		targetAddress = d.exchangeName
		d.log.Debug("RabbitMQ: using named exchange", zap.String("exchange", d.exchangeName), zap.String("routingKey", d.routingKey))
	}

	d.sender, err = d.session.NewSender(context.Background(), targetAddress, nil)
	if err != nil {
		return fmt.Errorf("failed to create AMQP sender: %w", err)
	}

	d.log.Debug("AMQP1 Driver initialized successfully",
		zap.String("targetAddress", targetAddress),
		zap.Bool("isAzureServiceBus", d.isAzureServiceBus))

	return nil
}

func (d *Driver) listener() {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				d.log.Error("panic in receiver listener", zap.Any("recover", r))
			}
		}()

		for {
			select {
			case <-d.stopCh:
				return
			default:
				// Receive message using Azure AMQP receiver
				message, err := d.receiver.Receive(context.Background(), nil)
				if stderr.Is(err, context.Canceled) {
					d.log.Info("receiver closed", zap.Error(err))
					return
				}
				if err != nil {
					d.log.Error("failed to receive message", zap.Error(err))
					continue
				}

				if message == nil {
					continue
				}

				// Process the message
				err = d.processMessage(message)
				if err != nil {
					d.log.Error("failed to process message", zap.Error(err))
				}
			}
		}
	}()
}

func (d *Driver) processMessage(message *amqp.Message) error {
	// Get message data
	var messageData []byte
	if len(message.Data) > 0 {
		messageData = message.Data[0] // Azure AMQP stores data as [][]byte
	} else {
		return fmt.Errorf("message has no data")
	}

	// Parse the JSON payload
	var payload map[string]interface{}
	err := json.Unmarshal(messageData, &payload)
	if err != nil {
		return fmt.Errorf("failed to unmarshal message payload: %w", err)
	}

	// Debug: Log received payload
	d.log.Debug("AMQP1 Received Payload", zap.String("payload", string(messageData)))

	// Extract header and body
	headerStr, ok := payload["header"].(string)
	if !ok {
		return fmt.Errorf("payload missing header field")
	}

	bodyStr, ok := payload["body"].(string)
	if !ok {
		return fmt.Errorf("payload missing body field")
	}

	// Debug: Log header before parsing
	d.log.Debug("AMQP1 Header before parsing", zap.String("header", headerStr))

	// Parse header JSON to get job metadata
	var headerData map[string]interface{}
	err = json.Unmarshal([]byte(headerStr), &headerData)
	if err != nil {
		return fmt.Errorf("failed to unmarshal header: %w", err)
	}

	// Debug: Log parsed header data
	headerDataJSON, _ := json.Marshal(headerData)
	d.log.Debug("AMQP1 Parsed Header Data", zap.String("headerData", string(headerDataJSON)))

	// Unpack job metadata using existing unpack function
	item, err := unpack(headerData)
	if err != nil {
		return fmt.Errorf("failed to unpack job metadata: %w", err)
	}

	// Set the actual job payload and broker control handles
	item.payload = []byte(bodyStr)
	item.receiver = d.receiver
	item.message = message
	item.driver = d

	// Push to pipeline queue; ACK/NACK will be triggered later by the Jobs plugin calling item methods
	d.pq.Insert(item)
	return nil
}

func ready(r uint32) bool {
	return r > 0
}

func ptrTo[T any](val T) *T {
	return &val
}

// removed unused helpers setRoutingKey and queueOrRk
