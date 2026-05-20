//go:build websocket_legacy
// +build websocket_legacy

package ws

import (
	"sync"

	"github.com/gorilla/websocket"
)

type websocketFrameWriter struct {
	mu    sync.Mutex
	conn  *websocket.Conn
	codec Codec
}

func newWebsocketFrameWriter(conn *websocket.Conn, codec Codec) *websocketFrameWriter {
	return &websocketFrameWriter{conn: conn, codec: codec}
}

func (w *websocketFrameWriter) WriteFrames(frames ...Frame) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, frame := range frames {
		encoded, err := w.codec.Encode(frame)
		if err != nil {
			return err
		}
		if err := w.conn.WriteMessage(websocket.TextMessage, encoded); err != nil {
			return err
		}
	}
	return nil
}
