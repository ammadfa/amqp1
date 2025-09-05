package amqp1jobs

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"go.uber.org/zap"
)

// convHeaders converts AMQP 1.0 application properties (map[string]any)
// into a map[string][]string similar to AMQP 0-9-1 convHeaders.
func convHeaders(h map[string]any, log *zap.Logger) map[string][]string { //nolint:gocyclo
	ret := make(map[string][]string, len(h))
	for k, v := range h {
		convHeadersAnyType(&ret, k, v, log)
	}
	return ret
}

func convHeadersAnyType(ret *map[string][]string, k string, header any, log *zap.Logger) { //nolint:gocyclo
	switch t := header.(type) {
	case int:
		(*ret)[k] = append((*ret)[k], strconv.Itoa(t))
	case int8:
		(*ret)[k] = append((*ret)[k], strconv.Itoa(int(t)))
	case int16:
		(*ret)[k] = append((*ret)[k], strconv.Itoa(int(t)))
	case int32:
		(*ret)[k] = append((*ret)[k], strconv.Itoa(int(t)))
	case int64:
		(*ret)[k] = append((*ret)[k], strconv.FormatInt(t, 10))
	case uint:
		(*ret)[k] = append((*ret)[k], strconv.FormatUint(uint64(t), 10))
	case uint8:
		(*ret)[k] = append((*ret)[k], strconv.FormatUint(uint64(t), 10))
	case uint16:
		(*ret)[k] = append((*ret)[k], strconv.FormatUint(uint64(t), 10))
	case uint32:
		(*ret)[k] = append((*ret)[k], strconv.FormatUint(uint64(t), 10))
	case uint64:
		(*ret)[k] = append((*ret)[k], strconv.FormatUint(t, 10))
	case float32:
		(*ret)[k] = append((*ret)[k], strconv.FormatFloat(float64(t), 'f', 5, 64))
	case float64:
		(*ret)[k] = append((*ret)[k], strconv.FormatFloat(t, 'f', 5, 64))
	case string:
		(*ret)[k] = append((*ret)[k], t)
	case []string:
		(*ret)[k] = append((*ret)[k], t...)
	case bool:
		if t {
			(*ret)[k] = append((*ret)[k], "true")
		} else {
			(*ret)[k] = append((*ret)[k], "false")
		}
	case []byte:
		(*ret)[k] = append((*ret)[k], string(t))
	case []any:
		for _, v := range t {
			convHeadersAnyType(ret, k, v, log)
		}
	case map[string]any:
		for kk, vv := range t {
			convHeadersAnyType(ret, kk, vv, log)
		}
	case time.Time:
		(*ret)[k] = append((*ret)[k], t.Format(time.RFC3339))
	default:
		// We don't know this type; log and ignore to avoid panics
		if log != nil {
			log.Warn("unknown header type", zap.String("key", k), zap.Any("value", t))
		}
	}
}

func formatDecimal(scale uint8, value int32) string {
	// Calculate the divisor based on the scale.
	divisor := math.Pow10(int(scale))
	// Divide the value by the divisor and format it as a string.
	return fmt.Sprintf("%.*f", scale, float64(value)/divisor)
}
