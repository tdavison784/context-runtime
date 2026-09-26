package domain

import "reflect"

type SemanticOperationKind string

const (
	OperationSpan              SemanticOperationKind = "SPAN"
	OperationGrant             SemanticOperationKind = "GRANT"
	OperationRevokeGrant       SemanticOperationKind = "REVOKE_GRANT"
	OperationTransition        SemanticOperationKind = "TRANSITION"
	OperationDeclareObligation SemanticOperationKind = "DECLARE_OBLIGATION"
	OperationMaterialization   SemanticOperationKind = "MATERIALIZATION"
	OperationReevaluate        SemanticOperationKind = "REEVALUATE"
	OperationRegisterResource  SemanticOperationKind = "REGISTER_RESOURCE"
	OperationReportResource    SemanticOperationKind = "REPORT_RESOURCE"
	OperationWorkspace         SemanticOperationKind = "WORKSPACE"
	OperationRegisterRun       SemanticOperationKind = "REGISTER_RUN"
	OperationObservation       SemanticOperationKind = "OBSERVATION"
	OperationCheckpoint        SemanticOperationKind = "CHECKPOINT"
	OperationReplace           SemanticOperationKind = "REPLACE"
)

type SpanIngestIntent struct{ Index int }

func (i SpanIngestIntent) Validate() error {
	if i.Index < 0 {
		return invalid("span operation: negative index")
	}
	return nil
}

// SemanticOperation is the ordered discriminated union hashed by v3. An optional
// SourceSpanIndex authenticates the operation at an earlier span's authority.
// Nil uses the envelope actor; it never promotes a lower-authority source.
type SemanticOperation struct {
	Kind              SemanticOperationKind
	SourceSpanIndex   *int
	Alias             string
	Span              *SpanIngestIntent                   `operation:"SPAN"`
	Grant             *GrantIntent                        `operation:"GRANT"`
	RevokeGrant       *RevokeGrantIntent                  `operation:"REVOKE_GRANT"`
	Transition        *TransitionIntent                   `operation:"TRANSITION"`
	DeclareObligation *DeclareObligationIntent            `operation:"DECLARE_OBLIGATION"`
	Materialization   *SetObligationMaterializationIntent `operation:"MATERIALIZATION"`
	Reevaluate        *ReevaluateIntent                   `operation:"REEVALUATE"`
	RegisterResource  *RegisterResourceIntent             `operation:"REGISTER_RESOURCE"`
	ReportResource    *ReportResourceChangeIntent         `operation:"REPORT_RESOURCE"`
	Workspace         *WorkspaceBindingIntent             `operation:"WORKSPACE"`
	RegisterRun       *RegisterObservationRunIntent       `operation:"REGISTER_RUN"`
	Observation       *ObservationIntent                  `operation:"OBSERVATION"`
	Checkpoint        *CheckpointIntent                   `operation:"CHECKPOINT"`
	Replace           *ReplaceDirectiveIntent             `operation:"REPLACE"`
}

func (o SemanticOperation) Validate() error {
	if o.Alias != "" && !ValidAgentKey(o.Alias) {
		return invalid("operation: invalid local alias")
	}
	if o.SourceSpanIndex != nil && *o.SourceSpanIndex < 0 {
		return invalid("operation: invalid source context")
	}
	v := reflect.ValueOf(o)
	count := 0
	for n := 3; n < v.NumField(); n++ {
		f := v.Field(n)
		if f.IsNil() {
			continue
		}
		count++
		if v.Type().Field(n).Tag.Get("operation") != string(o.Kind) {
			return invalid("operation: discriminant mismatch")
		}
		if err := f.Interface().(interface{ Validate() error }).Validate(); err != nil {
			return err
		}
	}
	if count != 1 || o.Kind == OperationSpan && o.SourceSpanIndex != nil {
		return invalid("operation: exactly one payload required")
	}
	return nil
}
func (o SemanticOperation) RequiresReporter() bool {
	switch o.Kind {
	case OperationDeclareObligation, OperationReevaluate, OperationRegisterResource, OperationReportResource, OperationWorkspace, OperationRegisterRun, OperationObservation, OperationCheckpoint:
		return true
	}
	return false
}
func (o SemanticOperation) Clone() SemanticOperation {
	return cloneSemanticValue(reflect.ValueOf(o)).Interface().(SemanticOperation)
}
func cloneSemanticValue(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(cloneSemanticValue(v.Elem()))
		return out
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for n := 0; n < v.Len(); n++ {
			out.Index(n).Set(cloneSemanticValue(v.Index(n)))
		}
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		for n := 0; n < v.NumField(); n++ {
			out.Field(n).Set(cloneSemanticValue(v.Field(n)))
		}
		return out
	default:
		return v
	}
}
