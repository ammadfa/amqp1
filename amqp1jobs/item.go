package amqp1jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Azure/go-amqp"
	"github.com/google/uuid"
	"github.com/roadrunner-server/api/v4/plugins/v4/jobs"
	"github.com/roadrunner-server/errors"
)

const (
	auto     string = "deduced_by_rr"
	ujobID   string = "rr_id"
	ujobTime string = "rr_time"
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
	const op = errors.Op("amqp1_jobs_context")

	// Pack job metadata for RoadRunner framework
	headerData, err := pack(i.ident, i)
	if err != nil {
		return nil, errors.E(op, err)
	}

	// Return JSON-encoded context for the framework
	contextJSON, err := json.Marshal(headerData)
	if err != nil {
		return nil, errors.E(op, err)
	}

	return contextJSON, nil
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
func pack(id string, item *Item) (map[string]interface{}, error) {
	const op = errors.Op("amqp1_pack_job")

	out := make(map[string]interface{})

	// Required fields for Spiral framework
	out["id"] = id
	out["job"] = item.job
	out["driver"] = "amqp" // Set driver type for Spiral framework
	out["queue"] = item.GroupID() // Use the pipeline/queue name

	// Pipeline information
	pipeline := "default"
	if item.Options != nil && item.Options.Pipeline != "" {
		pipeline = item.Options.Pipeline
	}
	out["pipeline"] = pipeline

	// Headers - must be in the format map[string][]string for Spiral
	out["headers"] = item.headers

	// Timeout - should be numeric (in seconds), not duration string
	timeout := 60 // default timeout in seconds
	if item.Options != nil && item.Options.DelayDuration() > 0 {
		timeout = int(item.Options.DelayDuration().Seconds())
	}
	out["timeout"] = timeout

	// Optional fields
	if item.Options != nil {
		if item.Options.DelayDuration() > 0 {
			out[ujobTime] = time.Now().UTC().Add(item.Options.DelayDuration()).Format(time.RFC3339)
		}

		if item.Options.Priority != nil {
			out["priority"] = *item.Options.Priority
		}

		out["auto_ack"] = item.Options.AutoAck
	}

	return out, nil
}

// unpack extracts job metadata from AMQP 1.0 headers
func unpack(headers map[string]interface{}) (*Item, error) {
	const op = errors.Op("amqp1_unpack_job")

	item := &Item{
		headers: make(map[string][]string),
		Options: &Options{},
	}

	for key, value := range headers {
		switch key {
		case "id", ujobID:
			if id, ok := value.(string); ok {
				item.ident = id
			}
		case "job":
			if job, ok := value.(string); ok {
				item.job = job
			}
		case "headers":
			// Handle headers in the Spiral format
			if headerMap, ok := value.(map[string][]string); ok {
				item.headers = headerMap
			} else if headerMapAny, ok := value.(map[string]interface{}); ok {
				for hkey, hvalue := range headerMapAny {
					switch hv := hvalue.(type) {
					case []interface{}:
						stringSlice := make([]string, len(hv))
						for i, v := range hv {
							stringSlice[i] = fmt.Sprintf("%v", v)
						}
						item.headers[hkey] = stringSlice
					case []string:
						item.headers[hkey] = hv
					case string:
						item.headers[hkey] = []string{hv}
					default:
						item.headers[hkey] = []string{fmt.Sprintf("%v", hv)}
					}
				}
			}
		case "priority":
			switch p := value.(type) {
			case int64:
				item.Options.Priority = &p
			case float64:
				priority := int64(p)
				item.Options.Priority = &priority
			case int:
				priority := int64(p)
				item.Options.Priority = &priority
			}
		case "pipeline":
			if pipeline, ok := value.(string); ok {
				item.Options.Pipeline = pipeline
			}
		case "auto_ack":
			if autoAck, ok := value.(bool); ok {
				item.Options.AutoAck = autoAck
			}
		case "timeout":
			// timeout is numeric in seconds
			switch t := value.(type) {
			case int64:
				delay := t
				if delay > 0 {
					item.Options.Delay = &delay
				}
			case float64:
				delay := int64(t)
				if delay > 0 {
					item.Options.Delay = &delay
				}
			case int:
				delay := int64(t)
				if delay > 0 {
					item.Options.Delay = &delay
				}
			}
		case ujobTime:
			if timeStr, ok := value.(string); ok {
				if t, err := time.Parse(time.RFC3339, timeStr); err == nil {
					delay := int64(time.Until(t).Seconds())
					if delay > 0 {
						item.Options.Delay = &delay
					}
				}
			}
		default:
			// ignore
		}
	}

	if item.ident == "" {
		item.ident = uuid.NewString()
	}

	return item, nil
}
