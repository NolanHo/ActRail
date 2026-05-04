package connectapi

import (
	"encoding/base64"
	"fmt"
	"io"
	"strings"

	actrailv1 "actrail/proto/actrail/v1"
	"google.golang.org/protobuf/proto"
)

func decodeCommandRequestProto(method string, data []byte) (commandRequest, error) {
	out := commandRequest{}
	switch method {
	case "Send":
		var req actrailv1.SendRequest
		if err := proto.Unmarshal(data, &req); err != nil {
			return commandRequest{}, err
		}
		out.Session = sessionIdentityFromProto(req.GetSession())
		out.Text = req.GetText()
	case "Enqueue":
		var req actrailv1.EnqueueRequest
		if err := proto.Unmarshal(data, &req); err != nil {
			return commandRequest{}, err
		}
		out.Session = sessionIdentityFromProto(req.GetSession())
		out.Text = req.GetText()
	case "CancelQueue":
		var req actrailv1.CancelQueueRequest
		if err := proto.Unmarshal(data, &req); err != nil {
			return commandRequest{}, err
		}
		out.Session = sessionIdentityFromProto(req.GetSession())
	case "Interrupt":
		var req actrailv1.InterruptRequest
		if err := proto.Unmarshal(data, &req); err != nil {
			return commandRequest{}, err
		}
		out.Session = sessionIdentityFromProto(req.GetSession())
	case "RespondUI":
		var req actrailv1.RespondUIRequest
		if err := proto.Unmarshal(data, &req); err != nil {
			return commandRequest{}, err
		}
		out.Session = sessionIdentityFromProto(req.GetSession())
		out.ResponseTo = req.GetResponseTo()
		out.Value = []byte(req.GetValue())
	default:
		return commandRequest{}, fmt.Errorf("unknown command method %q", method)
	}
	return out, nil
}

func sessionIdentityFromProto(identity *actrailv1.SessionIdentity) SessionIdentity {
	if identity == nil {
		return SessionIdentity{}
	}
	return SessionIdentity{SessionID: identity.GetSessionId(), RuntimeID: identity.GetRuntimeId()}
}

func encodeCommandResponseProto(payload []byte) []byte {
	data, err := proto.Marshal(&actrailv1.CommandResponse{PayloadJson: payload})
	if err != nil {
		return nil
	}
	return data
}

func decodeSubscribeRequestProto(data []byte) (subscribeRequest, error) {
	var req actrailv1.SubscribeRequest
	if err := proto.Unmarshal(data, &req); err != nil {
		return subscribeRequest{}, err
	}
	return subscribeRequest{AfterEventID: req.GetAfterEventId()}, nil
}

func encodeEventEnvelopeProto(event EventEnvelope) []byte {
	payload, err := base64.StdEncoding.DecodeString(event.PayloadJSON)
	if err != nil {
		payload = nil
	}
	data, err := proto.Marshal(&actrailv1.EventEnvelope{
		Id:          event.ID,
		Type:        event.Type,
		Stream:      event.Stream,
		UnixMillis:  event.UnixMillis,
		PayloadJson: payload,
	})
	if err != nil {
		return nil
	}
	return data
}

func requestWantsProto(contentType string) bool {
	value := strings.ToLower(contentType)
	return strings.Contains(value, "application/connect+proto") || strings.Contains(value, "application/proto")
}

func readProtoBody(r io.Reader) ([]byte, error) {
	return io.ReadAll(r)
}
