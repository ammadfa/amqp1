package amqp1jobs

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"strconv"
	"time"

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

	ctx := struct {
		ID      string              `json:"id"`
		Job     string              `json:"job"`
		Driver  string              `json:"driver"`
		Headers map[string][]string `json:"headers"`
	}{ID: i.ident, Job: i.job, Driver: pluginName, Headers: i.headers}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(ctx); err != nil {
		return nil, errors.E(op, err)
	}

	return buf.Bytes(), nil
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
	return nil
}

// Nack discards the job (no-op for this driver as it's handled in the consumer)
func (i *Item) Nack() error {
	return nil
}

// NackWithOptions discards the job with requeue options (no-op for this driver)
func (i *Item) NackWithOptions(requeue bool, delay int) error {
	return nil
}

// Requeue puts the message back to the queue (no-op for this driver)
func (i *Item) Requeue(headers map[string][]string, delay int) error {
	return nil
}

// KafkaOptions implementation (empty for non-Kafka drivers)
func (i *Item) Offset() int64     { return 0 }
func (i *Item) Partition() int32  { return 0 }
func (i *Item) Topic() string     { return "" }
func (i *Item) Metadata() string  { return "" }

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
	out[ujobID] = id

	if item.Options != nil {
		if item.Options.DelayDuration() > 0 {
			out[ujobTime] = time.Now().UTC().Add(item.Options.DelayDuration()).Format(time.RFC3339)
		}

		if item.Options.Priority != nil {
			out["priority"] = *item.Options.Priority
		}

		if item.Options.Pipeline != "" {
			out["pipeline"] = item.Options.Pipeline
		}

		out["auto_ack"] = item.Options.AutoAck
	}

	out["timeout"] = item.Options.DelayDuration().String()
	out["job"] = item.job

	// Convert string slice headers to individual values
	for key, values := range item.headers {
		if len(values) == 1 {
			out[key] = values[0]
		} else {
			out[key] = values
		}
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
		case ujobID:
			if id, ok := value.(string); ok {
				item.ident = id
			}
		case "job":
			if job, ok := value.(string); ok {
				item.job = job
			}
		case "priority":
			if priority, ok := value.(int64); ok {
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
			// Convert other headers
			switch v := value.(type) {
			case string:
				item.headers[key] = []string{v}
			case []string:
				item.headers[key] = v
			case int:
				item.headers[key] = []string{strconv.Itoa(v)}
			case int64:
				item.headers[key] = []string{strconv.FormatInt(v, 10)}
			default:
				item.headers[key] = []string{fmt.Sprintf("%v", v)}
			}
		}
	}

	if item.ident == "" {
		item.ident = uuid.NewString()
	}

	return item, nil
}
