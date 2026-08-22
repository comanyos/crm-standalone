package api

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/auth"
	"github.com/santoshguru/crm/internal/domain"
)

// customValuesForRead applies field-level visibility before dynamic values
// leave the server. It is deliberately an omission: a caller cannot tell a
// hidden field from one that has no value.
func (s *CRMService) customValuesForRead(ctx context.Context, scope domain.Scope, objectType string, values domain.CustomValues) (map[string]*crmv1.CustomValue, error) {
	if len(values) == 0 {
		return nil, nil
	}
	p, ok := auth.FromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("no authenticated principal"))
	}
	defs, err := s.store.ListCustomFieldDefinitions(ctx, scope, objectType, true)
	if err != nil {
		return nil, connectErr(err)
	}
	byKey := make(map[string]*domain.CustomFieldDefinition, len(defs))
	for _, def := range defs {
		byKey[def.FieldKey] = def
	}
	visible := make(domain.CustomValues, len(values))
	for key, value := range values {
		def := byKey[key]
		// Unknown values cannot be returned. They should be impossible because
		// every write validates keys, but hiding them is safer than turning a
		// future migration defect into a data disclosure.
		if def != nil && roleAllowed(p, def.Config.VisibleToRoles) {
			visible[key] = value
		}
	}
	// Formula values are never persisted or accepted from a writer. Compute
	// them only after hidden source fields were removed, so an expression
	// cannot become an inference channel for data this principal may not see.
	for _, def := range defs {
		if def.Archived() || def.DataType != domain.FieldFormula || !roleAllowed(p, def.Config.VisibleToRoles) {
			continue
		}
		value, err := domain.EvaluateExpression(def.Config.Expression, visible)
		if err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("formula cannot be evaluated"))
		}
		visible[def.FieldKey] = value
	}
	return customValuesToProto(visible), nil
}

// authorizeCustomFieldWrite refuses — rather than silently drops — a field
// value the caller may not edit. This makes a misleading successful update
// impossible and preserves the server boundary even for non-browser clients.
func (s *CRMService) authorizeCustomFieldWrite(ctx context.Context, scope domain.Scope, objectType string, values domain.CustomValues) error {
	if len(values) == 0 {
		return nil
	}
	p, ok := auth.FromContext(ctx)
	if !ok {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("no authenticated principal"))
	}
	defs, err := s.store.ListCustomFieldDefinitions(ctx, scope, objectType, true)
	if err != nil {
		return connectErr(err)
	}
	byKey := make(map[string]*domain.CustomFieldDefinition, len(defs))
	for _, def := range defs {
		byKey[def.FieldKey] = def
	}
	for key := range values {
		// Unknown keys are reported by the domain coercer. Keeping that error
		// here avoids turning an invalid schema key into an authorization
		// oracle.
		if def := byKey[key]; def != nil && !roleAllowed(p, def.Config.EditableToRoles) {
			return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("not permitted to edit custom field"))
		}
	}
	return nil
}

func roleAllowed(p *auth.Principal, allowlist []string) bool {
	// The only principal with metadata-write authority is crm_admin. It must
	// retain recovery access even if a field configuration accidentally omits
	// every role; otherwise a bad policy could make its own correction
	// impossible.
	if p.HasScope("crm:custom_field:write") || len(allowlist) == 0 {
		return true
	}
	roles := make(map[string]bool, len(p.Roles))
	for _, role := range p.Roles {
		roles[role] = true
	}
	for _, role := range allowlist {
		if roles[role] {
			return true
		}
	}
	return false
}

func (s *CRMService) accountForRead(ctx context.Context, scope domain.Scope, value *domain.Account) (*crmv1.Account, error) {
	out := accountToProto(value)
	if out == nil {
		return nil, nil
	}
	var err error
	out.CustomFields, err = s.customValuesForRead(ctx, scope, "account", value.CustomFields)
	return out, err
}
func (s *CRMService) contactForRead(ctx context.Context, scope domain.Scope, value *domain.Contact) (*crmv1.Contact, error) {
	out := contactToProto(value)
	if out == nil {
		return nil, nil
	}
	var err error
	out.CustomFields, err = s.customValuesForRead(ctx, scope, "contact", value.CustomFields)
	return out, err
}
func (s *CRMService) leadForRead(ctx context.Context, scope domain.Scope, value *domain.Lead) (*crmv1.Lead, error) {
	out := leadToProto(value)
	if out == nil {
		return nil, nil
	}
	var err error
	out.CustomFields, err = s.customValuesForRead(ctx, scope, "lead", value.CustomFields)
	return out, err
}
func (s *CRMService) opportunityForRead(ctx context.Context, scope domain.Scope, value *domain.Opportunity) (*crmv1.Opportunity, error) {
	out := opportunityToProto(value)
	if out == nil {
		return nil, nil
	}
	var err error
	out.CustomFields, err = s.customValuesForRead(ctx, scope, "opportunity", value.CustomFields)
	return out, err
}
func (s *CRMService) activityForRead(ctx context.Context, scope domain.Scope, value *domain.Activity) (*crmv1.Activity, error) {
	out := activityToProto(value)
	if out == nil {
		return nil, nil
	}
	var err error
	out.CustomFields, err = s.customValuesForRead(ctx, scope, "activity", value.CustomFields)
	return out, err
}
func (s *CRMService) taskForRead(ctx context.Context, scope domain.Scope, value *domain.Task) (*crmv1.Task, error) {
	out := taskToProto(value)
	if out == nil {
		return nil, nil
	}
	var err error
	out.CustomFields, err = s.customValuesForRead(ctx, scope, "task", value.CustomFields)
	return out, err
}
func (s *CRMService) noteForRead(ctx context.Context, scope domain.Scope, value *domain.Note) (*crmv1.Note, error) {
	out := noteToProto(value)
	if out == nil {
		return nil, nil
	}
	var err error
	out.CustomFields, err = s.customValuesForRead(ctx, scope, "note", value.CustomFields)
	return out, err
}
func (s *CRMService) customRecordForRead(ctx context.Context, scope domain.Scope, value *domain.CustomRecord) (*crmv1.CustomRecord, error) {
	out := customRecordToProto(value)
	if out == nil {
		return nil, nil
	}
	var err error
	out.Fields, err = s.customValuesForRead(ctx, scope, value.ObjectType, value.Fields)
	return out, err
}
