package slack_test

import (
	"encoding/json"
	"github.com/urmzd/mandatum/pkg/address"
	"github.com/urmzd/mandatum/pkg/envelope"
	"github.com/urmzd/mandatum/pkg/topic"
	"time"
)

var target = address.MustParse("slack://T01ABCDEF/C02CHANNEL?thread=1700000000.000100")

func event(seq uint64, kind envelope.Kind, payload string) envelope.Event {
	return envelope.Event{
		Seq:     seq,
		Topic:   topic.MustParse("agent:docs-bot:run_1"),
		RunID:   "run_1",
		Agent:   "docs-bot",
		Origin:  "slack",
		Kind:    kind,
		Payload: []byte(payload),
		At:      time.Unix(1700000100, 0).Add(time.Duration(seq) * time.Second),
	}
}

func textEvent(seq uint64, s string) envelope.Event {
	return event(seq, envelope.KindText, `{"text":`+jsonString(s)+`}`)
}

func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}
