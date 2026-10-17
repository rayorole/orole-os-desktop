package agents

import "reflect"

// sessionDelta reports which display fields differ between prev and cur. changed is false
// when none does.
func sessionDelta(prev, cur *Session) (d SessionDelta, changed bool) {
	d.ID, d.UpdatedAt = cur.ID, cur.UpdatedAt
	str := func(was, now string, dst **string) {
		if was != now {
			v := now
			*dst, changed = &v, true
		}
	}
	num := func(was, now int, dst **int) {
		if was != now {
			v := now
			*dst, changed = &v, true
		}
	}
	str(prev.Activity, cur.Activity, &d.Activity)
	str(prev.Tail, cur.Tail, &d.Tail)
	str(prev.LastMessage, cur.LastMessage, &d.LastMessage)
	str(prev.AgentSessionID, cur.AgentSessionID, &d.AgentSessionID)
	str(prev.Model, cur.Model, &d.Model)
	str(prev.TranscriptRef, cur.TranscriptRef, &d.TranscriptRef)
	num(prev.Files, cur.Files, &d.Files)
	num(prev.Additions, cur.Additions, &d.Additions)
	num(prev.Deletions, cur.Deletions, &d.Deletions)
	num(prev.ToolCalls, cur.ToolCalls, &d.ToolCalls)
	num(prev.FailedTools, cur.FailedTools, &d.FailedTools)
	if prev.Tokens != cur.Tokens {
		v := cur.Tokens
		d.Tokens, changed = &v, true
	}
	if prev.Billed != cur.Billed {
		d.Billed, changed = cur.Billed, true // recordTokens replaces it, never updates it in place
	}
	if prev.Usage != cur.Usage {
		d.Usage, changed = cloneUsage(cur.Usage), true
	}
	return d, changed
}

// onlyDeltaFieldsChanged reports whether cur differs from prev in nothing a SessionDelta
// cannot carry (UpdatedAt aside). mutateHot uses it to fall back to a full "agents.updated"
// when a caller touches a field outside the delta set, so a delta can never hide a change.
func onlyDeltaFieldsChanged(prev, cur Session) bool {
	cur.Activity, cur.Tail, cur.LastMessage = prev.Activity, prev.Tail, prev.LastMessage
	cur.AgentSessionID, cur.Model, cur.TranscriptRef = prev.AgentSessionID, prev.Model, prev.TranscriptRef
	cur.Files, cur.Additions, cur.Deletions = prev.Files, prev.Additions, prev.Deletions
	cur.ToolCalls, cur.FailedTools, cur.Tokens, cur.Billed = prev.ToolCalls, prev.FailedTools, prev.Tokens, prev.Billed
	cur.Usage, cur.UpdatedAt = prev.Usage, prev.UpdatedAt
	return reflect.DeepEqual(prev, cur)
}
