package amqp1jobs

import (
	"context"
	"crypto/tls"
	"encoding/json"
	stderr "errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/rabbitmq-amqp-go-client/pkg/rabbitmqamqp"
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

	// amqp1 connection and management
	environment *amqp.Environment
	connection  *amqp.AmqpConnection
	management  *amqp.AmqpManagement
	publisher   *amqp.Publisher
	consumer    *amqp.Consumer

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
	var conf config
	err := cfg.UnmarshalKey(configKey, &conf)
	if err != nil {
		return nil, errors.E(op, err)
	}

	err = cfg.UnmarshalKey(pluginName, &conf)
	if err != nil {
		return nil, errors.E(op, err)
	}

	err = conf.InitDefault()
	if err != nil {
		return nil, err
	}
	// PARSE CONFIGURATION END -------

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

	jb.environment, jb.connection, err = dial(conf.Addr, &conf)
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
	var conf config
	err := cfg.UnmarshalKey(pluginName, &conf)
	if err != nil {
		return nil, errors.E(op, err)
	}
	err = conf.InitDefault()
	if err != nil {
		return nil, err
	}
	// PARSE CONFIGURATION -------

	// parse prefetch
	prf, err := strconv.Atoi(pipeline.String(prefetch, "10"))
	if err != nil {
		log.Error("prefetch parse, driver will use default (10) prefetch", zap.String("prefetch", pipeline.String(prefetch, "10")))
	}

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
		exchangeName:      pipeline.String(exchangeKey, "amqp1.default"),
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

	jb.environment, jb.connection, err = dial(conf.Addr, &conf)
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

	if d.routingKey == "" && d.exchangeType != "fanout" {
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

	// declare/bind/check the queue
	var err error
	err = d.declareQueue()
	if err != nil {
		return err
	}

	// Create consumer for AMQP 1.0
	d.consumer, err = d.connection.NewConsumer(context.Background(), d.queue, nil)
	if err != nil {
		return errors.E(op, err)
	}

	// start reading messages from the consumer
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
		// d.connection should be protected (redial)
		d.mu.RLock()
		defer d.mu.RUnlock()

		if d.connection != nil {
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

	// For AMQP 1.0, we would need to query queue info through management
	// This is a simplified implementation
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

	if d.consumer != nil {
		err := d.consumer.Close(context.Background())
		if err != nil {
			d.log.Error("close consumer", zap.Error(err))
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

	var err error
	err = d.declareQueue()
	if err != nil {
		return err
	}

	d.consumer, err = d.connection.NewConsumer(context.Background(), d.queue, nil)
	if err != nil {
		return err
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

	// Close consumer and publisher
	if d.consumer != nil {
		_ = d.consumer.Close(context.Background())
	}
	if d.publisher != nil {
		_ = d.publisher.Close(context.Background())
	}
	if d.connection != nil {
		_ = d.connection.Close(context.Background())
	}
	if d.environment != nil {
		_ = d.environment.CloseConnections(context.Background())
	}

	d.log.Debug("pipeline was stopped", zap.String("driver", pipe.Driver()), zap.String("pipeline", pipe.Name()), zap.Time("start", start), zap.Int64("elapsed", time.Since(start).Milliseconds()))
	return nil
}

// handleItem publishes message to AMQP 1.0
func (d *Driver) handleItem(ctx context.Context, msg *Item) error {
	const op = errors.Op("amqp1_driver_handle_item")

	if d.publisher == nil {
		return errors.E(op, errors.Str("publisher not initialized"))
	}

	d.prop.Inject(ctx, propagation.HeaderCarrier(msg.headers))

	// Convert headers to AMQP 1.0 format
	amqpMsg := amqp.NewMessage(msg.Body())
	amqpMsg.ApplicationProperties = convertToAMQP1Headers(msg.headers)

	// Handle timeouts/delays
	if msg.Options.DelayDuration() > 0 {
		atomic.AddInt64(d.delayed, 1)
		// For AMQP 1.0, delays might need to be handled differently
		// This is a simplified implementation
		delayMs := int64(msg.Options.DelayDuration().Seconds() * 1000)
		amqpMsg.ApplicationProperties["x-delay"] = delayMs
	}

	publishResult, err := d.publisher.Publish(ctx, amqpMsg)
	if err != nil {
		if msg.Options.DelayDuration() > 0 {
			atomic.AddInt64(d.delayed, ^int64(0))
		}
		return errors.E(op, err)
	}

	// Check publish result
	switch publishResult.Outcome.(type) {
	case *amqp.StateAccepted:
		// Message was accepted
		return nil
	case *amqp.StateReleased:
		// Message was not routed
		if msg.Options.DelayDuration() > 0 {
			atomic.AddInt64(d.delayed, ^int64(0))
		}
		return errors.E(op, errors.Str("message was not routed"))
	case *amqp.StateRejected:
		// Message was rejected
		if msg.Options.DelayDuration() > 0 {
			atomic.AddInt64(d.delayed, ^int64(0))
		}
		stateType := publishResult.Outcome.(*amqp.StateRejected)
		if stateType.Error != nil {
			return errors.E(op, stateType.Error)
		}
		return errors.E(op, errors.Str("message was rejected"))
	default:
		if msg.Options.DelayDuration() > 0 {
			atomic.AddInt64(d.delayed, ^int64(0))
		}
		return errors.E(op, fmt.Errorf("unknown publish outcome: %v", publishResult.Outcome))
	}
}

func dial(addr string, conf *config) (*amqp.Environment, *amqp.AmqpConnection, error) {
	// Create environment
	env := amqp.NewEnvironment(addr, nil)

	// Create connection
	conn, err := env.NewConnection(context.Background())
	if err != nil {
		return nil, nil, err
	}

	return env, conn, nil
}

func (d *Driver) init() error {
	var err error

	// Initialize management client
	d.management = d.connection.Management()

	// Initialize publisher
	d.publisher, err = d.connection.NewPublisher(context.Background(), &amqp.ExchangeAddress{
		Exchange: d.exchangeName,
		Key:      d.routingKey,
	}, nil)
	if err != nil {
		return fmt.Errorf("failed to create publisher: %w", err)
	}

	return nil
}

func (d *Driver) declareQueue() error {
	if d.management == nil {
		return errors.Str("management client not initialized")
	}

	// Declare exchange if needed
	if d.exchangeName != "" && d.exchangeName != "amqp1.default" {
		var exchangeSpec amqp.IExchangeSpecification
		switch d.exchangeType {
		case "direct":
			exchangeSpec = &amqp.DirectExchangeSpecification{
				Name: d.exchangeName,
			}
		case "topic":
			exchangeSpec = &amqp.TopicExchangeSpecification{
				Name: d.exchangeName,
			}
		case "fanout":
			exchangeSpec = &amqp.FanOutExchangeSpecification{
				Name: d.exchangeName,
			}
		case "headers":
			exchangeSpec = &amqp.HeadersExchangeSpecification{
				Name: d.exchangeName,
			}
		default:
			exchangeSpec = &amqp.DirectExchangeSpecification{
				Name: d.exchangeName,
			}
		}
		_, err := d.management.DeclareExchange(context.Background(), exchangeSpec)
		if err != nil {
			return fmt.Errorf("failed to declare exchange: %w", err)
		}
	}

	// Declare queue - using QuorumQueueSpecification as it's more modern
	queueSpec := &amqp.QuorumQueueSpecification{
		Name: d.queue,
	}
	_, err := d.management.DeclareQueue(context.Background(), queueSpec)
	if err != nil {
		return fmt.Errorf("failed to declare queue: %w", err)
	}

	// Bind queue to exchange if needed
	if d.exchangeName != "" && d.exchangeName != "amqp1.default" && d.routingKey != "" {
		binding := &amqp.ExchangeToQueueBindingSpecification{
			SourceExchange:   d.exchangeName,
			DestinationQueue: d.queue,
			BindingKey:       d.routingKey,
		}
		_, err = d.management.Bind(context.Background(), binding)
		if err != nil {
			return fmt.Errorf("failed to bind queue: %w", err)
		}
	}

	return nil
}

func (d *Driver) listener() {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				d.log.Error("panic in consumer listener", zap.Any("recover", r))
			}
		}()

		for {
			select {
			case <-d.stopCh:
				return
			default:
				deliveryContext, err := d.consumer.Receive(context.Background())
				if stderr.Is(err, context.Canceled) {
					d.log.Info("consumer closed", zap.Error(err))
					return
				}
				if err != nil {
					d.log.Error("failed to receive message", zap.Error(err))
					continue
				}

				if deliveryContext == nil {
					continue
				}

				// Process the message
				err = d.processMessage(deliveryContext)
				if err != nil {
					d.log.Error("failed to process message", zap.Error(err))
					// Handle rejection based on requeueOnFail setting
					if d.requeueOnFail {
						deliveryContext.Requeue(context.Background()) // requeue
					} else {
						deliveryContext.Discard(context.Background(), nil) // don't requeue
					}
				} else {
					deliveryContext.Accept(context.Background())
				}
			}
		}
	}()
}

func (d *Driver) processMessage(deliveryContext *amqp.DeliveryContext) error {
	msg := deliveryContext.Message()
	
	// Convert AMQP 1.0 message to internal job format
	item := &Item{
		job:     "",
		ident:   uuid.NewString(),
		payload: msg.Data[0], // Take first data section
		headers: convertFromAMQP1Headers(msg.ApplicationProperties),
		Options: &Options{},
	}

	// Extract job information from application properties
	if jobName, ok := msg.ApplicationProperties["job"].(string); ok {
		item.job = jobName
	}
	if jobID, ok := msg.ApplicationProperties[ujobID].(string); ok {
		item.ident = jobID
	}

	// Push to pipeline queue
	d.pq.Insert(item)
	return nil
}

func ready(r uint32) bool {
	return r > 0
}

func ptrTo[T any](val T) *T {
	return &val
}

func (d *Driver) setRoutingKey(headers map[string][]string) string {
	if val, ok := headers[xRoutingKey]; ok {
		delete(headers, xRoutingKey)
		if len(val) == 1 && val[0] != "" {
			return val[0]
		}
	}

	return d.routingKey
}

func (d *Driver) queueOrRk() string {
	if d.queue != "" {
		return d.queue
	}

	return d.routingKey
}

// Helper functions for converting between header formats
func convertHeaders(headers map[string]any) map[string]interface{} {
	if headers == nil {
		return nil
	}
	result := make(map[string]interface{})
	for k, v := range headers {
		result[k] = v
	}
	return result
}

func convertToAMQP1Headers(headers map[string][]string) map[string]interface{} {
	result := make(map[string]interface{})
	for k, v := range headers {
		if len(v) == 1 {
			result[k] = v[0]
		} else {
			result[k] = v
		}
	}
	return result
}

func convertFromAMQP1Headers(headers map[string]interface{}) map[string][]string {
	result := make(map[string][]string)
	for k, v := range headers {
		switch val := v.(type) {
		case string:
			result[k] = []string{val}
		case []string:
			result[k] = val
		default:
			result[k] = []string{fmt.Sprintf("%v", val)}
		}
	}
	return result
}

// initTLS initializes TLS configuration
func initTLS(tlsConfig *TLS, cfg *tls.Config) error {
	if tlsConfig.Cert != "" && tlsConfig.Key != "" {
		cert, err := tls.LoadX509KeyPair(tlsConfig.Cert, tlsConfig.Key)
		if err != nil {
			return err
		}
		cfg.Certificates = []tls.Certificate{cert}
	}

	if tlsConfig.RootCA != "" {
		// Load custom CA if provided
		// Implementation would depend on specific requirements
	}

	return nil
}
