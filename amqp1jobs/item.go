package amqp1jobs

import (
	"context"
	"encoding/json"

	"time"

	"github.com/Azure/go-amqp"
	"github.com/google/uuid"
	"github.com/roadrunner-server/api/v4/plugins/v4/jobs"
	"github.com/roadrunner-server/errors"
	"go.uber.org/zap"
)

const (
	auto string = "deduced_by_rr"
)

type Item struct {
	// Job contains name of job broker (usually PHP class).
	job string `json:"job"`

	// Ident is unique identifier of the job, should be provided from outside
	ident string `json:"id"`

	// Payload is string data (usually JSON) passed to Job broker.
	payload []byte `json:"payload"`

	// Headers with key-values pairs
	headers map[string][]string `json:"headers"`

	// Options contains set of PipelineOptions specific to job execution. Can be empty.
	Options *Options `json:"options,omitempty"`

	// internal fields for ack/nack at the broker level
	receiver *amqp.Receiver
	message  *amqp.Message
	driver   *Driver
}

// Options carry information about how to handle given job.
type Options struct {
	// Priority is job priority, default - 10
	// pointer to distinguish 0 as a priority and nil as priority not set
	Priority *int64 `json:"priority"`

	// Pipeline manually specified pipeline.
	Pipeline string `json:"pipeline,omitempty"`

	// Delay defines time duration to delay execution for. Defaults to none.
	Delay *int64 `json:"delay,omitempty"`

	// AutoAck option
	AutoAck bool `json:"auto_ack"`
}

// DelayDuration returns delay duration in a form of time.Duration.
func (o *Options) DelayDuration() time.Duration {
	if o.Delay == nil {
		return 0
	}
	return time.Second * time.Duration(*o.Delay)
}

// Body packs job payload into binary payload.
func (i *Item) Body() []byte {
	return i.payload
}

// Context packs job context (job, id, driver) into binary payload.
func (i *Item) Context() ([]byte, error) {
	ctx, err := json.Marshal(
		struct {
			ID       string              `json:"id"`
			Job      string              `json:"job"`
			Driver   string              `json:"driver"`
			Queue    string              `json:"queue"`
			Headers  map[string][]string `json:"headers"`
			Pipeline string              `json:"pipeline"`
		}{
			ID:      i.ident,
			Job:     i.job,
			Driver:  pluginName,
			Headers: i.headers,
			Queue:   i.GroupID(),
			Pipeline: func() string {
				if i.Options != nil {
					return i.Options.Pipeline
				}
				return ""
			}(),
		},
	)

	if err != nil {
		return nil, err
	}

	return ctx, nil
}

// Ident returns job id
func (i *Item) Ident() string {
	return i.ident
}

// ID returns job id
func (i *Item) ID() string {
	return i.ident
}

// Job returns job name
func (i *Item) Job() string {
	return i.job
}

// Headers returns job headers
func (i *Item) Headers() map[string][]string {
	return i.headers
}

// GroupID returns the pipeline name
func (i *Item) GroupID() string {
	if i.Options != nil && i.Options.Pipeline != "" {
		return i.Options.Pipeline
	}
	return "default"
}

// Priority returns the job priority
func (i *Item) Priority() int64 {
	if i.Options != nil && i.Options.Priority != nil {
		return *i.Options.Priority
	}
	return 10 // default priority
}

// Delay returns the delay in seconds
func (i *Item) Delay() int64 {
	if i.Options != nil && i.Options.Delay != nil {
		return *i.Options.Delay
	}
	return 0
}

// AutoAck returns the auto acknowledgment setting
func (i *Item) AutoAck() bool {
	if i.Options != nil {
		return i.Options.AutoAck
	}
	return false
}

// UpdatePriority sets the priority of the job
func (i *Item) UpdatePriority(priority int64) {
	if i.Options == nil {
		i.Options = &Options{}
	}
	i.Options.Priority = &priority
}

// Ack acknowledges the job (no-op for this driver as it's handled in the consumer)
func (i *Item) Ack() error {
	if i.receiver != nil && i.message != nil {
		return i.receiver.AcceptMessage(context.Background(), i.message)
	}
	return nil
}

// Nack discards the job (no-op for this driver as it's handled in the consumer)
func (i *Item) Nack() error {
	if i.receiver != nil && i.message != nil {
		return i.receiver.RejectMessage(context.Background(), i.message, nil)
	}
	return nil
}

// NackWithOptions discards the job with requeue options (no-op for this driver)
func (i *Item) NackWithOptions(requeue bool, delay int) error {
	if i.receiver == nil || i.message == nil {
		return nil
	}
	if requeue {
		if delay > 0 && i.driver != nil {
			// Republish with delay and reject the original
			if i.Options == nil {
				i.Options = &Options{}
			}
			d := int64(delay)
			i.Options.Delay = &d
			_ = i.driver.handleItem(context.Background(), i)
			return i.receiver.RejectMessage(context.Background(), i.message, nil)
		}
		// No delay: release back to the queue
		return i.receiver.ReleaseMessage(context.Background(), i.message)
	}
	// Not requeue: reject
	return i.receiver.RejectMessage(context.Background(), i.message, nil)
}

// Requeue puts the message back to the queue (no-op for this driver)
func (i *Item) Requeue(headers map[string][]string, delay int) error {
	// Merge/override headers
	if headers != nil {
		if i.headers == nil {
			i.headers = map[string][]string{}
		}
		for k, v := range headers {
			i.headers[k] = v
		}
	}

	if i.Options == nil {
		i.Options = &Options{}
	}
	if delay > 0 {
		d := int64(delay)
		i.Options.Delay = &d
	}

	if i.driver != nil {
		_ = i.driver.handleItem(context.Background(), i)
	}

	if i.receiver != nil && i.message != nil {
		return i.receiver.RejectMessage(context.Background(), i.message, nil)
	}
	return nil
}

// Remove unused legacy stubs

// fromJob converts jobs.Message into amqp1 item
func fromJob(job jobs.Message) *Item {
	priority := job.Priority()
	delay := job.Delay()

	return &Item{
		job:     job.Name(),
		ident:   job.ID(),
		payload: job.Payload(),
		headers: job.Headers(),
		Options: &Options{
			Priority: &priority,
			Pipeline: job.GroupID(),
			Delay:    &delay,
			AutoAck:  job.AutoAck(),
		},
	}
}

// pack job metadata into AMQP 1.0 compatible headers
// pack job metadata into headers (RR keys only, parity with amqp driver)
func pack(id string, item *Item) (map[string]interface{}, error) {
	h, err := json.Marshal(item.headers)
	if err != nil {
		return nil, err
	}

	var pipeline string
	var delay int64
	var priority int64
	var autoAck bool

	if item.Options != nil {
		pipeline = item.Options.Pipeline
		if item.Options.Delay != nil {
			delay = *item.Options.Delay
		}
		if item.Options.Priority != nil {
			priority = *item.Options.Priority
		}
		autoAck = item.Options.AutoAck
	}

	return map[string]interface{}{
		jobs.RRID:       id,
		jobs.RRJob:      item.job,
		jobs.RRPipeline: pipeline,
		jobs.RRHeaders:  h,
		jobs.RRDelay:    delay,
		jobs.RRPriority: priority,
		jobs.RRAutoAck:  autoAck,
	}, nil
}

// (header-only helper removed) - header parsing is merged into driver-level unpack

// unpack parses AMQP message payload (AMQP1) and returns Item
func (d *Driver) unpack(msg *amqp.Message) (*Item, error) {
	const op = errors.Op("amqp1_unpack_msg")

	if msg == nil || len(msg.Data) == 0 {
		return nil, errors.E(op, errors.Str("empty amqp message payload"))
	}

	// parse top-level payload JSON
	var payload map[string]interface{}
	if err := json.Unmarshal(msg.Data[0], &payload); err != nil {
		return nil, errors.E(op, err)
	}

	headerIface, ok := payload["header"]
	if !ok {
		return nil, errors.E(op, errors.Str("payload missing header field"))
	}

	headerData, ok := headerIface.(map[string]interface{})
	if !ok {
		return nil, errors.E(op, errors.Str("payload header must be an object"))
	}

	// convert application properties to headers using convHeaders
	item := &Item{
		headers: convHeaders(headerData, d.log),
		Options: &Options{},
	}

	// id
	if v, ok := headerData[jobs.RRID]; !ok {
		item.ident = uuid.NewString()
	} else if id, ok := v.(string); ok {
		item.ident = id
	} else {
		item.ident = uuid.NewString()
	}

	// job
	if v, ok := headerData[jobs.RRJob]; !ok {
		item.job = auto
	} else if job, ok := v.(string); ok {
		item.job = job
	} else {
		item.job = auto
	}

	// pipeline
	if v, ok := headerData[jobs.RRPipeline]; ok {
		if pipeline, ok := v.(string); ok {
			item.Options.Pipeline = pipeline
		}
	}

	// If jobs.RRHeaders contains JSON bytes/object, prefer unmarsaled headers
	if h, ok := headerData[jobs.RRHeaders]; ok {
		switch hh := h.(type) {
		case []byte:
			var unmar map[string][]string
			if err := json.Unmarshal(hh, &unmar); err == nil {
				item.headers = unmar
			} else {
				d.log.Warn("failed to unmarshal headers (should be JSON), continuing execution", zap.Any("headers", hh), zap.Error(err))
			}
		case string:
			var unmar map[string][]string
			if err := json.Unmarshal([]byte(hh), &unmar); err == nil {
				item.headers = unmar
			}
		}
	}

	// delay
	if v, ok := headerData[jobs.RRDelay]; ok {
		switch t := v.(type) {
		case int64:
			d := t
			if d > 0 {
				item.Options.Delay = &d
			}
		case float64:
			d := int64(t)
			if d > 0 {
				item.Options.Delay = &d
			}
		case int:
			d := int64(t)
			if d > 0 {
				item.Options.Delay = &d
			}
		}
	}

	// priority
	if v, ok := headerData[jobs.RRPriority]; ok {
		switch p := v.(type) {
		case int64:
			item.Options.Priority = &p
		case float64:
			pr := int64(p)
			item.Options.Priority = &pr
		case int:
			pr := int64(p)
			item.Options.Priority = &pr
		}
	}

	// auto ack
	if v, ok := headerData[jobs.RRAutoAck]; ok {
		if aa, ok := v.(bool); ok {
			item.Options.AutoAck = aa
		}
	}

	if item.ident == "" {
		item.ident = uuid.NewString()
	}

	// set defaults using driver context
	if item.Options == nil {
		item.Options = &Options{}
	}
	if item.Options.Pipeline == "" {
		if p := d.pipeline.Load(); p != nil {
			item.Options.Pipeline = (*p).Name()
		}
	}

	// parse body
	var bodyStr string
	if b, ok := payload["body"].(string); ok {
		bodyStr = b
	} else {
		if bb, err := json.Marshal(payload["body"]); err == nil {
			bodyStr = string(bb)
		}
	}
	item.payload = []byte(bodyStr)

	return item, nil
}
