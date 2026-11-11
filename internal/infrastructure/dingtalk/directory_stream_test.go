package dingtalk

import (
	"context"
	"errors"
	"testing"

	"github.com/open-dingtalk/dingtalk-stream-sdk-go/payload"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDirectoryEventReadsBody(t *testing.T) {
	df := &payload.DataFrame{
		Headers: payload.DataFrameHeader{"topic": "user_add_org"},
		Data:    `{"EventType":"user_add_org","CorpId":"corp-1","UserId":["u1"," u2 ",""]}`,
	}

	evt := parseDirectoryEvent(df)
	require.NotNil(t, evt)
	assert.Equal(t, DirectoryEventUserAdd, evt.EventType)
	assert.Equal(t, "corp-1", evt.CorpID)
	assert.Equal(t, []string{"u1", "u2"}, evt.UserIDs, "blank ids must be dropped")
}

func TestParseDirectoryEventFallsBackToTopic(t *testing.T) {
	df := &payload.DataFrame{
		Headers: payload.DataFrameHeader{"topic": "user_leave_org"},
		Data:    `{"CorpId":"corp-1","UserId":["u9"]}`,
	}

	evt := parseDirectoryEvent(df)
	require.NotNil(t, evt)
	assert.Equal(t, DirectoryEventUserLeave, evt.EventType)
	assert.Equal(t, []string{"u9"}, evt.UserIDs)
}

func TestParseDirectoryEventIgnoresEverythingElse(t *testing.T) {
	cases := map[string]*payload.DataFrame{
		"nil frame":          nil,
		"unknown type":       {Headers: payload.DataFrameHeader{"topic": "org_dept_create"}, Data: `{"EventType":"org_dept_create","UserId":["u1"]}`},
		"no user ids":        {Headers: payload.DataFrameHeader{"topic": "user_add_org"}, Data: `{"EventType":"user_add_org","UserId":[]}`},
		"malformed body":     {Headers: payload.DataFrameHeader{"topic": "user_add_org"}, Data: "{not json"},
		"type only in topic": {Headers: payload.DataFrameHeader{"topic": "label_add"}, Data: `{"UserId":["u1"]}`},
	}
	for name, df := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Nil(t, parseDirectoryEvent(df))
		})
	}
}

func TestDirectoryStreamOnEventAcksAndPropagatesErrors(t *testing.T) {
	var got *DirectoryEvent
	stream := NewDirectoryStream(Config{ClientID: "id", ClientSecret: "secret"},
		func(_ context.Context, evt *DirectoryEvent) error { got = evt; return nil })

	resp, err := stream.onEvent(context.Background(), &payload.DataFrame{
		Headers: payload.DataFrameHeader{"topic": "user_add_org"},
		Data:    `{"EventType":"user_add_org","UserId":["u1"]}`,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, got)
	assert.Equal(t, []string{"u1"}, got.UserIDs)

	// Handler failure must surface as an error so the SDK answers non-200 and
	// DingTalk redelivers the event.
	failing := NewDirectoryStream(Config{ClientID: "id", ClientSecret: "secret"},
		func(context.Context, *DirectoryEvent) error { return errors.New("db down") })
	_, err = failing.onEvent(context.Background(), &payload.DataFrame{
		Headers: payload.DataFrameHeader{"topic": "user_leave_org"},
		Data:    `{"EventType":"user_leave_org","UserId":["u1"]}`,
	})
	require.Error(t, err)
}
