package dingtalk

import (
	"context"
	"encoding/json"
	"strings"

	dtsdk "github.com/open-dingtalk/dingtalk-stream-sdk-go/client"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/payload"

	"github.com/Tencent/WeKnora/internal/logger"
)

// Directory event types subscribed from the corp contact stream. Only these
// three change who has an account; everything else (dept, label, role) is
// ignored on purpose.
const (
	DirectoryEventUserAdd    = "user_add_org"
	DirectoryEventUserLeave  = "user_leave_org"
	DirectoryEventUserModify = "user_modify_org"
)

// DirectoryEvent is one member-change notification. CorpID scopes it to the
// deployment's corp; events from another corp are dropped by the caller.
type DirectoryEvent struct {
	EventType string
	CorpID    string
	UserIDs   []string
}

// DirectoryEventHandler processes one event. Returning an error makes the SDK
// answer non-200 so DingTalk redelivers; handlers must therefore be idempotent.
type DirectoryEventHandler func(ctx context.Context, evt *DirectoryEvent) error

// DirectoryStream keeps a stream-mode connection open for contact events. It
// mirrors the IM stream client (internal/im/dingtalk): same SDK, separate app
// credentials (the 网页应用, not the robot).
type DirectoryStream struct {
	handler      DirectoryEventHandler
	streamClient *dtsdk.StreamClient
}

// NewDirectoryStream registers the wildcard event router. A wildcard keeps the
// subscription working without one RegisterEventRouter call per topic.
func NewDirectoryStream(cfg Config, handler DirectoryEventHandler) *DirectoryStream {
	cli := dtsdk.NewStreamClient(
		dtsdk.WithAppCredential(&dtsdk.AppCredentialConfig{
			ClientId:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
		}),
	)
	s := &DirectoryStream{handler: handler, streamClient: cli}
	cli.RegisterAllEventRouter(s.onEvent)
	return s
}

// Start dials the stream. Non-blocking, like the IM client.
func (s *DirectoryStream) Start(ctx context.Context) error {
	return s.streamClient.Start(ctx)
}

// Close tears the connection down without triggering auto-reconnect.
func (s *DirectoryStream) Close() {
	s.streamClient.AutoReconnect = false
	s.streamClient.Close()
}

func (s *DirectoryStream) onEvent(ctx context.Context, df *payload.DataFrame) (*payload.DataFrameResponse, error) {
	evt := parseDirectoryEvent(df)
	if evt == nil {
		return payload.NewSuccessDataFrameResponse(), nil
	}
	if err := s.handler(ctx, evt); err != nil {
		logger.Errorf(ctx, "[DingTalk] directory event %s failed: %v", evt.EventType, err)
		// Non-nil error + nil response makes the SDK answer 500, so the
		// event is redelivered instead of silently lost.
		return nil, err
	}
	return payload.NewSuccessDataFrameResponse(), nil
}

// parseDirectoryEvent decodes the event payload. DingTalk sends the type both
// in the frame headers (topic) and inside the JSON body; the body wins because
// the wildcard router makes the header alone ambiguous.
func parseDirectoryEvent(df *payload.DataFrame) *DirectoryEvent {
	if df == nil {
		return nil
	}
	var body struct {
		EventType string   `json:"EventType"`
		CorpID    string   `json:"CorpId"`
		UserIDs   []string `json:"UserId"`
	}
	if err := json.Unmarshal([]byte(df.Data), &body); err != nil {
		logger.Warnf(context.Background(), "[DingTalk] directory event decode failed: %v", err)
		return nil
	}
	eventType := strings.TrimSpace(body.EventType)
	if eventType == "" {
		eventType = strings.TrimSpace(df.GetTopic())
	}
	switch eventType {
	case DirectoryEventUserAdd, DirectoryEventUserLeave, DirectoryEventUserModify:
	default:
		return nil
	}
	ids := make([]string, 0, len(body.UserIDs))
	for _, id := range body.UserIDs {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return &DirectoryEvent{EventType: eventType, CorpID: strings.TrimSpace(body.CorpID), UserIDs: ids}
}
