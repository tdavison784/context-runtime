package domain

// RetrievalOrigin is an exact authenticated holder/turn binding. A model tool
// request must carry its completed output invocation. Only a trusted HARNESS
// request may omit it; persisted results additionally validate their audit event.
type RetrievalOrigin struct {
	Holder                 Principal
	ConversationID, TurnID string
	Invocation             *ToolInvocation
}

func (o RetrievalOrigin) Clone() RetrievalOrigin {
	if o.Invocation != nil {
		v := *o.Invocation
		o.Invocation = &v
	}
	return o
}

func (o RetrievalOrigin) Validate() error {
	if err := o.Holder.Validate(); err != nil {
		return err
	}
	if o.Holder.TaskID == "" || o.Holder.AgentID == "" || o.ConversationID != ConversationIDFor(o.Holder.TaskID, o.Holder.AgentID) || !semanticID(o.TurnID) {
		return invalid("retrieval origin: exact holder, conversation and turn required")
	}
	if o.Invocation == nil {
		if o.Holder.Authority != AuthorityHarness {
			return ErrInvalidAuthorityPromotion
		}
		return nil
	}
	if err := o.Invocation.Validate(); err != nil {
		return err
	}
	if o.Invocation.Principal != o.Holder || o.Invocation.ConversationID != o.ConversationID || o.Invocation.TurnID != o.TurnID {
		return invalid("retrieval origin: invocation holder or turn mismatch")
	}
	return nil
}
