package amqp1jobs

import (
	"testing"

	"github.com/google/uuid"
	"github.com/roadrunner-server/api/v4/plugins/v4/jobs"
	"github.com/stretchr/testify/assert"
)

func TestItemCreation(t *testing.T) {
	job := createMockJob("test-job", "test-payload", map[string][]string{
		"header1": {"value1"},
		"header2": {"value2a", "value2b"},
	})

	item := fromJob(job)

	assert.Equal(t, "test-job", item.Job())
	assert.Equal(t, job.ID(), item.ID())
	assert.Equal(t, []byte("test-payload"), item.Body())
	assert.Equal(t, map[string][]string{
		"header1": {"value1"},
		"header2": {"value2a", "value2b"},
	}, item.Headers())
}

func TestPackUnpack(t *testing.T) {
	priority := int64(5)
	delay := int64(30)
	
	item := &Item{
		job:     "test-job",
		ident:   "test-id-123",
		payload: []byte("test payload"),
		headers: map[string][]string{
			"custom-header": {"custom-value"},
		},
		Options: &Options{
			Priority: &priority,
			Pipeline: "test-pipeline",
			Delay:    &delay,
			AutoAck:  true,
		},
	}

	// Pack the item
	headers, err := pack(item.ident, item)
	assert.NoError(t, err)
	assert.NotEmpty(t, headers)

	// Verify packed headers contain expected values
	assert.Equal(t, item.ident, headers[ujobID])
	assert.Equal(t, item.job, headers["job"])
	assert.Equal(t, priority, headers["priority"])
	assert.Equal(t, "test-pipeline", headers["pipeline"])
	assert.Equal(t, true, headers["auto_ack"])
	assert.Equal(t, "custom-value", headers["custom-header"])

	// Unpack the headers
	unpackedItem, err := unpack(headers)
	assert.NoError(t, err)
	assert.NotNil(t, unpackedItem)

	assert.Equal(t, item.ident, unpackedItem.ident)
	assert.Equal(t, item.job, unpackedItem.job)
	assert.Equal(t, priority, *unpackedItem.Options.Priority)
	assert.Equal(t, "test-pipeline", unpackedItem.Options.Pipeline)
	assert.Equal(t, true, unpackedItem.Options.AutoAck)
	assert.Equal(t, []string{"custom-value"}, unpackedItem.headers["custom-header"])
}

func TestItemContext(t *testing.T) {
	item := &Item{
		job:     "test-job",
		ident:   "test-id",
		payload: []byte("payload"),
		headers: map[string][]string{
			"header1": {"value1"},
		},
	}

	ctx, err := item.Context()
	assert.NoError(t, err)
	assert.NotEmpty(t, ctx)
}

func TestOptionsDelayDuration(t *testing.T) {
	tests := []struct {
		name     string
		delay    *int64
		expected int64 // in seconds
	}{
		{
			name:     "no delay",
			delay:    nil,
			expected: 0,
		},
		{
			name:     "zero delay",
			delay:    intPtr(0),
			expected: 0,
		},
		{
			name:     "30 second delay",
			delay:    intPtr(30),
			expected: 30,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := &Options{Delay: tt.delay}
			duration := opts.DelayDuration()
			assert.Equal(t, tt.expected, int64(duration.Seconds()))
		})
	}
}

// Helper functions for testing

func intPtr(i int64) *int64 {
	return &i
}

type mockJob struct {
	name    string
	id      string
	payload []byte
	headers map[string][]string
	priority int64
	pipeline string
	delay   int64
	autoAck bool
}

func (m *mockJob) Name() string                     { return m.name }
func (m *mockJob) ID() string                       { return m.id }
func (m *mockJob) Payload() []byte                  { return m.payload }
func (m *mockJob) Headers() map[string][]string     { return m.headers }
func (m *mockJob) Priority() int64                  { return m.priority }
func (m *mockJob) GroupID() string                  { return m.pipeline }
func (m *mockJob) Delay() int64                     { return m.delay }
func (m *mockJob) AutoAck() bool                    { return m.autoAck }
func (m *mockJob) UpdatePriority(priority int64)    { m.priority = priority }

// KafkaOptions implementation (empty for non-Kafka drivers)
func (m *mockJob) Offset() int64     { return 0 }
func (m *mockJob) Partition() int32  { return 0 }
func (m *mockJob) Topic() string     { return "" }
func (m *mockJob) Metadata() string  { return "" }

func createMockJob(name, payload string, headers map[string][]string) jobs.Message {
	return &mockJob{
		name:     name,
		id:       uuid.NewString(),
		payload:  []byte(payload),
		headers:  headers,
		pipeline: "default",
		autoAck:  false,
	}
}
