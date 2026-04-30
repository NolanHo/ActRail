package connectapi

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
)

const (
	protoWireVarint = 0
	protoWireBytes  = 2
)

type protoField struct {
	num   uint64
	wire  uint64
	value []byte
	u64   uint64
}

func appendProtoVarint(dst []byte, value uint64) []byte {
	return binary.AppendUvarint(dst, value)
}

func appendProtoKey(dst []byte, field uint64, wire uint64) []byte {
	return appendProtoVarint(dst, field<<3|wire)
}

func appendProtoString(dst []byte, field uint64, value string) []byte {
	if value == "" {
		return dst
	}
	dst = appendProtoKey(dst, field, protoWireBytes)
	dst = appendProtoVarint(dst, uint64(len(value)))
	return append(dst, value...)
}

func appendProtoBytes(dst []byte, field uint64, value []byte) []byte {
	if len(value) == 0 {
		return dst
	}
	dst = appendProtoKey(dst, field, protoWireBytes)
	dst = appendProtoVarint(dst, uint64(len(value)))
	return append(dst, value...)
}

func appendProtoMessage(dst []byte, field uint64, value []byte) []byte {
	return appendProtoBytes(dst, field, value)
}

func appendProtoUint64(dst []byte, field uint64, value uint64) []byte {
	if value == 0 {
		return dst
	}
	dst = appendProtoKey(dst, field, protoWireVarint)
	return appendProtoVarint(dst, value)
}

func appendProtoInt64(dst []byte, field uint64, value int64) []byte {
	if value == 0 {
		return dst
	}
	dst = appendProtoKey(dst, field, protoWireVarint)
	return appendProtoVarint(dst, uint64(value))
}

func readProtoFields(data []byte) ([]protoField, error) {
	fields := make([]protoField, 0)
	for len(data) > 0 {
		key, n := binary.Uvarint(data)
		if n <= 0 {
			return nil, fmt.Errorf("invalid protobuf field key")
		}
		data = data[n:]
		field := protoField{num: key >> 3, wire: key & 7}
		switch field.wire {
		case protoWireVarint:
			value, m := binary.Uvarint(data)
			if m <= 0 {
				return nil, fmt.Errorf("invalid protobuf varint")
			}
			field.u64 = value
			data = data[m:]
		case protoWireBytes:
			length, m := binary.Uvarint(data)
			if m <= 0 || length > uint64(len(data[m:])) || length > uint64(math.MaxInt) {
				return nil, fmt.Errorf("invalid protobuf bytes length")
			}
			start := m
			end := start + int(length)
			field.value = data[start:end]
			data = data[end:]
		default:
			return nil, fmt.Errorf("unsupported protobuf wire type %d", field.wire)
		}
		fields = append(fields, field)
	}
	return fields, nil
}

func protoString(fields []protoField, num uint64) string {
	for _, field := range fields {
		if field.num == num && field.wire == protoWireBytes {
			return string(field.value)
		}
	}
	return ""
}

func protoBytes(fields []protoField, num uint64) []byte {
	for _, field := range fields {
		if field.num == num && field.wire == protoWireBytes {
			return field.value
		}
	}
	return nil
}

func protoUint64(fields []protoField, num uint64) uint64 {
	for _, field := range fields {
		if field.num == num && field.wire == protoWireVarint {
			return field.u64
		}
	}
	return 0
}

func encodeSessionIdentityProto(identity SessionIdentity) []byte {
	var out []byte
	out = appendProtoString(out, 1, identity.SessionID)
	out = appendProtoString(out, 2, identity.RuntimeID)
	return out
}

func decodeSessionIdentityProto(data []byte) (SessionIdentity, error) {
	fields, err := readProtoFields(data)
	if err != nil {
		return SessionIdentity{}, err
	}
	return SessionIdentity{SessionID: protoString(fields, 1), RuntimeID: protoString(fields, 2)}, nil
}

func decodeCommandRequestProto(method string, data []byte) (commandRequest, error) {
	fields, err := readProtoFields(data)
	if err != nil {
		return commandRequest{}, err
	}
	identity, err := decodeSessionIdentityProto(protoBytes(fields, 1))
	if err != nil {
		return commandRequest{}, err
	}
	out := commandRequest{Session: identity}
	switch method {
	case "Send", "Enqueue":
		out.Text = protoString(fields, 2)
	case "RespondUI":
		out.ResponseTo = protoString(fields, 2)
		value, err := json.Marshal(protoString(fields, 3))
		if err != nil {
			return commandRequest{}, err
		}
		out.Value = value
	}
	return out, nil
}

func encodeCommandResponseProto(payload []byte) []byte {
	return appendProtoBytes(nil, 1, payload)
}

func decodeSubscribeRequestProto(data []byte) (subscribeRequest, error) {
	fields, err := readProtoFields(data)
	if err != nil {
		return subscribeRequest{}, err
	}
	return subscribeRequest{AfterEventID: protoUint64(fields, 1)}, nil
}

func encodeEventEnvelopeProto(event EventEnvelope) []byte {
	var out []byte
	out = appendProtoUint64(out, 1, event.ID)
	out = appendProtoString(out, 2, event.Type)
	out = appendProtoString(out, 3, event.Stream)
	out = appendProtoInt64(out, 4, event.UnixMillis)
	payload, err := base64.StdEncoding.DecodeString(event.PayloadJSON)
	if err != nil {
		payload = nil
	}
	out = appendProtoBytes(out, 5, payload)
	return out
}

func requestWantsProto(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "application/connect+proto")
}

func readProtoBody(r io.Reader) ([]byte, error) {
	return io.ReadAll(r)
}
