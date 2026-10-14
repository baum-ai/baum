package codexsession

import (
	"encoding/json"
	"fmt"

	"github.com/baumai/baum/sessions/codextranscript"
	"github.com/baumai/baum/sessions/session"
)

func (f *threadState) addAssistantMessage(line codextranscript.Line, message *codextranscript.Message) error {
	invocation := f.startBatch(line)
	text, err := messageText(message, "output_text")
	if err != nil {
		return err
	}
	content, err := json.Marshal(struct {
		Items json.RawMessage `json:"items"`
		Phase *string         `json:"phase"`
	}{Items: message.Content, Phase: message.Phase})
	if err != nil {
		return err
	}
	locator := message.ID
	kind := "item.id"
	if locator == "" {
		locator = fmt.Sprintf("%s:%d", f.meta.ID, line.Ordinal)
		kind = "rollout.line"
	}
	row := &session.AssistantMessage{
		ID: f.ids.Mint("assistant_message", locator), SessionID: f.session.ID,
		UserInputID: f.activeInput.ID, ModelInvocationID: invocation.ID,
		Content: content, Timestamp: timePtr(line.Timestamp),
	}
	if text != "" {
		row.Text = &text
	}
	f.messages = append(f.messages, row)
	f.vendorRow(row.ID, session.EntityAssistantMessage, kind, locator, line.Ordinal)
	return nil
}
